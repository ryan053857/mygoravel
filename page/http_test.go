package page_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/page"
)

type itemDTO struct {
	ID int `json:"id"`
}

type listOffsetIn struct {
	page.OffsetParams
}
type listOffsetOut struct {
	Body page.Offset[itemDTO]
}

type listCursorIn struct {
	page.CursorParams
}
type listCursorOut struct {
	Body page.Cursor[itemDTO]
}

func newAPI() *rest.API {
	all := []itemDTO{{1}, {2}, {3}, {4}, {5}}
	api := rest.New(rest.Options{Title: "page"})
	rest.Register(api, huma.Operation{Security: []map[string][]string{},
		OperationID: "list-offset", Method: http.MethodGet, Path: "/offset",
	}, func(_ context.Context, in *listOffsetIn) (*listOffsetOut, error) {
		lo := min(in.Offset(), len(all))
		hi := min(lo+in.Limit(), len(all))
		return &listOffsetOut{Body: page.NewOffset(all[lo:hi], in.OffsetParams, len(all))}, nil
	})
	rest.Register(api, huma.Operation{Security: []map[string][]string{},
		OperationID: "list-cursor", Method: http.MethodGet, Path: "/cursor",
	}, func(_ context.Context, in *listCursorIn) (*listCursorOut, error) {
		var after key
		ok, err := in.After(&after)
		if err != nil {
			return nil, err
		}
		tail := all
		if ok {
			tail = all[after.ID:]
		}
		tail = tail[:min(in.Limit(), len(tail))]
		return &listCursorOut{Body: page.NewCursor(tail, in.CursorParams, func(d itemDTO) any { return key{d.ID} })}, nil
	})
	return api
}

func get(t *testing.T, h http.Handler, url string, v any) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if v != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code
}

func TestOffsetOverHTTP(t *testing.T) {
	h := newAPI().Handler()
	var body page.Offset[itemDTO]
	if code := get(t, h, "/offset?page=2&per_page=2", &body); code != 200 {
		t.Fatalf("status=%d", code)
	}
	if body.Page != 2 || body.PerPage != 2 || body.Total != 5 || len(body.Items) != 2 || body.Items[0].ID != 3 {
		t.Fatalf("%+v", body)
	}
	if code := get(t, h, "/offset", &body); code != 200 || body.Page != 1 || body.PerPage != 20 {
		t.Fatalf("defaults: status=%d %+v", code, body)
	}
	for _, q := range []string{"page=0", "per_page=0", "per_page=101"} {
		if code := get(t, h, "/offset?"+q, nil); code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status=%d", q, code)
		}
	}
}

func TestCursorOverHTTP(t *testing.T) {
	h := newAPI().Handler()
	var ids []int
	url := "/cursor?per_page=2"
	for range 10 {
		var body page.Cursor[itemDTO]
		if code := get(t, h, url, &body); code != 200 {
			t.Fatalf("status=%d", code)
		}
		for _, it := range body.Items {
			ids = append(ids, it.ID)
		}
		if body.NextCursor == "" {
			break
		}
		url = "/cursor?per_page=2&cursor=" + body.NextCursor
	}
	if len(ids) != 5 || ids[4] != 5 {
		t.Fatalf("ids=%v", ids)
	}
	if code := get(t, h, "/cursor?cursor=***", nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid cursor status=%d", code)
	}
}

func TestOpenAPISchemaNames(t *testing.T) {
	doc, err := newAPI().OpenAPIJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"OffsetItemDTO"`, `"CursorItemDTO"`, `"per_page"`, `"next_cursor"`} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("openapi missing %s", want)
		}
	}
}
