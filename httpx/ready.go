package httpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/go-tenon/tenon/logging"
)

// DefaultCheckTimeout 是 ReadyOptions.Timeout 不大于 0 时每项检查的超时。
const DefaultCheckTimeout = time.Second

// Check 是就绪检查中的一项：一个依赖，以及确认它可用的方法。
type Check struct {
	// Name 出现在结果和日志中，例如 database、redis。
	Name string
	// Ping 确认依赖可用，返回错误表示不可用，例如 db.PingContext。超过 ReadyOptions.Timeout 视为失败。
	Ping func(ctx context.Context) error
	// Optional 表示这个依赖坏了服务仍然可以降级运行，例如缓存：失败时在结果中报告，不让就绪检查失败。
	// 否则一个缓存节点的短暂故障就会让全部实例同时被摘除。
	Optional bool
}

// ReadyOptions 是 Ready 的选项。
type ReadyOptions struct {
	// Draining 是 Serve 开始排空时设置的标志，与 ServeOptions.OnDraining 用同一个；为 nil 时不检查。
	Draining *Draining
	// Checks 是要检查的依赖。只放处理请求必需的依赖，以及标为 Optional 的降级依赖。
	Checks []Check
	// Timeout 是每项检查的超时，不大于 0 时为 DefaultCheckTimeout。
	Timeout time.Duration
	// Logger 记录检查结果的变化，nil 表示 slog.Default()。
	Logger *slog.Logger
}

// readyResult 是就绪检查的响应体。
type readyResult struct {
	Status string            `json:"status"`           // ready、draining 或 unavailable
	Checks map[string]string `json:"checks,omitempty"` // 每项的结果：ok 或 failed
}

// Ready 返回就绪检查的处理器，通常注册为 /readyz，部署平台和负载均衡器据此决定是否向这个实例转发请求。
//
// 正在排空时直接返回 503；否则并行执行全部检查，每项不超过 Timeout，必需的检查都通过时返回 200，
// 有一项失败时返回 503。响应体列出每项的结果，例如 {"status":"ready","checks":{"database":"ok","cache":"failed"}}；
// 失败的原因不出现在响应中，而是记录在日志里。
//
// 日志只记录变化：一项检查从通过变为失败时记录 Warn 和原因，恢复时记录 Info，平台每隔几秒的探测不会让日志刷屏。
// 不理会 ctx 的 Ping 也不会拖住响应，超时后结果按失败处理。名称为空、重复或 Ping 为 nil 时 panic。
func Ready(opts ReadyOptions) http.Handler {
	seen := map[string]bool{}
	for _, c := range opts.Checks {
		switch {
		case c.Name == "":
			panic("httpx: check name is required")
		case c.Ping == nil:
			panic("httpx: check " + c.Name + " has no Ping")
		case seen[c.Name]:
			panic("httpx: duplicate check " + c.Name)
		}
		seen[c.Name] = true
	}
	r := &ready{
		draining: opts.Draining,
		checks:   append([]Check(nil), opts.Checks...),
		timeout:  opts.Timeout,
		logger:   logging.Or(opts.Logger).With(logging.AttrComponent, "http"),
		failing:  map[string]bool{},
	}
	if r.timeout <= 0 {
		r.timeout = DefaultCheckTimeout
	}
	return r
}

type ready struct {
	draining *Draining
	checks   []Check
	timeout  time.Duration
	logger   *slog.Logger

	mu      sync.Mutex
	failing map[string]bool // 上一次失败的检查，用于只记录变化
}

func (r *ready) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	res := readyResult{Status: "ready"}
	if r.draining != nil && r.draining.Load() {
		res.Status = "draining"
	} else if len(r.checks) > 0 {
		res.Checks = make(map[string]string, len(r.checks))
		errs := r.run(req.Context())
		for i, c := range r.checks {
			res.Checks[c.Name] = "ok"
			if errs[i] != nil {
				res.Checks[c.Name] = "failed"
				if !c.Optional {
					res.Status = "unavailable"
				}
			}
		}
		r.logChanges(req.Context(), errs)
	}
	status := http.StatusOK
	if res.Status != "ready" {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if req.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(res)
	}
}

// run 并行执行全部检查，返回每项的错误。超时的检查不等它返回，按失败处理。
func (r *ready) run(ctx context.Context) []error {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	errs := make([]error, len(r.checks))
	var wg sync.WaitGroup
	for i, c := range r.checks {
		wg.Go(func() {
			done := make(chan error, 1)
			go func() { done <- ping(ctx, c.Ping) }()
			select {
			case err := <-done:
				errs[i] = err
			case <-ctx.Done():
				errs[i] = errors.Wrapf(ctx.Err(), "no answer within %s", r.timeout)
			}
		})
	}
	wg.Wait()
	return errs
}

// ping 调用 fn，把 panic 转换为错误。
func ping(ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = errors.Newf("panic: %v", v)
		}
	}()
	return fn(ctx)
}

// logChanges 记录与上一次相比结果变化了的检查。
func (r *ready) logChanges(ctx context.Context, errs []error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, c := range r.checks {
		failed := errs[i] != nil
		if failed == r.failing[c.Name] {
			continue
		}
		r.failing[c.Name] = failed
		if failed {
			r.logger.WarnContext(ctx, "readiness check failed", "check", c.Name, "optional", c.Optional, "error", errs[i])
		} else {
			r.logger.InfoContext(ctx, "readiness check recovered", "check", c.Name)
		}
	}
}
