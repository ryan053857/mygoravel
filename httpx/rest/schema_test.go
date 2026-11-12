package rest

import (
	"log/slog"
	"net/url"
	"reflect"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/page"
)

func TestQualify(t *testing.T) {
	for part, want := range map[string]string{
		"github.com/acme/blog/article.Changes":                "ArticleChanges",
		"github.com/acme/blog/comment.Changes":                "CommentChanges",
		"github.com/acme/blog/article.ArticleDTO":             "ArticleDTO",
		"github.com/acme/blog/apitoken.APIToken":              "APIToken",
		"github.com/acme/blog/article_test.Item":              "ArticleItem",
		"github.com/acme/go-blog/v2.Item":                     "GoblogItem",
		"github.com/acme/blog/article.changes":                "ArticleChanges",
		"main.Item":                                           "Item",
		"int":                                                 "Int",
		"github.com/danielgtaylor/huma/v2.ErrorDetail":        "ErrorDetail",
		"github.com/ryan053857/mygoravel/page.Offset":         "Offset",
		"github.com/ryan053857/mygoravel/apitoken/jwt.Pair":   "Pair",
		"github.com/ryan053857/mygoravelx/article.ArticleDTO": "ArticleDTO",
		"github.com/ryan053857/mygoravelx/article.Changes":    "ArticleChanges",
	} {
		if got := qualify(part); got != want {
			t.Errorf("qualify(%q) = %q, want %q", part, got, want)
		}
	}
}

func TestSchemaName(t *testing.T) {
	for _, c := range []struct {
		typ  any
		hint string
		want string
	}{
		{slog.Level(0), "", "SlogLevel"},
		{&url.URL{}, "", "URL"},
		{page.Offset[slog.Level]{}, "", "OffsetSlogLevel"},
		{ErrorModel{}, "", "ErrorModel"},
		{huma.ErrorDetail{}, "", "ErrorDetail"},
		{struct{}{}, "publishArticleRequest", "PublishArticleRequest"},
	} {
		if got := schemaName(reflect.TypeOf(c.typ), c.hint); got != c.want {
			t.Errorf("schemaName(%T) = %q, want %q", c.typ, got, c.want)
		}
	}
}
