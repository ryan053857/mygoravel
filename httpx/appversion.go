package httpx

import (
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/danielgtaylor/huma/v2"
	"golang.org/x/mod/semver"

	"github.com/ryan053857/mygoravel/i18n"
)

// AppVersionHeader 是 App 报告平台和版本的请求头，值为“平台/版本”，例如 ios/1.4.2。
const AppVersionHeader = "X-App-Version"

// CodeUpgradeRequired 是 App 版本低于最低版本时 426 响应的业务错误码。
const CodeUpgradeRequired = "app.upgrade_required"

// AppVersionConfig 是 AppVersion 的参数，对应 http.app_version 段。
type AppVersionConfig struct {
	// Minimum 是各平台允许的最低版本，例如 {"ios": "1.4.0", "android": "1.3.0"}。
	// 平台名是小写字母、数字、- 和 _；没有列出的平台不限制。
	Minimum map[string]string `yaml:"minimum"`
}

// DefaultAppVersionConfig 返回不限制任何平台的配置。
func DefaultAppVersionConfig() AppVersionConfig {
	return AppVersionConfig{Minimum: map[string]string{}}
}

// SetDefaults 实现 config.Defaulter。
func (c *AppVersionConfig) SetDefaults() { *c = DefaultAppVersionConfig() }

// Validate 检查平台名和版本号的格式。
func (c AppVersionConfig) Validate() error {
	for platform, version := range c.Minimum {
		if !validPlatform(platform) {
			return errors.Newf("minimum: platform %q must be lowercase letters, digits, - or _", platform)
		}
		if !validVersion(version) {
			return errors.Newf("minimum.%s: %q is not a version like 1.4.0", platform, version)
		}
	}
	return nil
}

// AppVersion 拒绝低于最低版本的 App 的请求。App 在每个请求中带上 X-App-Version: ios/1.4.2，
// 版本低于 cfg.Minimum 中该平台的版本时返回 426，code 为 app.upgrade_required，
// detail 按请求语言提示要升级到的版本；请求头格式错误时返回 400。
//
// 没有这个请求头的请求（浏览器、单页应用、第三方调用方）和没有设置最低版本的平台都放行。
// 带请求头的请求在访问日志中记录 app 字段，例如 ios/1.4.2。
//
// 版本按语义化版本比较：1.4 等于 1.4.0，1.4.0-beta.1 低于 1.4.0，+ 之后的构建号不参与比较。
// 安装在 CORS 之后，426 响应才带有 CORS 头；需要翻译时安装在 Locale 之后。cfg 不合法时 panic。
func AppVersion(cfg AppVersionConfig) func(http.Handler) http.Handler {
	mustValid("AppVersion", cfg.Validate())
	minimum := maps.Clone(cfg.Minimum)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			value := r.Header.Get(AppVersionHeader)
			if value == "" {
				next.ServeHTTP(w, r)
				return
			}
			platform, version, ok := parseAppVersion(value)
			if !ok {
				WriteProblem(w, http.StatusBadRequest, "invalid "+AppVersionHeader+" header, want platform/version such as ios/1.4.2")
				return
			}
			ctx := r.Context()
			AddAccessLogAttrs(ctx, slog.String("app", platform+"/"+version))
			if least, ok := minimum[platform]; ok && semver.Compare("v"+version, "v"+least) < 0 {
				AddAccessLogAttrs(ctx, slog.String("code", CodeUpgradeRequired))
				detail := i18n.T(ctx, CodeUpgradeRequired, map[string]string{"minimum": least})
				if detail == CodeUpgradeRequired { // 没有安装 Locale
					detail = http.StatusText(http.StatusUpgradeRequired)
				}
				writeUpgradeRequired(w, detail)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// parseAppVersion 把“平台/版本”拆开，平台转为小写。
func parseAppVersion(value string) (platform, version string, ok bool) {
	if len(value) > 64 {
		return "", "", false
	}
	platform, version, ok = strings.Cut(value, "/")
	platform = strings.ToLower(platform)
	if !ok || !validPlatform(platform) || !validVersion(version) {
		return "", "", false
	}
	return platform, version, true
}

func validPlatform(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, c := range []byte(s) {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// validVersion 接受不带 v 前缀的语义化版本，可以省略次版本号和修订号，例如 1、1.4、1.4.0-beta.1+45。
func validVersion(s string) bool {
	return s != "" && s[0] >= '0' && s[0] <= '9' && semver.IsValid("v"+s)
}

// writeUpgradeRequired 输出带 code 的 426 Problem Details，结构与 rest.ErrorModel 一致。
func writeUpgradeRequired(w http.ResponseWriter, detail string) {
	problem := struct {
		huma.ErrorModel
		Code string `json:"code"`
	}{
		ErrorModel: huma.ErrorModel{
			Title:  http.StatusText(http.StatusUpgradeRequired),
			Status: http.StatusUpgradeRequired,
			Detail: detail,
		},
		Code: CodeUpgradeRequired,
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusUpgradeRequired)
	_ = json.NewEncoder(w).Encode(problem)
}
