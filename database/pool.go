package database

import (
	"context"
	"database/sql/driver"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/ryan053857/mygoravel/acquire"
	"github.com/ryan053857/mygoravel/errorx"
)

// ErrBusy 表示连接池在 acquire.WithTimeout 设置的时限内没有空闲连接：数据库连接已经用满，请求过载。
// 它属于 errorx.Unavailable，HTTP 响应为 503，并带 Retry-After。
var ErrBusy = errorx.Define(errorx.Unavailable, "database.busy")

// busyRetryAfter 是 ErrBusy 建议客户端等待的时间。
const busyRetryAfter = time.Second

// connector 是交给 database/sql 的 driver.Connector：每次取连接时从 pgxpool 取一个，
// 按 ctx 中 acquire.WithTimeout 设置的时限限制等待，超过时返回 ErrBusy，并记录等待时长和超时次数。
// 时限只作用于取连接，不影响语句本身的执行。httpx 为每个请求设置时限；队列任务和命令不设置，排队等待连接。
type connector struct {
	driver.Connector
	pool     *pgxpool.Pool
	wait     metric.Float64Histogram // nil 表示不记录
	timeouts metric.Int64Counter
	attrs    metric.MeasurementOption
}

func newConnector(pool *pgxpool.Pool, o options) *connector {
	c := &connector{Connector: stdlib.GetPoolConnector(pool), pool: pool}
	if o.meters != nil {
		c.observe(o.meters, o.name)
	}
	return c
}

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	start := time.Now()
	acquireCtx := ctx
	d, limited := acquire.Timeout(ctx)
	if limited {
		var cancel context.CancelFunc
		acquireCtx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	conn, err := c.Connector.Connect(acquireCtx)
	if c.wait != nil {
		c.wait.Record(ctx, time.Since(start).Seconds(), c.attrs)
	}
	if err != nil && acquireCtx != ctx && ctx.Err() == nil && errors.Is(acquireCtx.Err(), context.DeadlineExceeded) {
		if c.timeouts != nil {
			c.timeouts.Add(ctx, 1, c.attrs)
		}
		return nil, ErrBusy.Wrap(errors.Wrapf(err, "database: no idle connection within %v", d), errorx.RetryAfter(busyRetryAfter))
	}
	return conn, err
}

// Close 在 *sql.DB 关闭时关闭连接池。
func (c *connector) Close() error {
	c.pool.Close()
	return nil
}

// waitBuckets 是取连接等待时长的直方图分桶（秒）：没有等待时在 1 毫秒以内，用满时接近 acquire 的时限。
var waitBuckets = []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// observe 创建连接池的指标，名称和属性遵循 OpenTelemetry 数据库语义约定的连接池部分。
func (c *connector) observe(mp metric.MeterProvider, name string) {
	meter := mp.Meter(scopeName)
	pool := attrPoolName.String(name)
	c.attrs = metric.WithAttributeSet(attribute.NewSet(pool))
	var err error
	if c.wait, err = meter.Float64Histogram("db.client.connection.wait_time", metric.WithUnit("s"),
		metric.WithDescription("Time it took to obtain a connection from the pool."),
		metric.WithExplicitBucketBoundaries(waitBuckets...)); err != nil {
		panic(errors.Wrap(err, "database: create db.client.connection.wait_time"))
	}
	if c.timeouts, err = meter.Int64Counter("db.client.connection.timeouts", metric.WithUnit("{timeout}"),
		metric.WithDescription("Connection requests that gave up after the acquire timeout.")); err != nil {
		panic(errors.Wrap(err, "database: create db.client.connection.timeouts"))
	}
	count, err := meter.Int64ObservableUpDownCounter("db.client.connection.count", metric.WithUnit("{connection}"),
		metric.WithDescription("Connections in the pool, by state: idle or used."))
	if err != nil {
		panic(errors.Wrap(err, "database: create db.client.connection.count"))
	}
	limit, err := meter.Int64ObservableUpDownCounter("db.client.connection.max", metric.WithUnit("{connection}"),
		metric.WithDescription("Maximum number of connections the pool allows."))
	if err != nil {
		panic(errors.Wrap(err, "database: create db.client.connection.max"))
	}
	idle := metric.WithAttributeSet(attribute.NewSet(pool, attrConnState.String("idle")))
	used := metric.WithAttributeSet(attribute.NewSet(pool, attrConnState.String("used")))
	if _, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s := c.pool.Stat()
		o.ObserveInt64(count, int64(s.IdleConns()), idle)
		o.ObserveInt64(count, int64(s.AcquiredConns()), used)
		o.ObserveInt64(limit, int64(s.MaxConns()), c.attrs)
		return nil
	}, count, limit); err != nil {
		panic(errors.Wrap(err, "database: register pool metrics"))
	}
}
