package search

import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/operation"
	"github.com/ryan053857/mygoravel/queue"
)

// validName 是索引名称的格式，它也是同步任务 topic 的一部分。
var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

// MaxTerms 是一个文档最多索引的词数，之后的文字不能被搜到。更长的文档应当切分为多个文档，见 LoadFunc。
// 它也是 PostgreSQL tsvector 的最大位置，并使 tsvector 不超过 1 MB 的上限。
const MaxTerms = 16383

// errInvalid 标记无法建索引的文档，例如过滤字段的值太长。重试也不会成功，同步任务直接转入失败任务表。
var errInvalid = errors.New("search: invalid document")

// Index 是一个搜索索引，D 是它的文档类型。用 Define 声明为包级变量，同步、重建和查询都通过它。
type Index[D any] struct {
	name    string
	schema  *schema
	tok     Tokenizer
	sync    queue.Topic[string]
	reindex queue.Topic[uuid.UUID]
}

// Option 是 Define 的选项。
type Option func(*options)

type options struct{ tok Tokenizer }

// WithTokenizer 设置分词器，默认是 Bigram。修改分词器之后要用 Reindex 重建索引。
func WithTokenizer(t Tokenizer) Option {
	return func(o *options) { o.tok = t }
}

// Define 声明名为 name 的索引。文档类型 D 的字段用 search 标签说明用途：
//
//   - text：参与全文检索的文本，string 或 []string；text,boost 匹配时权重更高，例如标题。至少要有一个。
//   - filter：过滤、排序和分面用的值，可以是字符串、布尔、整数、浮点数、time.Time、
//     实现 encoding.TextMarshaler 的类型（例如 uuid.UUID），以及它们的切片（多个值）和指针（nil 为没有值）。
//     编码后最长 191 字节。
//   - tenant：文档所在的租户，string。有它时查询总是限定在 Query.Tenant 中。
//
// 例如：
//
//	type ArticleDoc struct {
//	    ID          uuid.UUID `json:"id"`
//	    Tenant      string    `json:"tenant" search:"tenant"`
//	    Title       string    `json:"title" search:"text,boost"`
//	    Body        string    `json:"body" search:"text"`
//	    Tags        []string  `json:"tags" search:"filter"`
//	    PublishedAt time.Time `json:"published_at" search:"filter"`
//	}
//
//	var Articles = search.Define[ArticleDoc]("articles")
//
// 查询、高亮和分面按字段的 JSON 名称引用字段。文档整体编码为 JSON 保存在索引中，查询结果直接解码出 D，
// 所以 D 只放展示搜索结果需要的字段。
//
// name 由小写字母、数字、_、.、- 组成，最长 63 字节。D 的标签不正确时 panic。
func Define[D any](name string, opts ...Option) *Index[D] {
	if !validName.MatchString(name) {
		panic(errors.Newf("search: invalid index name %q", name))
	}
	s, err := parseSchema(reflect.TypeFor[D]())
	if err != nil {
		panic(err)
	}
	o := options{tok: Bigram}
	for _, opt := range opts {
		opt(&o)
	}
	return &Index[D]{
		name: name, schema: s, tok: o.tok,
		sync:    queue.Topic[string]("search.sync." + name),
		reindex: queue.Topic[uuid.UUID]("search.reindex." + name),
	}
}

// Name 返回索引的名称。
func (ix *Index[D]) Name() string { return ix.name }

// LoadFunc 返回 key 当前要被搜索的文档，没有时（已删除、未发布）返回空，索引中这个 key 原有的文档随之删除。
// 一个 key 可以对应多个文档，例如一个附件切分出的多个分段。
//
// 它在同步事务中执行，tx 是这个事务：用 tx 读取数据，读到的就是最新的状态。
// 同一个 key 的同步依次执行，因此它只应读取数据，耗时的处理（转换文件、调用模型）放在之前的任务中完成。
type LoadFunc[D any] func(ctx context.Context, tx bun.IDB, key string) ([]D, error)

// Sync 通过 queue.Database(db) 发布 keys 的同步任务。db 通常是修改数据的事务：事务提交后才同步，回滚时一起取消。
//
// 任务只带 key，处理时用 LoadFunc 重新读取当前的数据，所以重复、乱序执行的结果都一样；
// 同一个 key 还没开始处理的任务用 queue.Unique 合并，连续修改只同步一次。
// key 不能为空，最长 191 字节。
func (ix *Index[D]) Sync(ctx context.Context, db bun.IDB, keys ...string) error {
	for _, key := range keys {
		if err := checkKey(key); err != nil {
			return err
		}
	}
	jobs := queue.Database(db)
	for _, key := range keys {
		if err := ix.sync.Publish(ctx, jobs, key, queue.Unique(key)); err != nil {
			return errors.Wrapf(err, "search: publish %s sync", ix.name)
		}
	}
	return nil
}

// Handle 把同步任务和重建任务（见 Reindex）的处理函数注册到 w。w 必须使用 Sync 发布任务的数据库队列，
// db 是索引所在的数据库，load 读取一个 key 的文档，scan 在重建时列出所有的 key。opts 调整同步任务的处理。
// 文档无法建索引（例如过滤字段的值太长）时同步任务不再重试，直接转入失败任务表。
func (ix *Index[D]) Handle(w *queue.Worker, db *bun.DB, load LoadFunc[D], scan ScanFunc, opts ...queue.HandleOption) {
	if db == nil || load == nil || scan == nil {
		panic("search: nil database, load or scan function")
	}
	ix.sync.Handle(w, func(ctx context.Context, key string) error {
		err := ix.Refresh(ctx, db, load, key)
		if errors.Is(err, errInvalid) {
			return queue.Permanent(err)
		}
		return err
	}, opts...)
	ops := operation.New(db)
	ix.reindex.Handle(w, func(ctx context.Context, id uuid.UUID) error {
		return ix.rebuild(ctx, db, ops, load, scan, id)
	})
}

// Refresh 立即同步 keys，每个 key 一个事务，与同步任务的处理相同。用于测试，或者需要马上能搜到的场合。
// 重建期间同时写入查询使用的版本和正在重建的版本。
func (ix *Index[D]) Refresh(ctx context.Context, db bun.IDB, load LoadFunc[D], keys ...string) error {
	for _, key := range keys {
		if err := checkKey(key); err != nil {
			return err
		}
		if err := ix.refresh(ctx, db, load, key, 0); err != nil {
			return err
		}
	}
	return nil
}

func checkKey(key string) error {
	if key == "" || len(key) > maxValue {
		return errors.Newf("search: key must be 1 to %d bytes, got %d", maxValue, len(key))
	}
	return nil
}

// refresh 在一个事务中同步 key：build 为 0 时写入 versions 返回的版本，否则只写入重建的版本 build。
func (ix *Index[D]) refresh(ctx context.Context, db bun.IDB, load LoadFunc[D], key string, build int) error {
	st := storeOf(db)
	err := db.RunInTx(ctx, st.txOptions(), func(ctx context.Context, tx bun.Tx) error {
		// 先锁住这个 key：同一个 key 的同步依次执行，后执行的读到的数据更新，不会被先读到旧数据的覆盖
		if err := st.lockKey(ctx, tx, ix.name, key); err != nil {
			return err
		}
		versions := []int{build}
		if build == 0 {
			var err error
			if versions, err = st.versions(ctx, tx, ix.name); err != nil {
				return err
			}
		}
		docs, err := load(ctx, tx, key)
		if err != nil {
			return err
		}
		entries, err := ix.entries(docs)
		if err != nil {
			return err
		}
		for _, v := range versions {
			if err := st.replace(ctx, tx, ix.name, v, key, entries); err != nil {
				return err
			}
		}
		if len(entries) == 0 {
			return st.unlockKey(ctx, tx, ix.name, key)
		}
		return nil
	})
	return errors.Wrapf(err, "search: sync %s %q", ix.name, key)
}

// entry 是一个文档在索引中的内容。
type entry struct {
	tenant string
	doc    string
	boost  []string            // 权重更高的文本切分出的词
	body   []string            // 其他文本切分出的词
	values map[string][]string // 过滤字段编码后的值
}

func (ix *Index[D]) entries(docs []D) ([]entry, error) {
	out := make([]entry, len(docs))
	for i, d := range docs {
		v := reflect.ValueOf(d)
		e := entry{values: map[string][]string{}}
		if f := ix.schema.tenant; f != nil {
			if fv, ok := f.get(v); ok {
				e.tenant = fv.String()
			}
			if len(e.tenant) > maxValue {
				return nil, errors.Mark(errors.Newf("search: tenant longer than %d bytes", maxValue), errInvalid)
			}
		}
		for _, f := range ix.schema.text {
			for _, text := range f.texts(v) {
				for _, t := range ix.tok.Index(text) {
					if len(e.boost)+len(e.body) == MaxTerms {
						break
					}
					if s := term(t.Text); s == "" {
						continue
					} else if f.boost {
						e.boost = append(e.boost, s)
					} else {
						e.body = append(e.body, s)
					}
				}
			}
		}
		for name, f := range ix.schema.filters {
			vals, err := f.values(v)
			if err != nil {
				return nil, errors.Mark(err, errInvalid)
			}
			e.values[name] = vals
		}
		b, err := json.Marshal(d)
		if err != nil {
			return nil, errors.Mark(errors.Wrap(err, "search: encode document"), errInvalid)
		}
		e.doc = string(b)
		out[i] = e
	}
	return out, nil
}
