package mcpx_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-tenon/tenon/mcpx"
)

func TestResource(t *testing.T) {
	for url, want := range map[string]string{
		"https://blog.example.com/mcp":     "https://blog.example.com/.well-known/oauth-protected-resource/mcp",
		"https://blog.example.com/mcp/":    "https://blog.example.com/.well-known/oauth-protected-resource/mcp",
		"https://blog.example.com":         "https://blog.example.com/.well-known/oauth-protected-resource",
		"https://blog.example.com/api/mcp": "https://blog.example.com/.well-known/oauth-protected-resource/api/mcp",
	} {
		if got := (mcpx.Resource{URL: url}).MetadataURL(); got != want {
			t.Errorf("%s: %s, want %s", url, got, want)
		}
	}
	res := mcpx.Resource{URL: "https://blog.example.com/mcp", AuthorizationServers: []string{"https://blog.example.com"}, Scopes: []string{"articles:read"}}
	if res.MetadataPath() != "/.well-known/oauth-protected-resource/mcp" {
		t.Fatal(res.MetadataPath())
	}
	w := httptest.NewRecorder()
	mcpx.ResourceMetadata(res).ServeHTTP(w, httptest.NewRequest(http.MethodGet, res.MetadataPath(), nil))
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["resource"] != res.URL || w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("%s %v", w.Body, err)
	}
	for name, bad := range map[string]mcpx.Resource{
		"http":      {URL: "http://blog.example.com/mcp", AuthorizationServers: res.AuthorizationServers},
		"no server": {URL: res.URL},
		"query":     {URL: res.URL + "?x=1", AuthorizationServers: res.AuthorizationServers},
	} {
		if bad.Validate() == nil {
			t.Errorf("%s: valid", name)
		}
	}
	if (mcpx.Resource{URL: "http://localhost:8080/mcp", AuthorizationServers: res.AuthorizationServers}).Validate() != nil {
		t.Error("localhost rejected")
	}
}
