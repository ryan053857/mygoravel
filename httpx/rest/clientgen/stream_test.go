package clientgen_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/go-tenon/tenon/httpx/rest/clientgen/internal/example/api"
	"github.com/go-tenon/tenon/httpx/rest/clientgen/internal/example/api/article"
	"github.com/go-tenon/tenon/httpx/rest/clientgen/internal/example/api/identity"
	"github.com/go-tenon/tenon/httpx/rest/clientgen/internal/example/api/wire"
)

// collect 读取全部事件，返回事件 ID 和对应字段的描述。
func collect(t *testing.T, events func(func(api.ArticleEventsEvent, error) bool)) []string {
	t.Helper()
	var got []string
	for ev, err := range events {
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case ev.Changed != nil:
			got = append(got, ev.ID+" changed "+ev.Changed.Title)
		case ev.Deleted != nil:
			got = append(got, ev.ID+" deleted "+ev.Deleted.String()[:8])
		case ev.Message != nil:
			got = append(got, ev.ID+" message "+strings.ReplaceAll(*ev.Message, "\n", "|"))
		default:
			t.Fatalf("empty event %+v", ev)
		}
	}
	return got
}

func TestEvents(t *testing.T) {
	ctx := context.Background()
	base := serve(t, tokens{"secret": true})
	c := api.New(api.Options{BaseURL: base, Auth: wire.StaticToken("secret")})

	// future 是客户端不认识的事件，被跳过；[DONE] 结束迭代。
	want := []string{"1 changed Hello", "3 deleted 0193a5c2", "4 message multi|line"}
	if got := collect(t, c.Article.Events(ctx, article.EventsInput{})); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events %q", got)
	}
	resumed := collect(t, c.Article.Events(wire.WithLastEventID(ctx, "3"), article.EventsInput{}))
	if strings.Join(resumed, ",") != "4 message multi|line" {
		t.Fatalf("resumed %q", resumed)
	}

	n := 0
	for _, err := range c.Article.Events(ctx, article.EventsInput{Tag: "missing"}) {
		n++
		if !errors.Is(err, article.ErrNotFound) {
			t.Fatalf("error before the stream: %v", err)
		}
	}
	if n != 1 {
		t.Fatalf("got %d elements for a failed request", n)
	}
	for _, err := range api.New(api.Options{BaseURL: base}).Article.Events(ctx, article.EventsInput{}) {
		if !errors.Is(err, identity.ErrUnauthenticated) {
			t.Fatalf("without token: %v", err)
		}
	}

	auth := &rotating{current: "stale", next: "secret"}
	for ev, err := range api.New(api.Options{BaseURL: base, Auth: auth}).Article.Events(ctx, article.EventsInput{}) {
		if err != nil || ev.Changed == nil {
			t.Fatal(ev, err)
		}
		break // 提前结束迭代
	}
	if auth.refreshes.Load() != 1 {
		t.Fatalf("refreshed %d times", auth.refreshes.Load())
	}
}

func TestUpload(t *testing.T) {
	ctx := context.Background()
	base := serve(t, tokens{"secret": true})
	c := api.New(api.Options{BaseURL: base, Auth: wire.StaticToken("secret")})
	a, err := c.Article.Create(ctx, article.Changes{Title: "Hello", Slug: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	id := a.Body.ID
	png := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 1<<14)
	form := func(body io.Reader) article.CoverInputForm {
		return article.CoverInputForm{
			File:   wire.File{Name: `co"ver.png`, ContentType: "image/png", Body: body},
			Extras: []wire.File{{Name: "b.jpg", ContentType: "image/jpeg", Body: strings.NewReader("x")}, {Name: "c.jpg", Body: nil}},
			Alt:    "封面",
		}
	}

	cover, err := c.Article.Cover(ctx, id, form(bytes.NewReader(png)))
	if err != nil {
		t.Fatal(err)
	}
	if cover.Filename != `co"ver.png` || cover.ContentType != "image/png" || cover.Size != int64(len(png)) ||
		cover.Alt != "封面" || strings.Join(cover.Extras, ",") != "b.jpg" {
		t.Fatalf("%+v", cover)
	}

	var apiErr *wire.Error
	wrongType := form(strings.NewReader("text"))
	wrongType.File.ContentType = "text/plain"
	if _, err := c.Article.Cover(ctx, id, wrongType); !errors.As(err, &apiErr) || apiErr.Status != 422 {
		t.Fatalf("wrong content type: %v", err)
	}
	if _, err := c.Article.Cover(ctx, id, article.CoverInputForm{}); !errors.As(err, &apiErr) || apiErr.Status != 422 {
		t.Fatalf("missing file: %v", err)
	}

	// 文件内容可以 Seek：刷新凭据后从头重传。
	auth := &rotating{current: "stale", next: "secret"}
	cover, err = api.New(api.Options{BaseURL: base, Auth: auth}).Article.Cover(ctx, id, form(bytes.NewReader(png)))
	if err != nil || cover.Size != int64(len(png)) || auth.refreshes.Load() != 1 {
		t.Fatal(cover, err, auth.refreshes.Load())
	}

	// 不能 Seek：凭据已经刷新，但请求不重传，调用方再调用一次即可。
	auth = &rotating{current: "stale", next: "secret"}
	once := api.New(api.Options{BaseURL: base, Auth: auth})
	if _, err := once.Article.Cover(ctx, id, form(io.MultiReader(bytes.NewReader(png)))); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("non-seekable upload: %v", err)
	}
	if cover, err = once.Article.Cover(ctx, id, form(io.MultiReader(bytes.NewReader(png)))); err != nil || auth.refreshes.Load() != 1 {
		t.Fatal(cover, err, auth.refreshes.Load())
	}
}
