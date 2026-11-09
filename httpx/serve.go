package httpx

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"

	"github.com/cockroachdb/errors"
)

// ServeOptions 是 Serve 的运行时依赖。
type ServeOptions struct {
	// Logger 记录排空开始和服务器停止；nil 表示关闭这些日志。
	Logger *slog.Logger
	// OnDraining 在开始排空前调用（例如撤销 ready）；可空。
	OnDraining func()
}

// ListenAndServe 监听 cfg.Addr，并按 cfg 运行 handler，直到 ctx 取消。
func ListenAndServe(ctx context.Context, cfg Config, handler http.Handler, opts ServeOptions) error {
	if ctx == nil {
		return errors.New("httpx: nil context")
	}
	cfg = cfg.withDefaults()
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return errors.Wrap(err, "httpx: listen")
	}
	return Serve(ctx, cfg, ln, handler, opts)
}

// Serve 在 ln 上按 cfg 运行 handler（忽略 cfg.Addr），直到 ctx 取消或监听异常。
//
// 停止时：先调用 OnDraining，再在 cfg.ShutdownTimeout 内排空请求；
// 超时则取消请求 context 并强制关闭。调用方在 Serve 返回后关闭数据库等请求依赖。
func Serve(ctx context.Context, cfg Config, ln net.Listener, handler http.Handler, opts ServeOptions) error {
	if ctx == nil {
		return errors.New("httpx: nil context")
	}
	if ln == nil {
		return errors.New("httpx: nil listener")
	}
	if handler == nil {
		return errors.New("httpx: nil handler")
	}
	cfg = cfg.withDefaults()
	srv := cfg.server(handler)

	// 所有请求 context 派生自 requestRoot，排空超时或结束后统一取消。
	requestRoot, cancelRequests := context.WithCancel(context.Background())
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
	if opts.Logger != nil {
		opts.Logger.Info("http server draining")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	shutdownErr := srv.Shutdown(shutdownCtx)
	cancel()
	if shutdownErr != nil {
		cancelRequests()
		shutdownErr = errors.Join(shutdownErr, srv.Close())
	}
	if !returned {
		serveErr = <-errc
	}
	cancelRequests()
	// 本次停机流程产生的 ErrServerClosed 表示正常退出。
	if !returned && errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	if opts.Logger != nil {
		opts.Logger.Info("http server stopped")
	}
	return errors.Join(errors.Wrap(serveErr, "httpx: serve"), errors.Wrap(shutdownErr, "httpx: shutdown"))
}

// Draining 是可在就绪检查中观察的排空标志。
type Draining struct{ v atomic.Bool }

// Set 标记开始排空。
func (d *Draining) Set() { d.v.Store(true) }

// Load 报告是否正在排空。
func (d *Draining) Load() bool { return d.v.Load() }
