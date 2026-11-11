package command_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/httpx"
)

// TestMain 在设置了 daemonHelperEnv 时作为被升级的常驻进程运行，见 daemonHelper。
func TestMain(m *testing.M) {
	if os.Getenv(daemonHelperEnv) != "" {
		daemonHelper()
		return
	}
	os.Exit(m.Run())
}

const daemonHelperEnv = "COMMAND_DAEMON_HELPER"

// daemonHelper 是测试用的常驻进程：版本号取自 VERSION_FILE，内容为 fail 时在装配阶段失败。
func daemonHelper() {
	version, err := os.ReadFile(os.Getenv("VERSION_FILE"))
	if err != nil {
		os.Exit(1)
	}
	d, err := command.NewDaemon(nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer d.Stop()
	ln, err := d.Listen("tcp", os.Getenv("ADDR"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	time.Sleep(200 * time.Millisecond) // 模拟打开数据库等装配工作
	if string(version) == "fail" {
		os.Exit(4)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.Write(version)
	})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	err = d.Run(ctx, func(ctx context.Context) error {
		err := httpx.Serve(ctx, httpx.DefaultConfig(), ln, handler, httpx.ServeOptions{})
		time.Sleep(500 * time.Millisecond) // 模拟等待处理中的任务
		return err
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func TestDaemonWithoutNotifySocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	d, err := command.NewDaemon(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	ln, err := d.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- d.Run(ctx, func(ctx context.Context) error {
			<-ctx.Done()
			return nil
		})
	}()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

func TestDaemonListenError(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	d, err := command.NewDaemon(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Listen("tcp", "256.0.0.1:0"); err == nil || !strings.Contains(err.Error(), "command: listen tcp 256.0.0.1:0") {
		t.Fatalf("Listen error = %v", err)
	}
}

// TestDaemonUpgrade 模拟 systemd：接收 sd_notify，向主进程发送 SIGHUP，同时持续请求，
// 确认升级期间没有失败的请求、旧进程排空时的升级会等它退出、新版本失败时旧版本继续服务。
func TestDaemonUpgrade(t *testing.T) {
	h := startHelper(t, true)
	load := startLoad(h.addr)

	// v1 → v2：新进程就绪后写入 PID 文件，旧进程排空后以 0 退出。
	h.setVersion("v2")
	h.cmd.Process.Signal(syscall.SIGHUP)
	h.notify.expectPrefix(t, "RELOADING=1\nMONOTONIC_USEC=")
	h.notify.expect(t, "READY=1\nSTATUS=")
	pid2 := h.pid(t, h.cmd.Process.Pid)

	// v2 → v3：v1 还在排空，systemd 仍把它当作主进程，SIGHUP 发给 v1；
	// v1 转发给 v2，v2 等 v1 退出后再升级。
	h.setVersion("v3")
	h.cmd.Process.Signal(syscall.SIGHUP)
	h.notify.expectPrefix(t, "RELOADING=1\nMONOTONIC_USEC=")
	if err := h.cmd.Wait(); err != nil {
		t.Fatalf("v1 exited with %v", err)
	}
	h.notify.expect(t, "READY=1\nSTATUS=")
	pid3 := h.pid(t, pid2)

	// 新版本在装配阶段失败：v3 继续服务，状态中记录失败原因。
	waitGone(t, pid2)
	h.setVersion("fail")
	syscall.Kill(pid3, syscall.SIGHUP)
	h.notify.expectPrefix(t, "RELOADING=1\nMONOTONIC_USEC=")
	h.notify.expectPrefix(t, "READY=1\nSTATUS=upgrade failed: ")

	time.Sleep(300 * time.Millisecond)
	ok, versions, failed := load.stop()
	if failed != nil {
		t.Fatalf("%d requests ok, then failed: %v", ok, failed)
	}
	if versions["v1"] == 0 || versions["v2"] == 0 || versions["v3"] == 0 || len(versions) != 3 {
		t.Fatalf("versions served = %v, want v1, v2 and v3", versions)
	}
	h.stop(t, pid3)
}

// TestDaemonWithoutRuntimeDirectory：只报告就绪，reload 时在状态中说明需要 RuntimeDirectory=。
func TestDaemonWithoutRuntimeDirectory(t *testing.T) {
	h := startHelper(t, false)
	h.cmd.Process.Signal(syscall.SIGHUP)
	h.notify.expectPrefix(t, "RELOADING=1\nMONOTONIC_USEC=")
	h.notify.expect(t, "READY=1\nSTATUS=upgrade failed: upgrades need RuntimeDirectory= in the unit")
	if body, err := get(http.DefaultClient, "http://"+h.addr+"/"); err != nil || body != "v1" {
		t.Fatalf("GET = %q, %v; want v1 still serving", body, err)
	}
	h.cmd.Process.Signal(syscall.SIGTERM)
	if err := h.cmd.Wait(); err != nil {
		t.Fatalf("exited with %v", err)
	}
}

type helper struct {
	cmd        *exec.Cmd
	notify     *notifySocket
	addr       string
	dir        string
	setVersion func(string)
}

// startHelper 以子进程启动 daemonHelper（版本 v1），等它报告就绪。
func startHelper(t *testing.T, runtimeDir bool) *helper {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("upgrades need fd passing")
	}
	if testing.Short() {
		t.Skip("starts subprocesses")
	}
	dir, err := os.MkdirTemp("", "daemon") // unix socket 路径有长度限制，不用 t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	h := &helper{notify: listenNotify(t, filepath.Join(dir, "notify")), addr: freeAddr(t), dir: dir}
	versionFile := filepath.Join(dir, "version")
	h.setVersion = func(v string) {
		if err := os.WriteFile(versionFile, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	logs, err := os.Create(filepath.Join(dir, "log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			b, _ := os.ReadFile(logs.Name())
			t.Logf("daemon output:\n%s", b)
		}
	})

	h.setVersion("v1")
	h.cmd = exec.Command(os.Args[0], "-test.run=^$")
	h.cmd.Env = append(os.Environ(), daemonHelperEnv+"=1", "NOTIFY_SOCKET="+h.notify.path,
		"VERSION_FILE="+versionFile, "ADDR="+h.addr)
	if runtimeDir {
		h.cmd.Env = append(h.cmd.Env, "RUNTIME_DIRECTORY="+dir)
	}
	h.cmd.Stdout, h.cmd.Stderr = logs, logs
	if err := h.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.cmd.Process.Kill()
		if pid, err := h.readPID(); err == nil {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	h.notify.expect(t, "READY=1\nSTATUS=")
	if runtimeDir {
		if pid := h.pid(t, 0); pid != h.cmd.Process.Pid {
			t.Fatalf("PID file = %d, want %d", pid, h.cmd.Process.Pid)
		}
	}
	return h
}

func (h *helper) readPID() (int, error) {
	b, err := os.ReadFile(filepath.Join(h.dir, command.DaemonPIDFile))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// pid 等待 PID 文件写入一个不同于 old 的进程号：READY=1 先于 PID 文件发出。
func (h *helper) pid(t *testing.T, old int) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		pid, err := h.readPID()
		if err == nil && pid != old {
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("PID file = %d, %v; want a process other than %d", pid, err, old)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *helper) stop(t *testing.T, pid int) {
	t.Helper()
	syscall.Kill(pid, syscall.SIGTERM)
	waitGone(t, pid)
}

// waitGone 等待一个不是本进程子进程的进程退出。
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("process %d did not exit", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type notifySocket struct {
	path string
	msgs chan string
}

func listenNotify(t *testing.T, path string) *notifySocket {
	t.Helper()
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	n := &notifySocket{path: path, msgs: make(chan string, 16)}
	go func() {
		buf := make([]byte, 4096)
		for {
			k, err := conn.Read(buf)
			if err != nil {
				return
			}
			n.msgs <- string(buf[:k])
		}
	}()
	return n
}

func (n *notifySocket) next(t *testing.T) string {
	t.Helper()
	select {
	case msg := <-n.msgs:
		return msg
	case <-time.After(10 * time.Second):
		t.Fatal("no sd_notify message")
		return ""
	}
}

func (n *notifySocket) expect(t *testing.T, want string) {
	t.Helper()
	if got := n.next(t); got != want {
		t.Fatalf("sd_notify = %q, want %q", got, want)
	}
}

func (n *notifySocket) expectPrefix(t *testing.T, prefix string) {
	t.Helper()
	if got := n.next(t); !strings.HasPrefix(got, prefix) {
		t.Fatalf("sd_notify = %q, want prefix %q", got, prefix)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

type load struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	ok     int
	failed error
	served map[string]int
}

// startLoad 用 8 个客户端持续请求 addr，一半复用连接。
func startLoad(addr string) *load {
	ctx, cancel := context.WithCancel(context.Background())
	l := &load{cancel: cancel, served: map[string]int{}}
	for i := range 8 {
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			client := &http.Client{Transport: &http.Transport{DisableKeepAlives: i%2 == 0}}
			for ctx.Err() == nil {
				body, err := get(client, "http://"+addr+"/")
				l.mu.Lock()
				if err != nil && l.failed == nil {
					l.failed = fmt.Errorf("keep-alive %v: %w", i%2 != 0, err)
				} else if err == nil {
					l.ok++
					l.served[body]++
				}
				l.mu.Unlock()
			}
		}()
	}
	return l
}

func (l *load) stop() (int, map[string]int, error) {
	l.cancel()
	l.wg.Wait()
	return l.ok, l.served, l.failed
}

func get(client *http.Client, url string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	if _, err := io.Copy(&b, resp.Body); err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	return b.String(), nil
}
