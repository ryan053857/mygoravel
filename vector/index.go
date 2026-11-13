package vector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"regexp"
	"strconv"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
	"github.com/uptrace/bun"

	"github.com/go-tenon/tenon/operation"
	"github.com/go-tenon/tenon/queue"
	"github.com/go-tenon/tenon/search"
)

// validName 是索引名称的格式，它也是 collection 名称和同步任务 topic 的一部分。
var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

// 这个包使用的 payload 字段，以 _ 开头，不与过滤字段冲突。
const (
	payloadKey    = "_key"    // 文档所属的 key
	payloadPos    = "_pos"    // 文档在 key 的文档中的位置，从 0 开始
	payloadHash   = "_hash"   // 计算向量的文本的 SHA-256，文本不变时沿用原来的向量
	payloadTenant = "_tenant" // 文档所在的租户
	payloadDoc    = "_doc"    // 编码为 JSON 的文档
)

// batchSize 是一次写入 Qdrant 的最多文档数。
const batchSize = 256

// errInvalid 标记无法建索引的文档，例如计算向量的文本为空。重试也不会成功，同步任务直接转入失败任务表。
var errInvalid = errors.New("vector: invalid document")

// pointSpace 是 point ID 的 UUID 命名空间：同一个 key 的同一个位置总是同一个 ID，重复写入覆盖原来的。
var pointSpace = uuid.MustParse("1b2f1a6e-3f3c-4f57-9b1e-6c4f0d3e8a21")

// Index 是一个向量索引，D 是它的文档类型。用 Define 声明为包级变量，同步和查询都通过它。
type Index[D any] struct {
	name    string
	schema  *schema
	sync    queue.Topic[string]
	reindex queue.Topic[uuid.UUID]
}

// Define 声明名为 name 的索引。文档类型 D 的字段用 vector 标签说明用途：
//
//   - embed：计算向量的文本，string，有且只有一个。只用来计算向量、不需要保存的文本可以是 json:"-"，
//     例如 split.Chunk.TextWithContext() 的结果。
//   - filter：过滤用的值，可以是字符串、布尔、整数、浮点数、time.Time、
//     实现 encoding.TextMarshaler 的类型（例如 uuid.UUID），以及它们的切片（多个值）和指针（nil 为没有值）。
//     字段的 JSON 名称不能以 _ 开头，不能包含 .、[、]。
//   - tenant：文档所在的租户，string。有它时查询总是限定在 Query.Tenant 中。
//
// 例如：
//
//	type ChunkDoc struct {
//	    FileID  uuid.UUID `json:"file_id" vector:"filter"`
//	    Tenant  string    `json:"tenant" vector:"tenant"`
//	    Text    string    `json:"text"`
//	    Pages   []int     `json:"pages" vector:"filter"`
//	    Context string    `json:"-" vector:"embed"`
//	}
//
//	var Chunks = vector.Define[ChunkDoc]("attachment-chunks")
//
// 过滤条件按字段的 JSON 名称引用字段。文档整体编码为 JSON 保存在索引中，查询结果直接解码出 D，
// 所以 D 只放使用查询结果需要的字段。
//
// 默认只按模型计算的向量检索语义，用 Hybrid 声明为混合索引后同时按关键词检索。
//
// name 由小写字母、数字、_、.、- 组成，最长 63 字节。D 的标签不正确时 panic。
func Define[D any](name string, opts ...IndexOption) *Index[D] {
	if !validName.MatchString(name) {
		panic(errors.Newf("vector: invalid index name %q", name))
	}
	s, err := parseSchema(reflect.TypeFor[D]())
	if err != nil {
		panic(err)
	}
	for _, opt := range opts {
		opt(s)
	}
	return &Index[D]{
		name: name, schema: s,
		sync:    queue.Topic[string]("vector.sync." + name),
		reindex: queue.Topic[uuid.UUID]("vector.reindex." + name),
	}
}

// IndexOption 是 Define 的选项。
type IndexOption func(*schema)

// Hybrid 把索引声明为混合索引：除了模型计算的向量，还用 tok 切分 embed 字段的文本，按 BM25 计算关键词的稀疏向量。
// 查询时语义和关键词两路分别检索，再按排名用 RRF 融合，型号、编号、人名这类语义相近却不对的结果因此排在后面。
// tok 通常是 search.Bigram，需要按词语切分时用 search/gse。
//
// 混合索引的 collection 与普通索引的结构不同：给已有的索引加上或去掉 Hybrid、换分词器之后都要用 Reindex 重建，
// 重建完成之前原来的版本按原来的方式写入和查询。
func Hybrid(tok search.Tokenizer) IndexOption {
	if tok == nil {
		panic("vector: nil tokenizer")
	}
	return func(s *schema) { s.tokenizer = tok }
}

// Name 返回索引的名称。
func (ix *Index[D]) Name() string { return ix.name }

// LoadFunc 返回 key 当前要被检索的文档，没有时（已删除、未发布）返回空，索引中这个 key 原有的文档随之删除。
// 一个 key 可以对应多个文档，例如一个附件切分出的多个分段。
//
// 它在同步事务中执行，tx 是这个事务：用 tx 读取数据，读到的就是最新的状态。
// 同一个 key 的同步依次执行，因此它只应读取数据，耗时的处理（转换、切分文件）放在之前的任务中完成。
type LoadFunc[D any] func(ctx context.Context, tx bun.IDB, key string) ([]D, error)

// Sync 发布 keys 的同步任务。db 通常是修改数据的事务：事务提交后才同步，回滚时一起取消。
//
// 任务只带 key，处理时用 LoadFunc 重新读取当前的数据，所以重复、乱序执行的结果都一样。key 不能为空。
func (ix *Index[D]) Sync(ctx context.Context, db bun.IDB, keys ...string) error {
	for _, key := range keys {
		if err := checkKey(key); err != nil {
			return err
		}
	}
	return errors.Wrapf(ix.sync.PublishAll(ctx, db, keys), "vector: publish %s sync", ix.name)
}

// Handle 把同步任务和重建任务（见 Reindex）的处理函数注册到 w，
// load 读取一个 key 的文档，scan 在重建时列出所有的 key。opts 调整同步任务的处理。
// 文档无法建索引（例如计算向量的文本为空）时同步任务不再重试，直接转入失败任务表。
func (ix *Index[D]) Handle(w *queue.Worker, s *Store, load LoadFunc[D], scan ScanFunc, opts ...queue.HandleOption) {
	if s == nil || load == nil || scan == nil {
		panic("vector: nil store, load or scan function")
	}
	ix.sync.Handle(w, func(ctx context.Context, key string) error {
		err := ix.Refresh(ctx, s, load, key)
		if errors.Is(err, errInvalid) {
			return queue.Permanent(err)
		}
		return err
	}, opts...)
	ops := operation.New(s.db)
	ix.reindex.Handle(w, func(ctx context.Context, id uuid.UUID) error {
		return ix.rebuild(ctx, s, ops, load, scan, id)
	})
}

// Refresh 立即同步 keys，与同步任务的处理相同。用于测试，或者需要马上能查到的场合。
// 重建期间同时写入查询使用的版本和正在重建的版本。
//
// 每个 key 在一个事务中同步：锁住 key，读取文档，计算文本有变化的文档的向量，写入 Qdrant，删除多出的文档。
// 计算向量期间事务保持打开，同一个 key 的同步依次执行，先读到旧数据的不会覆盖后读到新数据的。
func (ix *Index[D]) Refresh(ctx context.Context, s *Store, load LoadFunc[D], keys ...string) error {
	for _, key := range keys {
		if err := checkKey(key); err != nil {
			return err
		}
	}
	for _, key := range keys {
		if err := ix.refresh(ctx, s, load, key, 0); err != nil {
			return err
		}
	}
	return nil
}

func checkKey(key string) error {
	if key == "" {
		return errors.New("vector: empty key")
	}
	return nil
}

// refresh 在一个事务中同步 key：build 为 0 时写入 indexVersions 返回的版本，否则只写入重建的版本 build。
func (ix *Index[D]) refresh(ctx context.Context, s *Store, load LoadFunc[D], key string, build int) error {
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// 先锁住这个 key：同一个 key 的同步依次执行，后执行的读到的数据更新，不会被先读到旧数据的覆盖
		_, err := tx.NewRaw(`INSERT INTO vector_keys (index_name, doc_key, synced_at) VALUES (?, ?, ?) `+
			`ON CONFLICT (index_name, doc_key) DO UPDATE SET synced_at = excluded.synced_at`,
			ix.name, key, time.Now().UTC()).Exec(ctx)
		if err != nil {
			return errors.Wrap(err, "vector: lock key")
		}
		versions := []int{build}
		if build == 0 {
			if versions, err = indexVersions(ctx, tx, ix.name); err != nil {
				return err
			}
		}
		docs, err := load(ctx, tx, key)
		if err != nil {
			return err
		}
		entries, err := ix.entries(key, docs)
		if err != nil {
			return err
		}
		for _, v := range versions {
			if err := ix.write(ctx, s, v, key, entries); err != nil {
				return err
			}
		}
		if len(entries) == 0 {
			_, err := tx.NewRaw(`DELETE FROM vector_keys WHERE index_name = ? AND doc_key = ?`, ix.name, key).Exec(ctx)
			return errors.Wrap(err, "vector: delete key")
		}
		return nil
	})
	return errors.Wrapf(err, "vector: sync %s %q", ix.name, key)
}

// write 把 key 在版本 version 中原有的文档替换为 entries，用建立这个版本的模型计算向量。
func (ix *Index[D]) write(ctx context.Context, s *Store, version int, key string, entries []entry) error {
	collection := s.collection(ix.name, version)
	// 重建可能在读取版本之后切换并删除了 version：这时会重新创建它，写入的文档不会被查询，
	// 下一次重建开始时随其他废弃的版本一起删除
	c, _, err := s.open(ctx, collection, ix.schema, true)
	if err != nil {
		return err
	}
	points, err := s.embed(ctx, collection, c, key, entries)
	if err != nil {
		return err
	}
	for start := 0; start < len(points); start += batchSize {
		_, err := s.client.Upsert(ctx, &qdrant.UpsertPoints{
			CollectionName: collection, Points: points[start:min(start+batchSize, len(points))], Wait: qdrant.PtrOf(true),
		})
		if err != nil {
			return errors.Wrap(err, "vector: upsert points")
		}
	}
	// 删除这个 key 原来比现在多出的文档
	_, err = s.client.Delete(ctx, &qdrant.DeletePoints{
		CollectionName: collection, Wait: qdrant.PtrOf(true),
		Points: qdrant.NewPointsSelectorFilter(&qdrant.Filter{Must: []*qdrant.Condition{
			qdrant.NewMatchKeyword(payloadKey, key),
			qdrant.NewRange(payloadPos, &qdrant.Range{Gte: qdrant.PtrOf(float64(len(points)))}),
		}}),
	})
	return errors.Wrap(err, "vector: delete points")
}

// entry 是一个文档要写入 Qdrant 的内容。
type entry struct {
	text   string // 计算向量的文本
	hash   string // text 的 SHA-256
	sparse sparse // 混合索引中 text 的稀疏向量
	point  *qdrant.PointStruct
}

// entries 把 key 的文档转换为 point，向量留空，由 Store.embed 填入。
func (ix *Index[D]) entries(key string, docs []D) ([]entry, error) {
	out := make([]entry, len(docs))
	for i, d := range docs {
		v := reflect.ValueOf(d)
		var text string
		if t, ok := ix.schema.embed.get(v); ok {
			text = t.String()
		}
		if text == "" {
			return nil, errors.Mark(errors.Newf("vector: document %d has no text to embed", i), errInvalid)
		}
		b, err := json.Marshal(d)
		if err != nil {
			return nil, errors.Mark(errors.Wrap(err, "vector: encode document"), errInvalid)
		}
		sum := sha256.Sum256([]byte(text))
		hash := hex.EncodeToString(sum[:])
		payload := map[string]*qdrant.Value{
			payloadKey:  qdrant.NewValueString(key),
			payloadPos:  qdrant.NewValueInt(int64(i)),
			payloadHash: qdrant.NewValueString(hash),
			payloadDoc:  qdrant.NewValueString(string(b)),
		}
		if f := ix.schema.tenant; f != nil {
			var tenant string
			if t, ok := f.get(v); ok {
				tenant = t.String()
			}
			payload[payloadTenant] = qdrant.NewValueString(tenant)
		}
		for name, f := range ix.schema.filters {
			value, err := f.payload(v)
			if err != nil {
				return nil, errors.Mark(err, errInvalid)
			}
			if value != nil {
				payload[name] = value
			}
		}
		out[i] = entry{text: text, hash: hash, point: &qdrant.PointStruct{Id: pointID(key, i), Payload: payload}}
		if tok := ix.schema.tokenizer; tok != nil {
			out[i].sparse = documentSparse(tok, text)
		}
	}
	return out, nil
}

func pointID(key string, pos int) *qdrant.PointId {
	return qdrant.NewIDUUID(uuid.NewSHA1(pointSpace, []byte(key+"\x00"+strconv.Itoa(pos))).String())
}
