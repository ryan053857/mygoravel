package webhook_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/go-tenon/tenon/webhook"
)

type publishedHook struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	PublishedAt time.Time `json:"published_at"`
}

func TestDocument(t *testing.T) {
	t.Parallel()
	doc := huma.DefaultConfig("t", "1").OpenAPI
	webhook.Document(doc,
		webhook.Event[publishedHook]{Name: "article.published", Summary: "文章发布后推送"},
		webhook.Event[struct {
			ID string `json:"id"`
		}]{Name: "comment.created"},
	)
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Webhooks map[string]struct {
			Post struct {
				Summary     string `json:"summary"`
				Parameters  []struct{ Name, In string }
				RequestBody struct {
					Content map[string]struct {
						Schema struct {
							Required   []string                   `json:"required"`
							Properties map[string]json.RawMessage `json:"properties"`
						} `json:"schema"`
					} `json:"content"`
				} `json:"requestBody"`
				Responses map[string]any `json:"responses"`
			} `json:"post"`
		} `json:"webhooks"`
		Components struct {
			Schemas map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	post := got.Webhooks["article.published"].Post
	if post.Summary != "文章发布后推送" || len(post.Parameters) != 3 || post.Parameters[2].Name != "webhook-signature" || post.Responses["2XX"] == nil {
		t.Fatalf("post: %+v", post)
	}
	schema := post.RequestBody.Content["application/json"].Schema
	if strings.Join(schema.Required, ",") != "type,timestamp,data" ||
		!strings.Contains(string(schema.Properties["type"]), `"enum":["article.published"]`) ||
		string(schema.Properties["data"]) != `{"$ref":"#/components/schemas/PublishedHook"}` {
		t.Fatalf("schema: %+v", schema.Properties)
	}
	anon := got.Webhooks["comment.created"].Post.RequestBody.Content["application/json"].Schema.Properties["data"]
	if string(anon) != `{"$ref":"#/components/schemas/CommentCreatedData"}` {
		t.Fatalf("anonymous data: %s", anon)
	}
	if got.Components.Schemas["PublishedHook"] == nil || got.Components.Schemas["CommentCreatedData"] == nil {
		t.Fatalf("schemas: %v", got.Components.Schemas)
	}
}

func TestDocumentRejectsInvalidAndDuplicateEvents(t *testing.T) {
	t.Parallel()
	for _, events := range [][]webhook.Documented{
		{webhook.Event[any]{Name: ""}},
		{webhook.Event[any]{Name: "article.*"}},
		{webhook.Event[any]{Name: "a.b"}, webhook.Event[int]{Name: "a.b"}},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("accepted %v", events)
				}
			}()
			webhook.Document(huma.DefaultConfig("t", "1").OpenAPI, events...)
		}()
	}
}
