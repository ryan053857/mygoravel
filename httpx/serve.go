package httpx

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/errors"
)

// ServeOptions 是 Serve 的运行时依赖。
type ServeOptions struct {
	// Logger 记录排空开始和服务器停止；nil 表示 slog.Default()。
	Logger *slog.Logger
	// OnDraining 在开始排空前调用（例如撤销 ready）；可空。
	OnDraining func()
}

// ListenAndServe 监听 cfg.Addr，并按 cfg 运行 handler，直到 ctx 取消。
func ListenAndServe(ctx context.Context, cfg Config, handler http.Handler, opts ServeOptions) error {
	if ctx == nil {
		return errors.New("httpx: nil context")
	}
	if err := cfg.Validate(); err != nil {
		return errors.Wrap(err, "httpx: config")
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return errors.Wrap(err, "httpx: listen")
	}
	return Serve(ctx, cfg, ln, handler, opts)
}

// Serve 在 ln 上按 cfg 运行 handler（不使用 cfg.Addr 监听，但 cfg 仍需合法），直到 ctx 取消或监听异常。
//
// Serve 返回时 ln 已关闭，包括因参数错误提前返回的情况。
//
// 停止时：先调用 OnDraining，再关闭 DrainStarted 返回的通道，然后在 cfg.ShutdownTimeout 内排空请求；
// 超时则取消请求 context 并强制关闭。调用方在 Serve 返回后关闭数据库等请求依赖。
func Serve(ctx context.Context, cfg Config, ln net.Listener, handler http.Handler, opts ServeOptions) error {
	if ln == nil {
		return errors.New("httpx: nil listener")
	}
	if ctx == nil {
		_ = ln.Close()
		return errors.New("httpx: nil context")
	}
	if handler == nil {
		_ = ln.Close()
		return errors.New("httpx: nil handler")
	}
	if err := cfg.Validate(); err != nil {
		_ = ln.Close()
		return errors.Wrap(err, "httpx: config")
	}
	logger := componentLogger(opts.Logger)
	srv := cfg.server(handler)
	var fresh freshConns
	srv.ConnState = fresh.track

	// 所有请求 context 派生自 requestRoot，排空超时或结束后统一取消；
	// draining 在排空开始时关闭，供 SSE 等长连接主动结束，见 DrainStarted。
	draining := make(chan struct{})
	requestRoot, cancelRequests := context.WithCancel(context.WithValue(context.Background(), drainKey{}, draining))
	defer cancelRequests()
	srv.BaseContext = func(net.Listener) context.Context { return requestRoot }
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	var serveErr error
	returned := false
	select {
	case serveErr = <-errc:
		returned = true
	case <-ctx.Done():
	}
	if opts.OnDraining != nil {
		opts.OnDraining()
	}
	close(draining)
	logger.Info("http server draining")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	// Shutdown 开始之后才读到的请求，net/http 不处理就关闭连接。所以先停止接收新连接，
	// 等已经接收的连接读到第一个请求，再开始 Shutdown。升级时监听 socket 交给了新进程，
	// 旧进程直到这一刻都在接收连接。
	if !returned {
		_ = ln.Close()
		serveErr = <-errc
		if errors.Is(serveErr, net.ErrClosed) {
			serveErr = nil
		}
		fresh.wait(shutdownCtx, cfg.ReadHeaderTimeout)
	}
	shutdownErr := srv.Shutdown(shutdownCtx)
	cancel()
	if shutdownErr != nil {
		cancelRequests()
		shutdownErr = errors.Join(shutdownErr, srv.Close())
	}
	cancelRequests()
	logger.Info("http server stopped")
	return errors.Join(errors.Wrap(serveErr, "httpx: serve"), errors.Wrap(shutdownErr, "httpx: shutdown"))
}

type drainKey struct{}

// DrainStarted 返回一个在服务器开始排空时关闭的通道。
// 长连接（例如 SSE）应当监听它并主动结束，否则 Serve 要等满 ShutdownTimeout 才能退出；
// 浏览器会自动重连到其他实例。请求不是由 Serve 处理时返回 nil，从它接收会一直阻塞。
func DrainStarted(ctx context.Context) <-chan struct{} {
	ch, _ := ctx.Value(drainKey{}).(chan struct{})
	return ch
}

// Draining 是可在就绪检查中观察的排空标志。
type Draining struct{ v atomic.Bool }

// Set 标记开始排空。
func (d *Draining) Set() { d.v.Store(true) }

// Load 报告是否正在排空。
func (d *Draining) Load() bool { return d.v.Load() }

// freshConns 记录已经接收、还没读到第一个请求的连接。
type freshConns struct {
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func (f *freshConns) track(c net.Conn, state http.ConnState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if state != http.StateNew {
		delete(f.conns, c)
		return
	}
	if f.conns == nil {
		f.conns = make(map[net.Conn]struct{})
	}
	f.conns[c] = struct{}{}
}

func (f *freshConns) len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.conns)
}

// wait 等待全部连接读到第一个请求，最多等 limit：超过请求头读取时限的连接会被服务器关闭。
func (f *freshConns) wait(ctx context.Context, limit time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for f.len() > 0 {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
