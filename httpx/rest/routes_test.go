package rest_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
)

func TestRoutesIncludesTypedNativeAndHiddenRoutesWithoutRuntime(t *testing.T) {
	api := rest.New(rest.Options{Mode: rest.ModeOpenAPI})
	factory := func() func(context.Context, *pingIn) (*pingOut, error) {
		t.Fatal("route inspection constructed a handler")
		return nil
	}
	validator := func(context.Context, string) (identity.Subject, error) {
		t.Fatal("route inspection validated a token")
		return identity.Subject{}, nil
	}
	// Register out of display order, including two methods on one path.
	create := huma.Operation{OperationID: "item-create", Method: http.MethodPost, Path: "/inventory/items", Summary: "创建条目"}
	rest.RequireBearer(api, validator, "items:write", "items:read")(&create)
	rest.Register(api, create, factory)
	get := huma.Operation{OperationID: "item-list", Method: http.MethodGet, Path: "/inventory/items", Summary: "查看条目列表\n支持分页"}
	rest.Public()(&get)
	rest.Register(api, get, factory)
	private := huma.Operation{OperationID: "item-private", Method: http.MethodGet, Path: "/inventory/private"}
	rest.RequireBearer(api, validator)(&private)
	rest.Register(api, private, factory)
	hidden := huma.Operation{OperationID: "item-hidden", Method: http.MethodGet, Path: "/inventory/hidden", Summary: "隐藏条目详情", Hidden: true}
	rest.Public()(&hidden)
	rest.Register(api, hidden, factory)

	native := func(http.ResponseWriter, *http.Request) {
		t.Fatal("route inspection invoked a native handler")
	}
	api.Router().Get("/inventory/assets/*", native)
	mounted := chi.NewRouter()
	mounted.Get("/ready", native)
	api.Router().Mount("/inventory/health", mounted)

	routes := httpx.Routes(api.Router())
	var inventory []httpx.Route
	for _, route := range routes {
		if strings.HasPrefix(route.Path, "/inventory/") {
			inventory = append(inventory, route)
		}
	}
	want := []httpx.Route{
		{Method: http.MethodGet, Path: "/inventory/assets/*", Access: "unknown"},
		{Method: http.MethodGet, Path: "/inventory/health/ready", Access: "unknown"},
		{Method: http.MethodGet, Path: "/inventory/hidden", OperationID: "item-hidden", Summary: "隐藏条目详情", Access: "public"},
		{Method: http.MethodGet, Path: "/inventory/items", OperationID: "item-list", Summary: "查看条目列表\n支持分页", Access: "public"},
		{Method: http.MethodPost, Path: "/inventory/items", OperationID: "item-create", Summary: "创建条目", Access: "bearer", Scopes: []string{"items:write", "items:read"}},
		{Method: http.MethodGet, Path: "/inventory/private", OperationID: "item-private", Access: "bearer"},
	}
	if !reflect.DeepEqual(inventory, want) {
		t.Fatalf("routes = %#v, want %#v", inventory, want)
	}
	for _, path := range []string{"/docs", "/openapi.json", "/openapi.yaml", "/openapi-3.0.json", "/openapi-3.0.yaml", "/schemas/{schema}"} {
		if !slices.ContainsFunc(routes, func(r httpx.Route) bool {
			return reflect.DeepEqual(r, httpx.Route{Method: http.MethodGet, Path: path, Access: httpx.AccessDocs})
		}) {
			t.Errorf("missing documentation endpoint %s", path)
		}
	}
	for range 10 {
		if !reflect.DeepEqual(httpx.Routes(api.Router()), routes) {
			t.Fatal("route ordering changed between snapshots")
		}
	}
	routes[0].Path = "changed"
	if httpx.Routes(api.Router())[0].Path == "changed" {
		t.Fatal("snapshot mutation changed the API")
	}
	for i, r := range routes {
		if r.OperationID == "item-create" {
			routes[i].Scopes[0] = "changed"
		}
	}
	if !slices.ContainsFunc(httpx.Routes(api.Router()), func(r httpx.Route) bool { return slices.Equal(r.Scopes, []string{"items:write", "items:read"}) }) {
		t.Fatal("scope mutation changed the API")
	}
}

func TestRoutesDoesNotInferNativeSecurity(t *testing.T) {
	api := rest.New(rest.Options{Mode: rest.ModeOpenAPI})
	api.OpenAPI().Security = []map[string][]string{{"api-key": {}}}
	huma.Register(api.API, huma.Operation{
		OperationID: "native", Method: http.MethodGet, Path: "/native",
	}, func(context.Context, *pingIn) (*pingOut, error) {
		t.Fatal("route inspection invoked a native Huma handler")
		return nil, nil
	})
	huma.Register(api.API, huma.Operation{
		OperationID: "native-security", Method: http.MethodGet, Path: "/native-security",
		Security: []map[string][]string{{"api-key": {}}},
	}, func(context.Context, *pingIn) (*pingOut, error) { return nil, nil })
	for _, path := range []string{"/native", "/native-security"} {
		if !slices.ContainsFunc(httpx.Routes(api.Router()), func(route httpx.Route) bool {
			return route.Path == path && route.OperationID != "" && route.Access == "unknown"
		}) {
			t.Errorf("route %s must retain its ID without inferring access", path)
		}
	}
}

func TestRoutesExpandsOpaqueMountMethodsWithoutInvokingHandler(t *testing.T) {
	for _, mount := range []string{"/opaque", "/opaque/"} {
		t.Run(mount, func(t *testing.T) {
			api := rest.New(rest.Options{Mode: rest.ModeOpenAPI})
			api.Router().Mount(mount, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("route inspection invoked the mounted handler")
			}))

			var got []httpx.Route
			for _, route := range httpx.Routes(api.Router()) {
				if strings.HasPrefix(route.Path, "/opaque") {
					got = append(got, route)
				}
			}
			var want []httpx.Route
			for _, method := range []string{
				http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead,
				http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut,
				"QUERY", http.MethodTrace,
			} {
				want = append(want, httpx.Route{Method: method, Path: "/opaque/*", Access: "unknown"})
			}
			// chi 隐藏 /opaque 和 /opaque/ 的挂载占位项，只展开通配路径上的方法。
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("mount routes = %#v, want %#v", got, want)
			}
		})
	}
}

func TestRoutesMarksOnlyHumaDocumentationEndpoints(t *testing.T) {
	api := rest.New(rest.Options{Mode: rest.ModeOpenAPI})
	// 与文档端点相似但由应用注册的路由不能被误标为 docs。
	api.Router().Get("/openapi", func(http.ResponseWriter, *http.Request) {})
	api.Router().Get("/docs/guide", func(http.ResponseWriter, *http.Request) {})
	for _, route := range httpx.Routes(api.Router()) {
		if (route.Path == "/openapi" || route.Path == "/docs/guide") && route.Access != httpx.AccessUnknown {
			t.Errorf("%s marked as %s", route.Path, route.Access)
		}
	}
}

// API 挂在上层 Router 的子路由中时，路由表给出完整路径，BasePath 写入 OpenAPI 的 servers。
func TestRoutesUnderMountedAPI(t *testing.T) {
	router := chi.NewRouter()
	var api *rest.API
	router.Route("/api/v1", func(r chi.Router) {
		api = rest.New(rest.Options{Mode: rest.ModeOpenAPI, Router: r, BasePath: "/api/v1"})
		op := huma.Operation{OperationID: "ping", Method: http.MethodGet, Path: "/ping"}
		rest.Public()(&op)
		rest.Register(api, op, func() func(context.Context, *pingIn) (*pingOut, error) { return nil })
	})
	router.Get("/health", func(http.ResponseWriter, *http.Request) {})
	routes := httpx.Routes(router)
	if !slices.ContainsFunc(routes, func(r httpx.Route) bool {
		return r.Path == "/api/v1/ping" && r.OperationID == "ping" && r.Access == httpx.AccessPublic
	}) || !slices.ContainsFunc(routes, func(r httpx.Route) bool {
		return r.Path == "/api/v1/openapi.json" && r.Access == httpx.AccessDocs
	}) || !slices.ContainsFunc(routes, func(r httpx.Route) bool {
		return r.Path == "/health" && r.Access == httpx.AccessUnknown
	}) {
		t.Fatalf("routes = %#v", routes)
	}
	if servers := api.OpenAPI().Servers; len(servers) != 1 || servers[0].URL != "/api/v1" {
		t.Fatalf("servers = %#v", servers)
	}
}

func TestDocsPageUsesBasePath(t *testing.T) {
	router := chi.NewRouter()
	router.Route("/api/v1", func(r chi.Router) {
		rest.New(rest.Options{Router: r, BasePath: "/api/v1"})
	})
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/docs", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "/api/v1/openapi") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"url":"/api/v1"`) {
		t.Fatalf("openapi status=%d", rr.Code)
	}
}
