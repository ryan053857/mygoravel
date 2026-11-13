package command

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cloudflare/tableflip"
	"github.com/cockroachdb/errors"
	"github.com/coreos/go-systemd/v22/daemon"

	"github.com/go-tenon/tenon/logging"
)

// DaemonPIDFile 是 Daemon 在 systemd 的 RuntimeDirectory 中写入的 PID 文件名，
// unit 文件的 PIDFile= 指向它，systemd 据此在升级后接管新进程。
const DaemonPIDFile = "daemon.pid"

// Daemon 是常驻进程：取得监听端口，报告就绪，运行各部分直到停止，并支持不停机升级。
//
// 在 systemd 的 notify 类服务（Type=notify-reload）中运行时，Run 向 systemd 报告就绪。
// 同时设置了 RuntimeDirectory= 时还支持升级：收到 SIGHUP 会用磁盘上的二进制启动新进程，
// 把 Listen 取得的监听 socket 交给它，等它的 Run 报告就绪后，本进程的 Run 像收到停止信号
// 一样平滑停止。监听 socket 一直打开，连接不会被拒绝。新进程在就绪之前退出或超过一分钟，
// 本进程继续运行，systemd 状态中的 Status 行以 "upgrade failed" 开头。
//
// 新进程把 PID 写入 RuntimeDirectory 中的 DaemonPIDFile，旧进程退出后 systemd 从这个文件
// 接管新进程。在此之前 systemd 仍把 SIGHUP 发给旧进程，旧进程转发给新进程，新进程等旧进程
// 退出后再升级。
//
// 不在 notify 类服务中时（没有 NOTIFY_SOCKET），Listen 是普通监听，Run 等同于 Concurrently，
// 不处理 SIGHUP。
//
// 一个进程只创建一个 Daemon。所有 Listen 都要在 Run 之前调用，进程退出前调用 Stop。
type Daemon struct {
	logger  *slog.Logger
	notify  bool                // 在 systemd 的 notify 类服务中
	upg     *tableflip.Upgrader // nil 表示不支持升级
	pidFile string
	hup     chan os.Signal
	stop    chan struct{}
	stopped sync.Once
}

// NewDaemon 创建 Daemon。logger 记录升级过程；为 nil 时在记录时使用 slog.Default()。
func NewDaemon(logger *slog.Logger) (*Daemon, error) {
	d := &Daemon{logger: logger, notify: os.Getenv("NOTIFY_SOCKET") != "", stop: make(chan struct{})}
	if !d.notify {
		return d, nil
	}
	d.hup = make(chan os.Signal, 1)
	signal.Notify(d.hup, syscall.SIGHUP)

	runtimeDir, _, _ := strings.Cut(os.Getenv("RUNTIME_DIRECTORY"), ":")
	if runtimeDir == "" {
		return d, nil
	}
	d.pidFile = filepath.Join(runtimeDir, DaemonPIDFile)
	upg, err := tableflip.New(tableflip.Options{PIDFile: d.pidFile})
	if errors.Is(err, tableflip.ErrNotSupported) {
		return d, nil
	}
	if err != nil {
		signal.Stop(d.hup)
		return nil, errors.Wrap(err, "command: daemon")
	}
	d.upg = upg
	return d, nil
}

// Listen 监听 addr。由升级启动的进程直接取得旧进程交来的同一个 socket。
func (d *Daemon) Listen(network, addr string) (net.Listener, error) {
	var (
		ln  net.Listener
		err error
	)
	if d.upg == nil {
		var lc net.ListenConfig
		ln, err = lc.Listen(context.Background(), network, addr)
	} else {
		ln, err = d.upg.Listen(network, addr)
	}
	return ln, errors.Wrapf(err, "command: listen %s %s", network, addr)
}

// Run 报告就绪，然后用 Concurrently 运行 runs，直到 ctx 取消或者交接给了升级启动的新进程。
// 交接之后 runs 收到的 ctx 被取消，各部分像收到停止信号一样平滑停止，Run 返回 nil。
func (d *Daemon) Run(ctx context.Context, runs ...func(context.Context) error) error {
	if !d.notify {
		return Concurrently(ctx, runs...)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 升级时 systemd 收到新进程的 READY=1 就结束 reload；STATUS 清除上一次升级失败的记录。
	d.sdNotify("READY=1\nSTATUS=")
	if d.upg != nil {
		// 写入 PID 文件，并让旧进程开始平滑停止。
		if err := d.upg.Ready(); err != nil {
			return errors.Wrap(err, "command: daemon ready")
		}
		go func() {
			select {
			case <-d.upg.Exit():
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	go d.reloads()
	return Concurrently(ctx, runs...)
}

// reloads 处理 SIGHUP，直到 Stop：交接之前升级本进程，交接之后转发给新进程。
func (d *Daemon) reloads() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-d.stop
		cancel()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.hup:
		}
		if d.handedOver() {
			if err := d.forward(); err != nil {
				d.sdNotify("READY=1\nSTATUS=upgrade failed: " + err.Error())
			}
			continue
		}
		d.sdNotify(daemon.SdNotifyReloading + "\n" + daemon.SdNotifyMonotonicUsec())
		if err := d.upgrade(ctx); err != nil {
			d.log().Error("upgrade failed, still running", "error", err)
			d.sdNotify("READY=1\nSTATUS=upgrade failed: " + err.Error())
			continue
		}
		d.log().Info("upgraded, handing over to the new process")
	}
}

func (d *Daemon) handedOver() bool {
	if d.upg == nil {
		return false
	}
	select {
	case <-d.upg.Exit():
		return true
	default:
		return false
	}
}

// forward 把 SIGHUP 转发给接手的新进程：本进程排空期间 systemd 仍把它当作主进程。
func (d *Daemon) forward() error {
	b, err := os.ReadFile(d.pidFile)
	if err != nil {
		return errors.Wrap(err, "forward reload")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid == os.Getpid() {
		return errors.Newf("forward reload: PID file names %q", b)
	}
	return errors.Wrap(syscall.Kill(pid, syscall.SIGHUP), "forward reload")
}

func (d *Daemon) upgrade(ctx context.Context) error {
	if d.upg == nil {
		return errors.New("upgrades need RuntimeDirectory= in the unit")
	}
	// 上一次升级的旧进程还在排空时，等它退出：tableflip 同时只允许一个旧进程。
	if err := d.upg.WaitForParent(ctx); err != nil {
		return err
	}
	d.log().Info("upgrading")
	// WaitForParent 返回时 tableflip 内部可能还没有记下旧进程已经退出（它在另一个 goroutine 中、
	// 稍后才处理这个信号），此时 Upgrade 会拒绝。旧进程确实已经退出，稍等再试即可。
	deadline := time.Now().Add(parentExitGrace)
	for {
		err := d.upg.Upgrade()
		if err == nil || !strings.Contains(err.Error(), "parent hasn't exited") || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// parentExitGrace 是 WaitForParent 返回后，等 tableflip 记下旧进程已经退出的最长时间。
const parentExitGrace = 5 * time.Second

// Stop 停止处理 SIGHUP，释放升级用到的资源。
func (d *Daemon) Stop() {
	d.stopped.Do(func() { close(d.stop) })
	if d.hup != nil {
		signal.Stop(d.hup)
	}
	if d.upg != nil {
		d.upg.Stop()
	}
}

func (d *Daemon) sdNotify(state string) {
	if _, err := daemon.SdNotify(false, state); err != nil {
		d.log().Warn("sd_notify failed", "error", err)
	}
}

func (d *Daemon) log() *slog.Logger {
	return logging.Or(d.logger).With(logging.AttrComponent, "daemon")
}
