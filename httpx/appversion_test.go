package httpx_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/go-tenon/tenon/httpx"
)

func appVersionConfig() httpx.AppVersionConfig {
	return httpx.AppVersionConfig{Minimum: map[string]string{"ios": "1.4", "android": "2.0.0"}}
}

func TestAppVersionComparesPerPlatform(t *testing.T) {
	t.Parallel()
	h := httpx.AppVersion(appVersionConfig())(&marker{})
	for _, tc := range []struct {
		header string
		want   int
	}{
		{"", 200},                 // 浏览器、第三方调用方
		{"macos/0.1.0", 200},      // 没有设置最低版本的平台
		{"ios/1.4.0", 200},        // 1.4 等于 1.4.0
		{"ios/1.10.0", 200},       // 按数字而不是字符串比较
		{"ios/1.4.0+99", 200},     // 构建号不参与比较
		{"ios/2", 200},            // 省略次版本号和修订号
		{"ios/1.3.9", 426},        // 低于最低版本
		{"iOS/1.3.9", 426},        // 平台不区分大小写
		{"ios/1.4.0-beta.1", 426}, // 预发布版本低于正式版
		{"android/1.99.0", 426},
		{"ios", 400},
		{"ios/", 400},
		{"/1.4.0", 400},
		{"ios/v1.4.0", 400},
		{"ios/1.4.0.1", 400},
		{"ios/latest", 400},
		{"i os/1.4.0", 400},
		{"ios/1." + strings.Repeat("0", 80), 400},
	} {
		req := httptest.NewRequest("GET", "/", nil)
		if tc.header != "" {
			req.Header.Set(httpx.AppVersionHeader, tc.header)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != tc.want {
			t.Errorf("%q: status %d, want %d", tc.header, rr.Code, tc.want)
		}
		if rr.Code != 200 && rr.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%q: content type %q", tc.header, rr.Header().Get("Content-Type"))
		}
	}
}

func TestAppVersionRejectionIsLocalizedLoggedAndReadableCrossOrigin(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	cfg := httpx.DefaultConfig()
	cfg.AppVersion = appVersionConfig()
	router := chi.NewRouter()
	router.Use(httpx.Defaults(cfg, slog.New(slog.NewJSONHandler(&logs, nil)), nil)...)
	router.Route("/api", func(r chi.Router) {
		r.Use(httpx.CORS(cfg.CORS), httpx.AppVersion(cfg.AppVersion))
		r.Get("/articles", func(http.ResponseWriter, *http.Request) {})
	})

	send := func(version, lang string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/articles", nil)
		req.Header.Set("Origin", "capacitor://localhost")
		req.Header.Set("Accept-Language", lang)
		req.Header.Set(httpx.AppVersionHeader, version)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}

	rr := send("ios/1.3.0", "zh-CN")
	var body struct {
		Status int    `json:"status"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Code   string `json:"code"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err, rr.Body.String())
	}
	if rr.Code != http.StatusUpgradeRequired || body.Status != 426 || body.Title != "Upgrade Required" ||
		body.Code != httpx.CodeUpgradeRequired || body.Detail != "当前版本过低，请升级到 1.4 或更高版本" {
		t.Fatalf("status %d, body %+v", rr.Code, body)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Fatal("426 must carry CORS headers so the app can read the code")
	}
	if !strings.Contains(logs.String(), `"app":"ios/1.3.0"`) || !strings.Contains(logs.String(), `"code":"app.upgrade_required"`) {
		t.Fatal("rejection must log app and code", logs.String())
	}

	if rr := send("ios/1.3.0", "en"); !strings.Contains(rr.Body.String(), "please update to 1.4 or later") {
		t.Fatal("detail must follow Accept-Language", rr.Body.String())
	}

	logs.Reset()
	if rr := send("Android/2.1.0", "en"); rr.Code != 200 || !strings.Contains(logs.String(), `"app":"android/2.1.0"`) {
		t.Fatal("accepted requests must log the normalized app version", rr.Code, logs.String())
	}
}

func TestAppVersionWithoutLocaleUsesStatusText(t *testing.T) {
	t.Parallel()
	h := httpx.AppVersion(appVersionConfig())(&marker{})
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(httpx.AppVersionHeader, "ios/1.0.0")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), `"detail":"Upgrade Required"`) {
		t.Fatal(rr.Body.String())
	}
}

func TestAppVersionConfigValidate(t *testing.T) {
	t.Parallel()
	if err := httpx.DefaultConfig().AppVersion.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, minimum := range []map[string]string{
		{"iOS": "1.0.0"},
		{"": "1.0.0"},
		{"ios": ""},
		{"ios": "v1.0.0"},
		{"ios": "1.0.0.0"},
	} {
		if err := (httpx.AppVersionConfig{Minimum: minimum}).Validate(); err == nil {
			t.Errorf("%v must be invalid", minimum)
		}
	}
	defer func() {
		if recover() == nil {
			t.Fatal("invalid config must panic")
		}
	}()
	httpx.AppVersion(httpx.AppVersionConfig{Minimum: map[string]string{"ios": "latest"}})
}
