package page

import (
	"encoding/base64"
	"encoding/json"

	"github.com/ryan053857/mygoravel/errorx"
)

// ErrInvalidCursor 表示客户端传入的游标无法解析。
var ErrInvalidCursor = errorx.Define(errorx.InvalidArgument, "page.invalid_cursor")

// OffsetParams 是固定分页的查询参数，嵌入到输入类型中使用。
// query 标签用于 JSON 接口（Huma），json 标签用于页面（web.ParseQuery、web.QueryURL）。
type OffsetParams struct {
	Page    int `query:"page" json:"page,omitempty" default:"1" minimum:"1" doc:"Page number, starting at 1."`
	PerPage int `query:"per_page" json:"per_page,omitempty" default:"20" minimum:"1" maximum:"100" doc:"Items per page."`
}

// Limit 返回 SQL 的 LIMIT。
func (p OffsetParams) Limit() int { return p.PerPage }

// Offset 返回 SQL 的 OFFSET。
func (p OffsetParams) Offset() int { return (p.Page - 1) * p.PerPage }

// Offset 是固定分页的响应体。
type Offset[T any] struct {
	Items   []T `json:"items"`
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
	Total   int `json:"total" doc:"Total number of items across all pages."`
}

// NewOffset 组装固定分页响应，nil items 编码为 []。
func NewOffset[T any](items []T, p OffsetParams, total int) Offset[T] {
	return Offset[T]{Items: nonNil(items), Page: p.Page, PerPage: p.PerPage, Total: total}
}

// LastPage 返回最后一页的页码，没有数据时为 1。
func (o Offset[T]) LastPage() int {
	if o.PerPage < 1 || o.Total <= 0 {
		return 1
	}
	return (o.Total + o.PerPage - 1) / o.PerPage
}

// Links 返回分页导航中要显示的页码：首页、末页和当前页两侧各 onEachSide 页，
// 中间省略的部分用 0 表示。例如第 10 页、共 20 页、onEachSide 为 1 时返回 [1 0 9 10 11 0 20]。
// 只隔一页时直接显示那一页，不用省略号。
func (o Offset[T]) Links(onEachSide int) []int {
	last := o.LastPage()
	var links []int
	for p := 1; p <= last; p++ {
		switch {
		case p == 1 || p == last || (p >= o.Page-onEachSide && p <= o.Page+onEachSide):
			links = append(links, p)
		case p == o.Page-onEachSide-1 && p == 2, p == o.Page+onEachSide+1 && p == last-1:
			links = append(links, p) // 省略号只代替一页时，直接显示那一页
		case len(links) > 0 && links[len(links)-1] != 0:
			links = append(links, 0)
		}
	}
	return links
}

// CursorParams 是游标分页的查询参数，嵌入到输入类型中使用。
type CursorParams struct {
	Cursor  string `query:"cursor" doc:"Opaque cursor from next_cursor of the previous page. Omit for the first page."`
	PerPage int    `query:"per_page" default:"20" minimum:"1" maximum:"100" doc:"Items per page."`
}

// Limit 返回 SQL 的 LIMIT，比 PerPage 多取一条用于判断是否还有下一页。
func (p CursorParams) Limit() int { return p.PerPage + 1 }

// After 把游标解码到 v，报告请求是否携带游标；游标无效时返回 ErrInvalidCursor。
// v 的结构与 NewCursor 的 key 返回值一致。
func (p CursorParams) After(v any) (bool, error) {
	if p.Cursor == "" {
		return false, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(p.Cursor)
	if err != nil {
		return false, ErrInvalidCursor.Wrap(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return false, ErrInvalidCursor.Wrap(err)
	}
	return true, nil
}

// Cursor 是游标分页的响应体，没有下一页时省略 next_cursor。
type Cursor[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty" doc:"Pass as cursor to fetch the next page. Absent on the last page."`
}

// NewCursor 组装游标分页响应。items 按 p.Limit() 查询，多出的一条被截掉，
// 并把 key 返回的最后一条的排序位置编码为下一页游标。key 的返回值必须能编码为 JSON，
// 否则 panic。
func NewCursor[T any](items []T, p CursorParams, key func(T) any) Cursor[T] {
	if p.PerPage < 1 || len(items) <= p.PerPage {
		return Cursor[T]{Items: nonNil(items)}
	}
	items = items[:p.PerPage]
	raw, err := json.Marshal(key(items[len(items)-1]))
	if err != nil {
		panic("page: cursor key is not JSON-encodable: " + err.Error())
	}
	return Cursor[T]{Items: items, NextCursor: base64.RawURLEncoding.EncodeToString(raw)}
}

func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}
