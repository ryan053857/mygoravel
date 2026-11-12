package vector

import (
	"context"
	"encoding/json"

	"github.com/cockroachdb/errors"
	"github.com/qdrant/go-client/qdrant"
)

// DefaultLimit 是 Query.Limit 不大于 0 时返回的结果数。
const DefaultLimit = 10

// Query 是一次检索的条件。
type Query struct {
	// Text 是查询文本，用 Store 的模型计算向量后检索语义最接近的文档。不能为空。
	Text string
	// Tenant 是查询的租户，"" 为平台本身。文档类型有 tenant 字段时总是只查询这个租户的文档；
	// 没有 tenant 字段时必须为空。
	Tenant string
	Filter Filter // nil 时不过滤
	// Limit 是最多返回的结果数，不大于 0 时为 DefaultLimit。
	Limit int
	// MinScore 是结果的最低相似度，0 时不限制。相似度是余弦相似度，范围是 -1 到 1。
	MinScore float64
}

// Hit 是一条检索结果。
type Hit[D any] struct {
	Key   string  `json:"key"`   // 文档所属的 key，即 Sync 时传入的 key
	Doc   D       `json:"doc"`   // 同步时保存的文档
	Score float64 `json:"score"` // 与查询文本的余弦相似度，越大越接近
}

// Search 检索与 q.Text 语义最接近的文档，按相似度从高到低返回。索引还没有同步过任何文档时返回空。
func (ix *Index[D]) Search(ctx context.Context, s *Store, q Query) ([]Hit[D], error) {
	if q.Text == "" {
		return nil, errors.New("vector: empty query text")
	}
	filter, err := ix.filter(q)
	if err != nil {
		return nil, err
	}
	ok, err := s.open(ctx, ix.name, ix.schema, false)
	if err != nil || !ok {
		return nil, err
	}
	vecs, err := s.model.Embedder.Embed(ctx, []string{q.Text})
	if err != nil {
		return nil, errors.Wrap(err, "vector: embed query")
	}
	if len(vecs) != 1 || len(vecs[0]) != s.model.Dims {
		return nil, errors.Newf("vector: embedder returned an unexpected vector for model %q", s.model.Name)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	req := &qdrant.QueryPoints{
		CollectionName: s.collection(ix.name),
		Query:          qdrant.NewQueryDense(vecs[0]),
		Filter:         filter,
		Limit:          qdrant.PtrOf(uint64(limit)),
		WithPayload:    qdrant.NewWithPayloadInclude(payloadKey, payloadDoc),
	}
	if q.MinScore != 0 {
		req.ScoreThreshold = qdrant.PtrOf(float32(q.MinScore))
	}
	found, err := s.client.Query(ctx, req)
	if err != nil {
		return nil, errors.Wrapf(err, "vector: query %s", ix.name)
	}
	hits := make([]Hit[D], len(found))
	for i, p := range found {
		payload := p.GetPayload()
		h := Hit[D]{Key: payload[payloadKey].GetStringValue(), Score: float64(p.GetScore())}
		if err := json.Unmarshal([]byte(payload[payloadDoc].GetStringValue()), &h.Doc); err != nil {
			return nil, errors.Wrapf(err, "vector: decode %s document %q", ix.name, h.Key)
		}
		hits[i] = h
	}
	return hits, nil
}

// filter 把租户和 q.Filter 转换为 Qdrant 的过滤条件。
func (ix *Index[D]) filter(q Query) (*qdrant.Filter, error) {
	var must []*qdrant.Condition
	if ix.schema.tenant != nil {
		must = append(must, qdrant.NewMatchKeyword(payloadTenant, q.Tenant))
	} else if q.Tenant != "" {
		return nil, errors.Newf("vector: index %s has no tenant field", ix.name)
	}
	if q.Filter != nil {
		c, err := q.Filter.condition(ix.schema)
		if err != nil {
			return nil, err
		}
		must = append(must, c)
	}
	if len(must) == 0 {
		return nil, nil
	}
	return &qdrant.Filter{Must: must}, nil
}
