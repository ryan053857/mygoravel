package vector

import (
	"context"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/qdrant/go-client/qdrant"
	"github.com/uptrace/bun"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/go-tenon/tenon/ai"
)

// validPrefix 是 collection 名称前缀的格式。
var validPrefix = regexp.MustCompile(`^[a-z0-9_.-]{0,32}$`)

// Model 是计算向量的模型。
type Model struct {
	// Name 是模型的名称，例如 text-embedding-3-small。它和 Dims 一起记录在索引的每个版本中：
	// 不同模型、不同维度的向量都不能比较，一个版本的写入和查询总是使用建立它的模型和维度。
	Name string
	// Dims 是向量的维度，与模型输出的一致。同一个模型可以输出不同的维度，例如 openai.WithDimensions。
	Dims int
	// Embedder 计算向量，通常是 openai.New(cfg.Embedding)。
	Embedder ai.Embedder
}

// Store 把索引保存在 Qdrant 中。它持有应用的数据库，用来在同步时锁住 key、读取文档、记录索引的版本。
// 用 New 创建，可以并发使用。
type Store struct {
	db     *bun.DB
	client *qdrant.Client
	model  Model             // 建立新版本使用的模型
	models map[modelID]Model // 所有模型：model 和 WithPrevious 传入的旧模型
	prefix string

	opening singleflight.Group
	mu      sync.Mutex
	ready   map[string]collectionInfo // 已经确认模型、建好 payload 索引的 collection
}

// Option 是 New 的选项。
type Option func(*Store)

// WithPrefix 给所有 collection 的名称加上前缀，多个应用共用一个 Qdrant 时用它区分。
// prefix 由小写字母、数字、_、.、- 组成，最长 32 字节。
func WithPrefix(prefix string) Option {
	return func(s *Store) { s.prefix = prefix }
}

// WithPrevious 传入换模型之前使用的模型。名称或维度不同都算换了模型，例如同一个模型从 3072 维改为 1024 维。
// 换了模型之后，索引在重建完成之前仍是用旧模型建立的，
// 查询和同步它都要用旧模型；重建完成后去掉这个选项。
func WithPrevious(models ...Model) Option {
	return func(s *Store) {
		for _, m := range models {
			s.addModel(m)
		}
	}
}

// New 创建 Store。db 是应用的数据库，需要 vector 的迁移；client 是 Qdrant 的客户端，在入口创建一次；
// model 计算向量，索引的新版本都用它建立。参数不正确时 panic。
func New(db *bun.DB, client *qdrant.Client, model Model, opts ...Option) *Store {
	if db == nil || client == nil {
		panic("vector: nil database or client")
	}
	s := &Store{db: db, client: client, model: model, models: map[modelID]Model{}, ready: map[string]collectionInfo{}}
	s.addModel(model)
	for _, opt := range opts {
		opt(s)
	}
	if !validPrefix.MatchString(s.prefix) {
		panic(errors.Newf("vector: invalid collection prefix %q", s.prefix))
	}
	return s
}

func (s *Store) addModel(m Model) {
	if m.Name == "" || m.Dims <= 0 || m.Embedder == nil {
		panic("vector: a model needs a name, positive dimensions and an embedder")
	}
	id := modelID{m.Name, m.Dims}
	if _, ok := s.models[id]; ok {
		panic(errors.Newf("vector: duplicate model %q with %d dimensions", m.Name, m.Dims))
	}
	s.models[id] = m
}

// modelID 标识一个模型：同一个模型可以输出不同维度的向量（例如 openai.WithDimensions），不同维度的向量也不能比较。
type modelID struct {
	name string
	dims int
}

// collection 返回索引的一个版本的 collection 名称。
func (s *Store) collection(index string, version int) string {
	return s.prefix + index + "_v" + strconv.Itoa(version)
}

// metadata 中记录模型的键。
const (
	metaModel = "mygoravel.model"
	metaDims  = "mygoravel.dims"
)

// collectionInfo 是 collection 的结构，写入和查询它都按这个结构：索引的每个版本保持建立时的模型和结构，
// 给索引加上或去掉 Hybrid、换模型之后，重建完成之前原来的版本照常使用。
type collectionInfo struct {
	model  Model // 建立它的模型
	hybrid bool  // 有没有稀疏向量
}

// open 确认 collection 存在，返回它的结构。不存在时，create 为 true 则按 s.model 和 sc 创建，否则 ok 为 false。
func (s *Store) open(ctx context.Context, collection string, sc *schema, create bool) (c collectionInfo, ok bool, err error) {
	s.mu.Lock()
	c, ok = s.ready[collection]
	s.mu.Unlock()
	if ok {
		return c, true, nil
	}
	// 同一个进程中同时打开同一个 collection 的只有一个去读取、创建，其余的等它的结果
	type result struct {
		c  collectionInfo
		ok bool
	}
	v, err, _ := s.opening.Do(collection+"\x00"+strconv.FormatBool(create), func() (any, error) {
		c, ok, err := s.load(ctx, collection, sc, create)
		return result{c, ok}, err
	})
	if err != nil {
		return c, false, err
	}
	r := v.(result)
	return r.c, r.ok, nil
}

// load 读取 collection 的结构，建立 payload 索引，缓存在 s.ready 中。不存在时，create 为 true 则创建，否则 ok 为 false。
func (s *Store) load(ctx context.Context, collection string, sc *schema, create bool) (c collectionInfo, ok bool, err error) {
	info, err := s.info(ctx, collection, false)
	if notFound(err) {
		if !create {
			return c, false, nil
		}
		if err := s.create(ctx, collection, sc); err != nil {
			return c, false, err
		}
		info, err = s.info(ctx, collection, true)
	}
	if err != nil {
		return c, false, errors.Wrapf(err, "vector: get collection %s", collection)
	}
	if c.model, err = s.modelOf(collection, info); err != nil {
		return c, false, err
	}
	c.hybrid = info.GetConfig().GetParams().GetSparseVectorsConfig().GetMap()[vectorSparse] != nil
	// 文档类型新增的过滤字段也在这里建立索引；已经存在的索引不受影响
	if err := s.createFieldIndexes(ctx, collection, sc); err != nil {
		return c, false, err
	}
	s.mu.Lock()
	s.ready[collection] = c
	s.mu.Unlock()
	return c, true, nil
}

// modelOf 返回建立 collection 的模型，它必须是 Store 的模型之一。
func (s *Store) modelOf(collection string, info *qdrant.CollectionInfo) (Model, error) {
	meta := info.GetConfig().GetMetadata()
	name, dims := meta[metaModel].GetStringValue(), meta[metaDims].GetIntegerValue()
	m, ok := s.models[modelID{name, int(dims)}]
	if !ok {
		return Model{}, errors.Newf("vector: collection %s was built with model %q (%d dimensions), which is not configured; "+
			"pass it with vector.WithPrevious until a reindex with %q (%d dimensions) finishes",
			collection, name, dims, s.model.Name, s.model.Dims)
	}
	return m, nil
}

// create 用 s.model 创建 collection。多个进程同时创建时只有一个成功，其余的沿用它。
func (s *Store) create(ctx context.Context, collection string, sc *schema) error {
	dense := &qdrant.VectorParams{Size: uint64(s.model.Dims), Distance: qdrant.Distance_Cosine}
	req := &qdrant.CreateCollection{
		CollectionName: collection,
		VectorsConfig:  qdrant.NewVectorsConfig(dense),
		Metadata: map[string]*qdrant.Value{
			metaModel: qdrant.NewValueString(s.model.Name),
			metaDims:  qdrant.NewValueInt(int64(s.model.Dims)),
		},
	}
	if sc.tokenizer != nil {
		// 混合索引：命名的稠密向量，加上由 Qdrant 按 collection 统计 IDF 的稀疏向量
		req.VectorsConfig = qdrant.NewVectorsConfigMap(map[string]*qdrant.VectorParams{vectorDense: dense})
		req.SparseVectorsConfig = qdrant.NewSparseVectorsConfig(map[string]*qdrant.SparseVectorParams{
			vectorSparse: {Modifier: qdrant.Modifier_Idf.Enum()},
		})
	}
	if sc.tenant != nil {
		// 查询总是限定在一个租户中：只给每个租户建立 HNSW 图，不建全局的图
		req.HnswConfig = &qdrant.HnswConfigDiff{M: qdrant.PtrOf(uint64(0)), PayloadM: qdrant.PtrOf(uint64(16))}
	}
	err := s.client.CreateCollection(ctx, req)
	if status.Code(err) == codes.AlreadyExists {
		return nil // 另一个进程同时创建成功，沿用它
	}
	return errors.Wrapf(err, "vector: create collection %s", collection)
}

// info 读取 collection 的信息。collection 刚被创建、还在初始化时，Qdrant 会返回 Internal 或 Unavailable，
// 例如另一个进程同时创建了它：这时稍等再读，最多等 infoAttempts 次。created 为 true 时刚刚创建过，NotFound 也稍等再读。
func (s *Store) info(ctx context.Context, collection string, created bool) (*qdrant.CollectionInfo, error) {
	for attempt := 1; ; attempt++ {
		info, err := s.client.GetCollectionInfo(ctx, collection)
		code := status.Code(err)
		initializing := code == codes.Internal || code == codes.Unavailable || (created && code == codes.NotFound)
		if err == nil || !initializing || attempt == infoAttempts {
			return info, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(infoInterval):
		}
	}
}

// 等待 collection 初始化：最多读取 infoAttempts 次，每次间隔 infoInterval。
const (
	infoAttempts = 50
	infoInterval = 100 * time.Millisecond
)

// notFound 报告 err 是不是 Qdrant 返回的 collection 不存在。
func notFound(err error) bool { return status.Code(err) == codes.NotFound }

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

// embed 给 entries 填入 m 计算的向量，返回它们的 point：文本与这个 key 在 collection 中已有的某个文档相同时
// 沿用它的向量，其余的去掉重复后一次交给模型计算。
// 有稀疏向量的 collection 中，point 同时带上 entries 的稀疏向量（索引没有声明 Hybrid 时没有）。
func (s *Store) embed(ctx context.Context, collection string, c collectionInfo, key string, entries []entry) ([]*qdrant.PointStruct, error) {
	m, hybrid := c.model, c.hybrid
	points := make([]*qdrant.PointStruct, len(entries))
	if len(entries) == 0 {
		return points, nil
	}
	hashes := make([]string, len(entries))
	for i, e := range entries {
		hashes[i] = e.hash
	}
	known, err := s.vectors(ctx, collection, m, hybrid, key, hashes)
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
		vecs, err := m.Embedder.Embed(ctx, texts)
		if err != nil {
			return nil, errors.Wrap(err, "vector: embed")
		}
		if len(vecs) != len(texts) {
			return nil, errors.Newf("vector: embedder returned %d vectors for %d texts", len(vecs), len(texts))
		}
		for i, v := range vecs {
			if len(v) != m.Dims {
				return nil, errors.Newf("vector: embedder returned %d dimensions, model %q has %d", len(v), m.Name, m.Dims)
			}
			known[missing[i]] = v
		}
	}
	for i, e := range entries {
		e.point.Vectors = qdrant.NewVectorsDense(known[e.hash])
		if hybrid {
			named := map[string]*qdrant.Vector{vectorDense: qdrant.NewVectorDense(known[e.hash])}
			if len(e.sparse.indices) > 0 { // 没有切分出词的文本只有稠密向量
				named[vectorSparse] = qdrant.NewVectorSparse(e.sparse.indices, e.sparse.values)
			}
			e.point.Vectors = qdrant.NewVectorsMap(named)
		}
		points[i] = e.point
	}
	return points, nil
}

// vectors 返回 key 已有的文档中，文本的哈希在 hashes 中的文档的向量，按哈希索引。
func (s *Store) vectors(ctx context.Context, collection string, m Model, hybrid bool, key string, hashes []string) (map[string][]float32, error) {
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
	if hybrid {
		req.WithVectors = qdrant.NewWithVectorsInclude(vectorDense)
	}
	for {
		found, next, err := s.client.ScrollAndOffset(ctx, req)
		if err != nil {
			return nil, errors.Wrap(err, "vector: read existing points")
		}
		for _, p := range found {
			v := p.GetVectors().GetVector()
			if hybrid {
				v = p.GetVectors().GetVectors().GetVectors()[vectorDense]
			}
			if v := v.GetDenseVector().GetData(); len(v) == m.Dims {
				known[p.GetPayload()[payloadHash].GetStringValue()] = v
			}
		}
		if next == nil {
			return known, nil
		}
		req.Offset = next
	}
}
