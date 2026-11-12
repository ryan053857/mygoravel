package vector

import (
	"context"
	"regexp"
	"sync"

	"github.com/cockroachdb/errors"
	"github.com/qdrant/go-client/qdrant"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/ai"
)

// validPrefix 是 collection 名称前缀的格式。
var validPrefix = regexp.MustCompile(`^[a-z0-9_.-]{0,32}$`)

// Model 是计算向量的模型。
type Model struct {
	// Name 是模型的名称，例如 text-embedding-3-small。它记录在 collection 中：
	// 不同模型的向量不能比较，换了模型之后，用旧模型建立的 collection 不能再写入和查询。
	Name string
	// Dims 是向量的维度，与模型输出的一致。
	Dims int
	// Embedder 计算向量，通常是 openai.New(cfg.Embedding)。
	Embedder ai.Embedder
}

// Store 把索引保存在 Qdrant 中。它持有应用的数据库，用来在同步时锁住 key、读取文档。用 New 创建，可以并发使用。
type Store struct {
	db     *bun.DB
	client *qdrant.Client
	model  Model
	prefix string

	mu    sync.Mutex
	ready map[string]bool // 已经确认模型一致、建好 payload 索引的 collection
}

// Option 是 New 的选项。
type Option func(*Store)

// WithPrefix 给所有 collection 的名称加上前缀，多个应用共用一个 Qdrant 时用它区分。
// prefix 由小写字母、数字、_、.、- 组成，最长 32 字节。
func WithPrefix(prefix string) Option {
	return func(s *Store) { s.prefix = prefix }
}

// New 创建 Store。db 是应用的数据库，需要 vector 的迁移；client 是 Qdrant 的客户端，在入口创建一次；
// model 计算所有索引的向量。参数不正确时 panic。
func New(db *bun.DB, client *qdrant.Client, model Model, opts ...Option) *Store {
	if db == nil || client == nil || model.Embedder == nil {
		panic("vector: nil database, client or embedder")
	}
	if model.Name == "" || model.Dims <= 0 {
		panic("vector: model needs a name and positive dimensions")
	}
	s := &Store{db: db, client: client, model: model, ready: map[string]bool{}}
	for _, opt := range opts {
		opt(s)
	}
	if !validPrefix.MatchString(s.prefix) {
		panic(errors.Newf("vector: invalid collection prefix %q", s.prefix))
	}
	return s
}

// collection 返回索引的 collection 的名称。
func (s *Store) collection(index string) string { return s.prefix + index + "_v1" }

// metadata 中记录模型的键。
const (
	metaModel = "mygoravel.model"
	metaDims  = "mygoravel.dims"
)

// open 确认索引的 collection 存在且是用 s.model 建立的。不存在时，create 为 true 则创建，否则返回 false。
func (s *Store) open(ctx context.Context, index string, sc *schema, create bool) (bool, error) {
	name := s.collection(index)
	s.mu.Lock()
	ready := s.ready[name]
	s.mu.Unlock()
	if ready {
		return true, nil
	}

	exists, err := s.client.CollectionExists(ctx, name)
	if err != nil {
		return false, errors.Wrapf(err, "vector: check collection %s", name)
	}
	if !exists {
		if !create {
			return false, nil
		}
		if err := s.create(ctx, name, sc); err != nil {
			return false, err
		}
	}
	info, err := s.client.GetCollectionInfo(ctx, name)
	if err != nil {
		return false, errors.Wrapf(err, "vector: get collection %s", name)
	}
	if err := s.checkModel(name, info); err != nil {
		return false, err
	}
	// 文档类型新增的过滤字段也在这里建立索引；已经存在的索引不受影响
	if err := s.createFieldIndexes(ctx, name, sc); err != nil {
		return false, err
	}
	s.mu.Lock()
	s.ready[name] = true
	s.mu.Unlock()
	return true, nil
}

func (s *Store) checkModel(name string, info *qdrant.CollectionInfo) error {
	meta := info.GetConfig().GetMetadata()
	model, dims := meta[metaModel].GetStringValue(), meta[metaDims].GetIntegerValue()
	if model != s.model.Name || dims != int64(s.model.Dims) {
		return errors.Newf("vector: collection %s was built with model %q (%d dimensions), configured %q (%d dimensions)",
			name, model, dims, s.model.Name, s.model.Dims)
	}
	return nil
}

// create 用 s.model 创建 collection。多个进程同时创建时只有一个成功，其余的沿用它。
func (s *Store) create(ctx context.Context, name string, sc *schema) error {
	req := &qdrant.CreateCollection{
		CollectionName: name,
		VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{
			Size: uint64(s.model.Dims), Distance: qdrant.Distance_Cosine,
		}),
		Metadata: map[string]*qdrant.Value{
			metaModel: qdrant.NewValueString(s.model.Name),
			metaDims:  qdrant.NewValueInt(int64(s.model.Dims)),
		},
	}
	if sc.tenant != nil {
		// 查询总是限定在一个租户中：只给每个租户建立 HNSW 图，不建全局的图
		req.HnswConfig = &qdrant.HnswConfigDiff{M: qdrant.PtrOf(uint64(0)), PayloadM: qdrant.PtrOf(uint64(16))}
	}
	if err := s.client.CreateCollection(ctx, req); err != nil {
		// 另一个进程同时创建成功时沿用它
		if exists, _ := s.client.CollectionExists(ctx, name); !exists {
			return errors.Wrapf(err, "vector: create collection %s", name)
		}
	}
	return nil
}

// createFieldIndexes 给框架使用的 _key、租户和过滤字段建立 payload 索引，已经存在的不变。
func (s *Store) createFieldIndexes(ctx context.Context, name string, sc *schema) error {
	type index struct {
		field  string
		typ    qdrant.FieldType
		params *qdrant.PayloadIndexParams
	}
	indexes := []index{{field: payloadKey, typ: qdrant.FieldType_FieldTypeKeyword}}
	if sc.tenant != nil {
		indexes = append(indexes, index{payloadTenant, qdrant.FieldType_FieldTypeKeyword,
			qdrant.NewPayloadIndexParamsKeyword(&qdrant.KeywordIndexParams{IsTenant: qdrant.PtrOf(true)})})
	}
	for _, f := range sc.filters {
		indexes = append(indexes, index{field: f.name, typ: f.fieldType()})
	}
	for _, ix := range indexes {
		_, err := s.client.CreateFieldIndex(ctx, &qdrant.CreateFieldIndexCollection{
			CollectionName: name, FieldName: ix.field, FieldType: ix.typ.Enum(), FieldIndexParams: ix.params,
			Wait: qdrant.PtrOf(true),
		})
		if err != nil {
			return errors.Wrapf(err, "vector: create payload index %s.%s", name, ix.field)
		}
	}
	return nil
}

// embed 给 entries 填入向量，返回它们的 point：文本与这个 key 已有的某个文档相同时沿用它的向量，
// 其余的去掉重复后一次交给模型计算。
func (s *Store) embed(ctx context.Context, collection, key string, entries []entry) ([]*qdrant.PointStruct, error) {
	points := make([]*qdrant.PointStruct, len(entries))
	if len(entries) == 0 {
		return points, nil
	}
	hashes := make([]string, len(entries))
	for i, e := range entries {
		hashes[i] = e.hash
	}
	known, err := s.vectors(ctx, collection, key, hashes)
	if err != nil {
		return nil, err
	}
	var texts []string
	var missing []string // texts 对应的哈希
	for _, e := range entries {
		if _, ok := known[e.hash]; !ok {
			known[e.hash] = nil
			texts, missing = append(texts, e.text), append(missing, e.hash)
		}
	}
	if len(texts) > 0 {
		vecs, err := s.model.Embedder.Embed(ctx, texts)
		if err != nil {
			return nil, errors.Wrap(err, "vector: embed")
		}
		if len(vecs) != len(texts) {
			return nil, errors.Newf("vector: embedder returned %d vectors for %d texts", len(vecs), len(texts))
		}
		for i, v := range vecs {
			if len(v) != s.model.Dims {
				return nil, errors.Newf("vector: embedder returned %d dimensions, model %q has %d", len(v), s.model.Name, s.model.Dims)
			}
			known[missing[i]] = v
		}
	}
	for i, e := range entries {
		e.point.Vectors = qdrant.NewVectorsDense(known[e.hash])
		points[i] = e.point
	}
	return points, nil
}

// vectors 返回 key 已有的文档中，文本的哈希在 hashes 中的文档的向量，按哈希索引。
func (s *Store) vectors(ctx context.Context, collection, key string, hashes []string) (map[string][]float32, error) {
	known := map[string][]float32{}
	req := &qdrant.ScrollPoints{
		CollectionName: collection,
		Filter: &qdrant.Filter{Must: []*qdrant.Condition{
			qdrant.NewMatchKeyword(payloadKey, key), qdrant.NewMatchKeywords(payloadHash, hashes...),
		}},
		Limit:       qdrant.PtrOf(uint32(batchSize)),
		WithPayload: qdrant.NewWithPayloadInclude(payloadHash),
		WithVectors: qdrant.NewWithVectors(true),
	}
	for {
		found, next, err := s.client.ScrollAndOffset(ctx, req)
		if err != nil {
			return nil, errors.Wrap(err, "vector: read existing points")
		}
		for _, p := range found {
			if v := p.GetVectors().GetVector().GetDenseVector().GetData(); len(v) == s.model.Dims {
				known[p.GetPayload()[payloadHash].GetStringValue()] = v
			}
		}
		if next == nil {
			return known, nil
		}
		req.Offset = next
	}
}
