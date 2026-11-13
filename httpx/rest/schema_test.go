package rest

import (
	"log/slog"
	"net/url"
	"reflect"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/go-tenon/tenon/page"
)

func TestQualify(t *testing.T) {
	for part, want := range map[string]string{
		"github.com/acme/blog/article.Changes":          "ArticleChanges",
		"github.com/acme/blog/comment.Changes":          "CommentChanges",
		"github.com/acme/blog/article.ArticleDTO":       "ArticleDTO",
		"github.com/acme/blog/apitoken.APIToken":        "APIToken",
		"github.com/acme/blog/article_test.Item":        "ArticleItem",
		"github.com/acme/go-blog/v2.Item":               "GoblogItem",
		"github.com/acme/blog/article.changes":          "ArticleChanges",
		"main.Item":                                     "Item",
		"int":                                           "Int",
		"github.com/danielgtaylor/huma/v2.ErrorDetail":  "ErrorDetail",
		"github.com/go-tenon/tenon/page.Offset":         "Offset",
		"github.com/go-tenon/tenon/apitoken/jwt.Pair":   "Pair",
		"github.com/go-tenon/tenonx/article.ArticleDTO": "ArticleDTO",
		"github.com/go-tenon/tenonx/article.Changes":    "ArticleChanges",
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

type rejectInput struct {
	ID   string `path:"id"`
	Body struct {
		Reason string `json:"reason"`
	}
}

func TestSchemaNamesAnonymousBody(t *testing.T) {
	// 让本包的类型像应用的类型一样加包名。
	defer func(old []string) { bare = old }(bare)
	bare = bare[1:]
	n := &schemaNames{anon: map[reflect.Type]string{}}
	n.bodies(reflect.TypeFor[*rejectInput]())
	body := reflect.TypeFor[rejectInput]().Field(1).Type
	if got := n.name(body, "RejectInputBody"); got != "RestRejectInputBody" {
		t.Errorf("anonymous body = %q", got)
	}
	if got := n.name(reflect.TypeFor[struct{ X int }](), "otherHint"); got != "OtherHint" {
		t.Errorf("unregistered anonymous = %q", got)
	}
}
