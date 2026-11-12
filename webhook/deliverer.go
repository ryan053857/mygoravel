package webhook

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/outbound"
	"github.com/ryan053857/mygoravel/queue"
)

// DefaultTimeout 是 Options.Timeout 不大于 0 时每次请求的超时。
const DefaultTimeout = 10 * time.Second

// DefaultMaxAttempts 是 Options.MaxAttempts 不大于 0 时每次投递最多的请求次数，按队列的退避，最后一次约在 2 天后。
const DefaultMaxAttempts = 16

// maxError 是投递记录中 last_error 保存的最大字节数。
const maxError = 1024

// Options 是 Deliverer 的选项，零值可用。
type Options struct {
	// Client 发送请求。nil 时使用 outbound.New(outbound.Options{AnyPort: true})：端点地址由租户填写，
	// 默认拒绝内网、回环等地址，端口不限。本地开发时传入用 outbound.Options.Allow 放行本机的客户端。
	// 投递不跟随重定向，3xx 响应视为失败，Client 的 CheckRedirect 被忽略。
	Client *http.Client
	// Timeout 是每次请求的超时，包括读取响应；不大于 0 时为 DefaultTimeout。
	Timeout time.Duration
	// MaxAttempts 是每次投递最多的请求次数，覆盖 Worker 的 Config.MaxAttempts；不大于 0 时为 DefaultMaxAttempts。
	MaxAttempts int
}

// Deliverer 处理投递任务：签名并发送请求，把结果写回投递记录。
type Deliverer struct {
	db          bun.IDB
	client      *http.Client
	timeout     time.Duration
	maxAttempts int
}

// NewDeliverer 创建投递任务的处理器，db 是投递记录和端点所在的数据库。
func NewDeliverer(db bun.IDB, opts Options) *Deliverer {
	if db == nil {
		panic("webhook: nil database")
	}
	client := opts.Client
	if client == nil {
		client = outbound.New(outbound.Options{AnyPort: true, Timeout: -1})
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	d := &Deliverer{db: db, client: &noRedirect, timeout: opts.Timeout, maxAttempts: opts.MaxAttempts}
	if d.timeout <= 0 {
		d.timeout = DefaultTimeout
	}
	if d.maxAttempts <= 0 {
		d.maxAttempts = DefaultMaxAttempts
	}
	return d
}

// Handle 把 Deliver 的处理函数注册到 w，最多请求 Options.MaxAttempts 次。w 必须使用 Send 发布任务的数据库队列。
//
// 端点返回 2xx 时投递成功；其他状态码、超时和网络错误按队列的退避重试；
// 端点地址被 outbound 拒绝（内网地址）时不再重试，投递记录直接标记为失败。
// 429 和 503 带有 Retry-After 时按它等待。最后一次仍然失败时投递记录标记为失败，任务正常结束，
// 不进入失败任务表：端点出错是接收方的问题，投递记录就是它的处理结果，需要时用 Resend 重发。
func (d *Deliverer) Handle(w *queue.Worker) {
	Deliver.Handle(w, d.deliver, queue.MaxAttempts(d.maxAttempts))
}

func (d *Deliverer) deliver(ctx context.Context, id uuid.UUID) error {
	del := new(Delivery)
	err := d.db.NewSelect().Model(del).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // 端点已删除，或者记录已被清理
	}
	if err != nil {
		return errors.Wrap(err, "webhook: load delivery")
	}
	if del.Status != StatusPending {
		return nil
	}
	ep := new(Endpoint)
	err = d.db.NewSelect().Model(ep).Where("id = ?", del.EndpointID).Scan(ctx)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return d.finish(ctx, del, StatusFailed, 0, "endpoint deleted", false)
	case err != nil:
		return errors.Wrap(err, "webhook: load endpoint")
	case ep.Disabled:
		return d.finish(ctx, del, StatusFailed, 0, "endpoint disabled", false)
	}

	code, sendErr := d.send(ctx, ep, del)
	status := StatusSucceeded
	if sendErr != nil {
		status = StatusPending
		if _, final := queue.AttemptFromContext(ctx); final || errors.Is(sendErr, outbound.ErrForbidden) {
			status = StatusFailed
		}
	}
	if err := d.finish(ctx, del, status, code, errorText(sendErr), true); err != nil {
		return err
	}
	if status == StatusPending {
		return sendErr
	}
	return nil
}

// finish 把一次尝试的结果写回投递记录。记录已经不是 pending 时（例如重复的任务已经处理过）不做修改。
func (d *Deliverer) finish(ctx context.Context, del *Delivery, status Status, code int, lastError string, attempted bool) error {
	upd := d.db.NewUpdate().Model((*Delivery)(nil)).
		Set("status = ?", status).Set("response_status = ?", code).Set("last_error = ?", lastError).
		Set("updated_at = ?", time.Now().UTC()).
		Where("id = ?", del.ID).Where("status = ?", StatusPending)
	if attempted {
		upd = upd.Set("attempts = attempts + 1")
	}
	_, err := upd.Exec(ctx)
	return errors.Wrap(err, "webhook: update delivery")
}

// send 签名并发送一次请求，返回响应的状态码；没有收到响应时为 0。
func (d *Deliverer) send(ctx context.Context, ep *Endpoint, del *Delivery) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, strings.NewReader(del.Payload))
	if err != nil {
		return 0, errors.Wrap(err, "build request")
	}
	now := time.Now()
	var sigs []string
	for _, secret := range ep.secrets(now) {
		sig, err := Sign(secret, del.EventID.String(), now, []byte(del.Payload))
		if err != nil {
			return 0, err
		}
		sigs = append(sigs, sig)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderID, del.EventID.String())
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
	req.Header.Set(HeaderSignature, strings.Join(sigs, " "))

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxError))
	err = errors.Newf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		if secs, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil && secs > 0 {
			err = queue.RetryAfter(err, time.Duration(secs)*time.Second)
		}
	}
	return resp.StatusCode, err
}

// errorText 返回保存在 last_error 中的错误信息，不超过 maxError 字节。
func errorText(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > maxError {
		s = s[:maxError]
	}
	return strings.ToValidUTF8(s, "")
}
