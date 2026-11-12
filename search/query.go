package search

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/page"
)

// DefaultPerPage 是 Query.Page.PerPage 不大于 0 时每页的条数。
const DefaultPerPage = 20

// DefaultFacetLimit 是 Query.FacetLimit 不大于 0 时每个分面最多返回的值的个数。
const DefaultFacetLimit = 20

// Query 是一次搜索的条件。
type Query struct {
	// Text 是用户输入的查询文本，切分出的每个词都要出现在文档中，最后一个词按前缀匹配（输入到一半也能搜到）。
	// 为空或者没有切分出词时不按文本过滤，相当于列出符合 Filter 的所有文档。
	Text string
	// Tenant 是查询的租户，"" 为平台本身。文档类型有 tenant 字段时总是只查询这个租户的文档；
	// 没有 tenant 字段时必须为空。
	Tenant string
	Filter Filter   // nil 时不过滤
	Sort   []Sort   // 为空时有查询文本的按相关度排序，否则按 key 排序
	Facets []string // 统计这些过滤字段在结果中各个值的文档数
	// FacetLimit 是每个分面最多返回的值的个数，按文档数从多到少；不大于 0 时为 DefaultFacetLimit。
	FacetLimit int
	// Page 是分页参数，零值为第 1 页、每页 DefaultPerPage 条。
	Page page.OffsetParams
}

// Hit 是一条搜索结果。
type Hit[D any] struct {
	Key   string  `json:"key"`   // 文档所属的 key，即 Sync 时传入的 key
	Doc   D       `json:"doc"`   // 同步时保存的文档
	Score float64 `json:"score"` // 相关度，越大越相关；不同数据库的计算方法不同，只用于比较同一次查询的结果
	// Highlights 是命中查询的文本字段的摘要，按字段的 JSON 名称索引；没有查询文本时为空。
	Highlights map[string]Snippet `json:"highlights,omitempty"`
}

// Result 是一页搜索结果。
type Result[D any] struct {
	page.Offset[Hit[D]]
	Facets map[string][]FacetValue `json:"facets,omitempty"` // 按 Query.Facets 中的字段名索引
}

// FacetValue 是一个分面值和结果中带有这个值的文档数。
type FacetValue struct {
	Value string `json:"value"` // 数字、时间转换为文字，例如 42、2026-11-12T08:00:00Z
	Count int    `json:"count"`
}

// Sort 是一个排序条件，用 Asc 或 Desc 创建。
type Sort struct {
	Field string
	Desc  bool
}

// Asc 按过滤字段 field 从小到大排序，没有值的排在最后。field 不能是切片。
func Asc(field string) Sort { return Sort{Field: field} }

// Desc 按过滤字段 field 从大到小排序，没有值的排在最后。field 不能是切片。
func Desc(field string) Sort { return Sort{Field: field, Desc: true} }

// Search 在 db 中查询 ix，返回 q.Page 指定的一页结果。
func (ix *Index[D]) Search(ctx context.Context, db bun.IDB, q Query) (*Result[D], error) {
	if ix.schema.tenant == nil && q.Tenant != "" {
		return nil, errors.Newf("search: index %s has no tenant field", ix.name)
	}
	p := q.Page
	if p.Page < 1 {
		p.Page = 1
	}
	if p.PerPage < 1 {
		p.PerPage = DefaultPerPage
	}
	terms := parseQuery(ix.tok, q.Text)
	text := len(terms.terms) > 0

	var where sqlExpr
	// 只查询 search_indexes 中记录的版本，没有记录时为 1，见 versions.go
	where.add("e.index_name = ? AND e.version = COALESCE((SELECT i.version FROM search_indexes AS i WHERE i.name = ?), 1)"+
		" AND e.tenant = ?", ix.name, ix.name, q.Tenant)
	columns, scoreArgs := "e.doc_key, e.doc", []any(nil)
	if text {
		query := tsquery(terms)
		where.add(" AND e.tokens @@ ?::tsquery", query)
		columns, scoreArgs = columns+", ts_rank(e.tokens, ?::tsquery) AS score", []any{query}
	}
	if q.Filter != nil {
		where.add(" AND ")
		if err := q.Filter.build(ix.schema, &where); err != nil {
			return nil, err
		}
	}
	const from = "search_entries AS e"

	var order sqlExpr
	for _, s := range q.Sort {
		f := ix.schema.filters[s.Field]
		if f == nil || f.multi {
			return nil, errors.Newf("search: cannot sort by %q: not a single-valued filter field", s.Field)
		}
		value := "(SELECT v.value FROM search_values AS v WHERE v.entry_id = e.id AND v.field = ?)"
		dir := "ASC"
		if s.Desc {
			dir = "DESC"
		}
		order.add(value+" IS NULL, "+value+" "+dir+", ", s.Field, s.Field)
	}
	if text {
		order.add("score DESC, ")
	}
	order.add("e.doc_key, e.position")

	var total int64
	err := db.NewRaw("SELECT COUNT(*) FROM "+from+" WHERE "+where.sql(), where.args...).Scan(ctx, &total)
	if err != nil {
		return nil, errors.Wrap(err, "search: count")
	}
	var rows []struct {
		DocKey string  `bun:"doc_key"`
		Doc    string  `bun:"doc"`
		Score  float64 `bun:"score"` // 没有查询文本时不查询，为 0
	}
	args := append(append(append(scoreArgs, where.args...), order.args...), p.Limit(), p.Offset())
	err = db.NewRaw("SELECT "+columns+" FROM "+from+" WHERE "+where.sql()+
		" ORDER BY "+order.sql()+" LIMIT ? OFFSET ?", args...).Scan(ctx, &rows)
	if err != nil {
		return nil, errors.Wrap(err, "search: query")
	}

	hits := make([]Hit[D], len(rows))
	for i, r := range rows {
		h := Hit[D]{Key: r.DocKey, Score: r.Score}
		if err := json.Unmarshal([]byte(r.Doc), &h.Doc); err != nil {
			return nil, errors.Wrapf(err, "search: decode %s document %q", ix.name, r.DocKey)
		}
		if text {
			h.Highlights = ix.highlights(terms, h.Doc)
		}
		hits[i] = h
	}
	res := &Result[D]{Offset: page.NewOffset(hits, p, total)}
	if len(q.Facets) > 0 {
		if res.Facets, err = ix.facets(ctx, db, q, from, where); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func (ix *Index[D]) highlights(q queryTerms, doc D) map[string]Snippet {
	v := reflect.ValueOf(doc)
	out := map[string]Snippet{}
	for _, f := range ix.schema.text {
		if s, ok := highlight(ix.tok, q, strings.Join(f.texts(v), " ")); ok {
			out[f.name] = s
		}
	}
	return out
}

func (ix *Index[D]) facets(ctx context.Context, db bun.IDB, q Query, from string, where sqlExpr) (map[string][]FacetValue, error) {
	limit := q.FacetLimit
	if limit <= 0 {
		limit = DefaultFacetLimit
	}
	out := map[string][]FacetValue{}
	for _, name := range q.Facets {
		f := ix.schema.filters[name]
		if f == nil {
			return nil, errors.Newf("search: cannot facet by %q: not a filter field", name)
		}
		var rows []struct {
			Value string `bun:"value"`
			N     int    `bun:"n"`
		}
		args := append(append([]any{name}, where.args...), limit)
		err := db.NewRaw("SELECT v.value, COUNT(*) AS n FROM search_values AS v WHERE v.field = ? AND v.entry_id IN "+
			"(SELECT e.id FROM "+from+" WHERE "+where.sql()+") GROUP BY v.value ORDER BY n DESC, v.value LIMIT ?", args...).Scan(ctx, &rows)
		if err != nil {
			return nil, errors.Wrapf(err, "search: facet %q", name)
		}
		values := make([]FacetValue, len(rows))
		for i, r := range rows {
			values[i] = FacetValue{Value: f.decode(r.Value), Count: r.N}
		}
		out[name] = values
	}
	return out, nil
}

// sqlExpr 是拼接中的 SQL 片段和它的参数。
type sqlExpr struct {
	b    strings.Builder
	args []any
}

func (e *sqlExpr) add(sql string, args ...any) {
	e.b.WriteString(sql)
	e.args = append(e.args, args...)
}

func (e *sqlExpr) sql() string { return e.b.String() }
