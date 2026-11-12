package broadcast

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/logging"
)

// Database 是使用 broadcasts 表的 Backend，Migrations 提供表结构。
//
// Publish 插入一行，并立即投递给本进程中的 Hub；每个实例每 100 毫秒按 id 读取新行，投递其他进程发布的消息，
// 所以它们最多延迟一个轮询间隔。同一个 Database 发布的消息按发布顺序投递；不同进程同时发布的消息没有确定的先后，
// 本进程的消息可能先于其他进程更早提交的消息到达。
//
// 自增 id 的提交顺序可能与分配顺序不同：读到不连续的 id 时，等待缺失的行最多 2 秒，
// 期间到达就按顺序投递，超时则视为回滚留下的空号跳过。数据库暂时不可用时从上次读到的位置继续，
// 不会丢消息，所以 Database 不会让订阅因 ErrLost 结束。
//
// 表中的行由应用定时调用 Cleanup 删除。
type Database struct {
	db     bun.IDB
	logger *slog.Logger

	interval   time.Duration
	gapTimeout time.Duration
	retention  time.Duration

	// mu 保护 listeners：Publish 的本地投递和轮询投递在它下面去重，每条消息恰好投递一次。
	mu        sync.Mutex
	listeners map[*listener]struct{}
}

// listener 是一次 Listen 的状态。
type listener struct {
	deliver func(Message)
	cursor  int64              // 轮询已经越过的 id
	local   map[int64]struct{} // 已经由 Publish 投递、轮询还没越过的 id
}

type row struct {
	bun.BaseModel `bun:"table:broadcasts,alias:b"`

	ID        int64     `bun:"id,pk,autoincrement"`
	Topic     string    `bun:"topic,notnull"`
	Data      []byte    `bun:"data,notnull"`
	CreatedAt time.Time `bun:"created_at,notnull"`
}

// batchSize 是每次轮询最多读取的行数；读满时立即再读，不等下一个间隔。
const batchSize = 1000

// NewDatabase 创建数据库 Backend。db 必须已执行 broadcasts 迁移。logger 记录轮询失败与恢复，
// nil 时使用 slog.Default()。db 为 nil 时 panic。
func NewDatabase(db bun.IDB, logger *slog.Logger) *Database {
	if db == nil {
		panic("broadcast: nil database")
	}
	return &Database{
		db:         db,
		logger:     logging.Or(logger).With(logging.AttrComponent, "broadcast"),
		interval:   100 * time.Millisecond,
		gapTimeout: 2 * time.Second,
		retention:  10 * time.Minute,
		listeners:  map[*listener]struct{}{},
	}
}

// Publish 实现 Backend：插入一行，然后投递给本进程中正在 Listen 的 Hub。
func (d *Database) Publish(ctx context.Context, msg Message) error {
	data := msg.Data
	if data == nil {
		data = []byte{}
	}
	r := &row{Topic: msg.Topic, Data: data, CreatedAt: time.Now().UTC()}
	if _, err := d.db.NewInsert().Model(r).Exec(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for l := range d.listeners {
		if r.ID > l.cursor { // 轮询还没读到这一行
			l.local[r.ID] = struct{}{}
			l.deliver(msg)
		}
	}
	return nil
}

// Listen 实现 Backend：从开始监听时的最新一行之后读起。
func (d *Database) Listen(ctx context.Context, deliver func(Message), _ func(error)) {
	var cursor int64
	for {
		err := d.db.NewSelect().Model((*row)(nil)).ColumnExpr("COALESCE(MAX(id), 0)").Scan(ctx, &cursor)
		if err == nil {
			break
		}
		if !d.failed(ctx, err) {
			return
		}
	}
	l := &listener{deliver: deliver, cursor: cursor, local: map[int64]struct{}{}}
	d.mu.Lock()
	d.listeners[l] = struct{}{}
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.listeners, l)
		d.mu.Unlock()
	}()
	var (
		failing  bool
		gapSince time.Time
	)
	for {
		var rows []row
		err := d.db.NewSelect().Model(&rows).Where("id > ?", cursor).OrderExpr("id").Limit(batchSize).Scan(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !failing {
				d.logger.Error("broadcast poll failed; retrying", "error", err)
				failing = true
			}
		} else if failing {
			d.logger.Info("broadcast poll recovered")
			failing = false
		}
		d.mu.Lock()
		for _, r := range rows {
			if r.ID != cursor+1 {
				if gapSince.IsZero() {
					gapSince = time.Now()
				}
				if time.Since(gapSince) < d.gapTimeout {
					break // 等缺失的行提交
				}
			}
			gapSince = time.Time{}
			if _, ok := l.local[r.ID]; ok {
				delete(l.local, r.ID)
			} else {
				deliver(Message{Topic: r.Topic, Data: r.Data})
			}
			cursor = r.ID
			l.cursor = cursor
		}
		d.mu.Unlock()
		if len(rows) == batchSize && gapSince.IsZero() {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.interval):
		}
	}
}

// failed 记录 Listen 开始时的错误并等待重试，ctx 结束时返回 false。
func (d *Database) failed(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	d.logger.Error("broadcast listen failed; retrying", "error", err)
	select {
	case <-ctx.Done():
		return false
	case <-time.After(time.Second):
		return true
	}
}

// Cleanup 删除 10 分钟以前的行，每次最多 limit 行，返回删除的行数。可以交给 schedule.Cleanup 定期执行。
func (d *Database) Cleanup(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, errors.New("broadcast: cleanup limit must be positive")
	}
	old := d.db.NewSelect().Model((*row)(nil)).Column("id").
		Where("created_at < ?", time.Now().UTC().Add(-d.retention)).OrderExpr("id").Limit(int64(limit))
	res, err := d.db.NewDelete().Model((*row)(nil)).
		Where("id IN (?)", old).Exec(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "broadcast: cleanup")
	}
	n, err := res.RowsAffected()
	return int(n), errors.Wrap(err, "broadcast: cleanup")
}
