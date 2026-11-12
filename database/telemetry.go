package database

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/uptrace/bun"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// scopeName 是数据库指标和链路的 instrumentation scope。
const scopeName = "github.com/ryan053857/mygoravel/database"

// durationBuckets 是 OpenTelemetry 数据库语义约定建议的耗时分桶（秒）。
var durationBuckets = []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10}

// 属性名遵循 OpenTelemetry 的数据库语义约定。
const (
	attrSystem     = attribute.Key("db.system.name")
	attrNamespace  = attribute.Key("db.namespace")
	attrOperation  = attribute.Key("db.operation.name")
	attrCollection = attribute.Key("db.collection.name")
	attrStatusCode = attribute.Key("db.response.status_code")
	attrQueryText  = attribute.Key("db.query.text")
	attrErrorType  = attribute.Key("error.type")
	attrAddress    = attribute.Key("server.address")
	attrPort       = attribute.Key("server.port")
)

// Option 调整 Open 打开的连接池。
type Option func(*options)

type options struct {
	meters  metric.MeterProvider
	tracers trace.TracerProvider
	logger  *slog.Logger
}

// WithTelemetry 让连接池报告每条语句的 OpenTelemetry 指标和链路。
//
// 指标 db.client.operation.duration（直方图，秒）的属性遵循 OpenTelemetry 的数据库语义约定：
// db.system.name（postgresql）、db.namespace（数据库名）、db.operation.name（SELECT、INSERT、BEGIN 等）、
// db.collection.name（Bun 查询的表，原始 SQL 没有）、server.address 和 server.port；
// 出错时还有 error.type 和 db.response.status_code（SQLSTATE，例如唯一约束冲突是 23505）。
// 没有查到行（sql.ErrNoRows）不算出错。
//
// ctx 中有 span 时，每条语句是它之下的一个 client span，名称是操作和表，例如 SELECT articles：
// 请求、队列任务中的查询出现在它们的链路中；后台轮询这类没有上级 span 的查询只计入指标。span 的 db.query.text 是把字面量换成 ? 之后的 SQL，
// 参数的值不会发往链路平台。
//
// mp 或 tp 为 nil 时 panic；只需要其中一种时，另一个传 noop 实现。
func WithTelemetry(mp metric.MeterProvider, tp trace.TracerProvider) Option {
	if mp == nil || tp == nil {
		panic("database: nil meter or tracer provider")
	}
	return func(o *options) { o.meters, o.tracers = mp, tp }
}

// WithLogger 设置记录慢查询的日志器，不设置时使用 slog.Default()。慢查询的阈值是 Config.SlowQuery。
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.logger = l }
}

// queryHook 是 Open 安装的 bun.QueryHook：记录指标和链路，并把超过阈值的语句记为慢查询。
type queryHook struct {
	tracer   trace.Tracer            // nil 表示不记录链路
	duration metric.Float64Histogram // nil 表示不记录指标
	base     []attribute.KeyValue    // 每条语句都有的属性：数据库、地址
	slow     time.Duration           // 0 表示不记录慢查询
	logger   *slog.Logger
}

type spanKey struct{}

// newQueryHook 按 o 和 cfg 创建钩子；既不记录指标链路、也不记录慢查询时返回 nil。
func newQueryHook(o options, cfg Config, database, host string, port uint16) *queryHook {
	if o.meters == nil && cfg.SlowQuery <= 0 {
		return nil
	}
	h := &queryHook{
		base: []attribute.KeyValue{
			attrSystem.String("postgresql"),
			attrNamespace.String(database),
			attrAddress.String(host),
			attrPort.Int(int(port)),
		},
		slow:   cfg.SlowQuery,
		logger: o.logger,
	}
	if h.logger == nil {
		h.logger = slog.Default()
	}
	h.logger = h.logger.With("component", "database")
	if o.meters != nil {
		duration, err := o.meters.Meter(scopeName).Float64Histogram("db.client.operation.duration",
			metric.WithUnit("s"),
			metric.WithDescription("Duration of database client operations."),
			metric.WithExplicitBucketBoundaries(durationBuckets...))
		if err != nil {
			panic(errors.Wrap(err, "database: create db.client.operation.duration"))
		}
		h.duration = duration
		h.tracer = o.tracers.Tracer(scopeName)
	}
	return h
}

var _ bun.QueryHook = (*queryHook)(nil)

func (h *queryHook) BeforeQuery(ctx context.Context, e *bun.QueryEvent) context.Context {
	// 只在 ctx 中已有 span 时记录：队列转发任务、调度器加锁这些后台查询不属于任何请求或任务，
	// 每条都成为一条单独的链路只会淹没真正的链路。它们的耗时仍然计入指标。
	if h.tracer == nil || !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}
	op, table := describe(e)
	name := op
	if table != "" {
		name += " " + table
	}
	spanCtx, span := h.tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindClient))
	if e.Stash == nil {
		e.Stash = map[any]any{}
	}
	e.Stash[spanKey{}] = span
	if op == "BEGIN" {
		// Bun 把这里返回的 ctx 保存为事务的 ctx，COMMIT 和 ROLLBACK 用它执行：
		// 返回原来的 ctx，它们才会和事务中的语句一样接在调用方之下，而不是接在已经结束的 BEGIN 之下。
		return ctx
	}
	return spanCtx
}

func (h *queryHook) AfterQuery(ctx context.Context, e *bun.QueryEvent) {
	elapsed := time.Since(e.StartTime)
	op, table := describe(e)
	attrs := append(h.base[:len(h.base):len(h.base)], attrOperation.String(op))
	if table != "" {
		attrs = append(attrs, attrCollection.String(table))
	}
	errType, code := classify(e.Err)
	if errType != "" {
		attrs = append(attrs, attrErrorType.String(errType))
	}
	if code != "" {
		attrs = append(attrs, attrStatusCode.String(code))
	}
	if h.duration != nil {
		h.duration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(attrs...))
	}

	var text string
	if span, ok := e.Stash[spanKey{}].(trace.Span); ok {
		text = sanitize(e.Query)
		span.SetAttributes(attrs...)
		span.SetAttributes(attrQueryText.String(text))
		if errType != "" {
			span.RecordError(e.Err)
			span.SetStatus(codes.Error, e.Err.Error())
		}
		span.End()
	}
	if h.slow > 0 && elapsed >= h.slow {
		if text == "" {
			text = sanitize(e.Query)
		}
		h.logger.WarnContext(ctx, "slow query",
			"duration", elapsed, string(attrOperation), op, string(attrCollection), table, "query", text)
	}
}

// describe 返回语句的操作（SELECT、INSERT、BEGIN 等，大写）和 Bun 查询的表名；原始 SQL 没有表名。
func describe(e *bun.QueryEvent) (op, table string) {
	op = strings.ToUpper(e.Operation())
	if e.IQuery != nil {
		table = tableName(e.IQuery.GetTableName())
	}
	return op, table
}

// tableName 从 Bun 给出的表达式中取出表名：去掉别名和标识符的双引号，例如 "articles" AS "a" 得到 articles。
func tableName(expr string) string {
	expr, _, _ = strings.Cut(strings.TrimSpace(expr), " ")
	return strings.ReplaceAll(expr, `"`, "")
}

// classify 返回 err 的 error.type 和 SQLSTATE。PostgreSQL 返回的错误两者都是 SQLSTATE；
// 超时和取消分别是 timeout、canceled；其他错误是 _OTHER。没有错误或没有查到行时都为空。
func classify(err error) (errType, code string) {
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		return "", ""
	}
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr):
		return pgErr.Code, pgErr.Code
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", ""
	case errors.Is(err, context.Canceled):
		return "canceled", ""
	}
	return "_OTHER", ""
}
