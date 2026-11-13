// Package article 是 clientgen 测试用的示例模块：一个保存在内存中的文章接口。
package article

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/go-tenon/tenon/errorx"
	"github.com/go-tenon/tenon/httpx"
	"github.com/go-tenon/tenon/httpx/rest"
	"github.com/go-tenon/tenon/page"
)

// Status 是文章的状态。
type Status string

const (
	// StatusDraft 是还没有发布的草稿。
	StatusDraft Status = "draft"
	// StatusPublished 是已经发布的文章。
	StatusPublished Status = "published"
)

var (
	// ErrNotFound 表示文章不存在。
	ErrNotFound = errorx.Define(errorx.NotFound, "article.not_found")
	// ErrSlugTaken 表示 slug 已被其他文章使用。
	ErrSlugTaken = errorx.Define(errorx.Conflict, "article.slug_taken")
	errInternal  = errorx.Define(errorx.Internal, "article.internal")
)

// Changes 是创建和修改文章时提交的内容。
type Changes struct {
	// Title 是标题。
	Title string `json:"title" minLength:"1" maxLength:"200"`
	Slug  string `json:"slug" pattern:"^[a-z0-9]+(-[a-z0-9]+)*$"`
	// Tags 是标签，可以为空。
	Tags []string `json:"tags,omitempty" required:"false"`

	Draft bool   `json:"-"`
	notes string // 未导出的字段不复制
}

// Notes 返回备注，让未导出的字段有用处。
func (c Changes) Notes() string { return c.notes }

// ArticleDTO 是返回给客户端的文章。
type ArticleDTO struct {
	ID          uuid.UUID      `json:"id"`
	Title       string         `json:"title"`
	Slug        string         `json:"slug"`
	Status      Status         `json:"status"`
	Tags        []string       `json:"tags"`
	Author      Author         `json:"author"`
	PublishedAt *time.Time     `json:"published_at,omitempty" doc:"发布时间，草稿没有这个字段"`
	Meta        map[string]any `json:"meta,omitempty"`
	Featured    bool           `json:"featured"`
}

// Author 是文章的作者。
type Author struct {
	Name string `json:"name"`
}

type ArticleOutput struct {
	Body ArticleDTO
}

// CreatedOutput 是创建文章的响应。
type CreatedOutput struct {
	Location string `header:"Location"`
	Body     ArticleDTO
}

type CreateInput struct {
	Body Changes
}

type IDInput struct {
	ID uuid.UUID `path:"id"`
}

type UpdateInput struct {
	ID   uuid.UUID `path:"id"`
	Body Changes
}

// PublishInput 是发布文章的参数。
type PublishInput struct {
	ID uuid.UUID `path:"id"`
	// IdempotencyKey 让重试不会重复发布。
	IdempotencyKey string `header:"Idempotency-Key"`
}

// ListInput 是文章列表的筛选条件。
type ListInput struct {
	page.OffsetParams
	Status Status   `query:"status" enum:"draft,published"`
	Tags   []string `query:"tag,explode"`
	Q      string   `query:"q"`
}

type ListOutput struct {
	Body page.Offset[ArticleDTO]
}

// FeatureInput 设置文章是否精选，请求体是匿名结构体。
type FeatureInput struct {
	ID   uuid.UUID `path:"id"`
	Body struct {
		Featured bool `json:"featured"`
	}
}

// EventsInput 订阅文章的变化。
type EventsInput struct {
	Tag string `query:"tag"`
}

// CoverInput 上传文章的封面和附图。
type CoverInput struct {
	ID      uuid.UUID `path:"id"`
	RawBody huma.MultipartFormFiles[struct {
		File   huma.FormFile   `form:"file" contentType:"image/png,image/jpeg" required:"true"`
		Extras []huma.FormFile `form:"extras"`
		Alt    string          `form:"alt"`
	}]
}

// Cover 是上传的封面。
type Cover struct {
	Filename    string   `json:"filename"`
	ContentType string   `json:"content_type"`
	Size        int64    `json:"size"`
	Alt         string   `json:"alt"`
	Extras      []string `json:"extras"`
}

type CoverOutput struct {
	Body Cover
}

type PingOutput struct {
	Body struct {
		OK bool `json:"ok"`
	}
}

// Store 把文章保存在内存中。
type Store struct {
	mu       sync.Mutex
	articles []ArticleDTO
	keys     map[string]bool
}

// NewStore 创建空的 Store。
func NewStore() *Store { return &Store{keys: map[string]bool{}} }

// Register 登记示例接口；auth 保护需要登录的接口。
func Register(api *rest.API, s *Store, auth func(*huma.Operation)) {
	op := func(id, method, path, summary string, protected bool) huma.Operation {
		o := huma.Operation{OperationID: id, Method: method, Path: path, Summary: summary}
		if protected {
			auth(&o)
		} else {
			rest.Public()(&o)
		}
		return o
	}
	create := op("article-create", http.MethodPost, "/articles", "创建文章", true)
	create.DefaultStatus = http.StatusCreated
	rest.Register(api, create, s.create)
	rest.Register(api, op("article-get", http.MethodGet, "/articles/{id}", "获取文章", false), s.get)
	rest.Register(api, op("article-update", http.MethodPut, "/articles/{id}", "修改文章", true), s.update)
	rest.Register(api, op("article-delete", http.MethodDelete, "/articles/{id}", "删除文章", true), s.delete)
	rest.Register(api, op("article-publish", http.MethodPost, "/articles/{id}/publish", "发布文章", true), s.publish)
	rest.Register(api, op("article-feature", http.MethodPut, "/articles/{id}/feature", "设置精选", true), s.feature)
	rest.Register(api, op("article-list", http.MethodGet, "/articles", "文章列表", false), s.list)
	rest.RegisterSSE(api, op("article-events", http.MethodGet, "/articles/events", "文章变化", true),
		map[string]any{"changed": ArticleDTO{}, "deleted": uuid.UUID{}, "message": ""}, s.events)
	rest.Register(api, op("article-cover", http.MethodPost, "/articles/{id}/cover", "上传封面", true), s.cover)
	rest.Register(api, op("ping", http.MethodGet, "/ping", "", false), ping)
}

func ping(context.Context, *struct{}) (*PingOutput, error) {
	out := &PingOutput{}
	out.Body.OK = true
	return out, nil
}

func (s *Store) create(_ context.Context, in *CreateInput) (*CreatedOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.find(in.Body.Slug) >= 0 {
		return nil, ErrSlugTaken.New(errorx.Public("slug", in.Body.Slug))
	}
	a := ArticleDTO{ID: uuid.New(), Title: in.Body.Title, Slug: in.Body.Slug, Status: StatusDraft,
		Tags: append([]string{}, in.Body.Tags...), Author: Author{Name: "alice"}}
	s.articles = append(s.articles, a)
	return &CreatedOutput{Location: "/articles/" + a.ID.String(), Body: a}, nil
}

func (s *Store) get(_ context.Context, in *IDInput) (*ArticleOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.index(in.ID)
	if err != nil {
		return nil, err
	}
	return &ArticleOutput{Body: s.articles[i]}, nil
}

func (s *Store) update(_ context.Context, in *UpdateInput) (*ArticleOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.index(in.ID)
	if err != nil {
		return nil, err
	}
	if j := s.find(in.Body.Slug); j >= 0 && j != i {
		return nil, ErrSlugTaken.New()
	}
	a := &s.articles[i]
	a.Title, a.Slug, a.Tags = in.Body.Title, in.Body.Slug, append([]string{}, in.Body.Tags...)
	return &ArticleOutput{Body: *a}, nil
}

func (s *Store) delete(_ context.Context, in *IDInput) (*struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.index(in.ID)
	if err != nil {
		return nil, err
	}
	s.articles = slices.Delete(s.articles, i, i+1)
	return &struct{}{}, nil
}

func (s *Store) publish(_ context.Context, in *PublishInput) (*ArticleOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.index(in.ID)
	if err != nil {
		return nil, err
	}
	a := &s.articles[i]
	if in.IdempotencyKey != "" && s.keys[in.IdempotencyKey] {
		return &ArticleOutput{Body: *a}, nil
	}
	s.keys[in.IdempotencyKey] = true
	if a.Status == StatusPublished {
		return nil, errInternal.New()
	}
	now := time.Date(2026, 11, 13, 8, 30, 0, 0, time.UTC)
	a.Status, a.PublishedAt = StatusPublished, &now
	return &ArticleOutput{Body: *a}, nil
}

func (s *Store) feature(_ context.Context, in *FeatureInput) (*ArticleOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.index(in.ID)
	if err != nil {
		return nil, err
	}
	s.articles[i].Featured = in.Body.Featured
	return &ArticleOutput{Body: s.articles[i]}, nil
}

func (s *Store) list(_ context.Context, in *ListInput) (*ListOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var matched []ArticleDTO
	for _, a := range s.articles {
		if in.Status != "" && a.Status != in.Status {
			continue
		}
		if in.Q != "" && !strings.Contains(a.Title, in.Q) {
			continue
		}
		if !hasAll(a.Tags, in.Tags) {
			continue
		}
		matched = append(matched, a)
	}
	from := min(int(in.Offset()), len(matched))
	to := min(from+int(in.Limit()), len(matched))
	return &ListOutput{Body: page.NewOffset(matched[from:to], in.OffsetParams, int64(len(matched)))}, nil
}

// events 发出固定的几条事件：Last-Event-ID 之前的跳过，future 是客户端不认识的事件，最后以 [DONE] 结束。
func (s *Store) events(_ context.Context, in *EventsInput) (*huma.StreamResponse, error) {
	if in.Tag == "missing" {
		return nil, ErrNotFound.New()
	}
	id := uuid.MustParse("0193a5c2-7d1e-7b3a-9f61-2c4e8d0a1b2c")
	events := []struct {
		name string
		data any
	}{
		{"changed", ArticleDTO{ID: id, Title: "Hello", Status: StatusPublished, Tags: []string{}}},
		{"future", map[string]int{"n": 1}},
		{"deleted", id},
		{"", "multi\nline"},
	}
	return rest.SSE(func(st *httpx.Stream) error {
		after := 0
		if v := st.LastEventID(); v != "" {
			after, _ = strconv.Atoi(v)
		}
		for i, ev := range events {
			if i+1 <= after {
				continue
			}
			if err := st.SendJSON(httpx.Event{Name: ev.name, ID: strconv.Itoa(i + 1)}, ev.data); err != nil {
				return err
			}
		}
		return st.Send(httpx.Event{}, "[DONE]")
	}), nil
}

func (s *Store) cover(_ context.Context, in *CoverInput) (*CoverOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.index(in.ID); err != nil {
		return nil, err
	}
	form := in.RawBody.Data()
	data, err := io.ReadAll(form.File)
	if err != nil {
		return nil, err
	}
	c := Cover{Filename: form.File.Filename, ContentType: form.File.ContentType, Size: int64(len(data)), Alt: form.Alt, Extras: []string{}}
	for _, f := range form.Extras {
		c.Extras = append(c.Extras, f.Filename)
	}
	return &CoverOutput{Body: c}, nil
}

func (s *Store) find(slug string) int {
	return slices.IndexFunc(s.articles, func(a ArticleDTO) bool { return a.Slug == slug })
}

func (s *Store) index(id uuid.UUID) (int, error) {
	if i := slices.IndexFunc(s.articles, func(a ArticleDTO) bool { return a.ID == id }); i >= 0 {
		return i, nil
	}
	return -1, ErrNotFound.New()
}

func hasAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}
