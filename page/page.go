package page

import (
	"encoding/base64"
	"encoding/json"

	"github.com/ryan053857/mygoravel/errorx"
)

// ErrInvalidCursor 表示客户端传入的游标无法解析。
var ErrInvalidCursor = errorx.Define(errorx.InvalidArgument, "page.invalid_cursor")

// OffsetParams 是固定分页的查询参数，嵌入到输入类型中使用。
type OffsetParams struct {
	Page    int `query:"page" default:"1" minimum:"1" doc:"Page number, starting at 1."`
	PerPage int `query:"per_page" default:"20" minimum:"1" maximum:"100" doc:"Items per page."`
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
