package oauthserver

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/go-chi/chi/v5"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/apitoken"
	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/logging"
	"github.com/ryan053857/mygoravel/mcpx"
	"github.com/ryan053857/mygoravel/outbound"
)

// 授权请求本身有问题、不能跳回客户端时的错误：授权端点把错误码交给同意页（ConsentURL?error=…），同意页的接口返回 422。
var (
	// ErrInvalidClient 表示 client_id 不是预先登记的客户端，也取不回有效的客户端元数据文档。
	ErrInvalidClient = errorx.Define(errorx.InvalidArgument, "oauth.invalid_client")
	// ErrInvalidRedirect 表示 redirect_uri 缺失或不是客户端登记的回调地址。
	ErrInvalidRedirect = errorx.Define(errorx.InvalidArgument, "oauth.invalid_redirect_uri")
)

// 端点的路径，相对于挂载的路由。
const (
	MetadataPath  = "/.well-known/oauth-authorization-server"
	AuthorizePath = "/oauth/authorize"
	TokenPath     = "/oauth/token"
	RevokePath    = "/oauth/revoke"
)

// 默认值。
const (
	DefaultCodeTTL          = 10 * time.Minute
	DefaultTokenLifetime    = 365 * 24 * time.Hour
	DefaultTokenIdleTimeout = 30 * 24 * time.Hour
	// MetadataTimeout 是 Options.HTTPClient 为 nil 时取回客户端元数据文档的超时。
	MetadataTimeout = 5 * time.Second
)

// Scope 是客户端可以申请的一个范围，通常是权限目录中的一项权限，Description 显示在同意页上。
type Scope struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Options 是 New 的选项。
type Options struct {
	// Issuer 是授权服务器的标识，也是各端点的地址前缀，例如 https://blog.example.com。
	// 必须是没有路径的 https 地址（本机开发可以用 http://localhost）；Routes 挂在这个地址的根上。
	Issuer string
	// Resources 是令牌可以用于的资源，例如 MCP 服务的地址 https://blog.example.com/mcp，至少一个。
	// 授权请求的 resource 参数必须是其中之一；只有一个时可以省略。
	Resources []string
	// Scopes 是客户端可以申请的范围，至少一个。客户端不传 scope 时申请全部范围。
	Scopes []Scope
	// Clients 是预先登记的客户端。https 地址形式的 client_id 不需要登记，按 Client ID Metadata Document 取回。
	Clients []Client
	// ConsentURL 是单页应用中同意页的地址，必须设置，例如 /oauth/consent，也可以是完整的 https 地址。
	// 授权端点校验请求后跳到这里，附上 request 参数；请求无法跳回客户端时附上 error 参数（错误码，例如 oauth.invalid_client）。
	// 同意页用 Register 注册的接口读取请求、提交用户的选择。
	ConsentURL string
	// DynamicRegistration 开启动态客户端注册（RFC 7591）：客户端在 POST /oauth/register 登记自己，得到 client_id。
	// MCP 规范已把它标为过时，claude.ai 和 ChatGPT 都优先使用 Client ID Metadata Document，只在授权服务器不支持时才注册；
	// 需要兼容只支持注册的客户端时开启。注册接口不需要认证，应当为它限流，并定时调用 CleanupClients。
	DynamicRegistration bool
	// TokenLifetime 是令牌的最长有效期，TokenIdleTimeout 是最长空闲时间，到期后客户端重新授权。
	// 不大于 0 时分别为 DefaultTokenLifetime、DefaultTokenIdleTimeout。
	TokenLifetime    time.Duration
	TokenIdleTimeout time.Duration
	// HTTPClient 取回客户端元数据文档，nil 时使用 outbound.New 创建的客户端（拒绝内网地址），超时为 MetadataTimeout。
	HTTPClient *http.Client
	// Logger 记录签发令牌和取回元数据失败，nil 表示 slog.Default()。
	Logger *slog.Logger
}

// Server 是 OAuth 2.1 授权服务器：用户在单页应用的同意页上同意后，为 OAuth 客户端签发 apitoken 的 OAuth 令牌。可以并发使用。
type Server struct {
	db       bun.IDB
	tokens   *apitoken.Store
	opts     Options
	issuer   string
	scopes   map[string]Scope
	clients  map[string]Client
	http     *http.Client
	logger   *slog.Logger
	metadata []byte
}

// New 创建授权服务器，db 必须已执行 Migrations 的迁移，tokens 签发和撤销令牌。选项不合法时 panic。
func New(db bun.IDB, tokens *apitoken.Store, opts Options) *Server {
	if db == nil || tokens == nil {
		panic("oauthserver: nil database or token store")
	}
	if err := validate(opts); err != nil {
		panic(errors.Wrap(err, "oauthserver"))
	}
	s := &Server{
		db: db, tokens: tokens, opts: opts,
		issuer:  strings.TrimSuffix(opts.Issuer, "/"),
		scopes:  map[string]Scope{},
		clients: map[string]Client{},
		http:    opts.HTTPClient,
		logger:  logging.Or(opts.Logger).With(logging.AttrComponent, "oauth"),
	}
	for _, sc := range opts.Scopes {
		s.scopes[sc.Name] = sc
	}
	for _, c := range opts.Clients {
		s.clients[c.ID] = c
	}
	if s.http == nil {
		s.http = outbound.New(outbound.Options{Timeout: MetadataTimeout})
	}
	if s.opts.TokenLifetime <= 0 {
		s.opts.TokenLifetime = DefaultTokenLifetime
	}
	if s.opts.TokenIdleTimeout <= 0 {
		s.opts.TokenIdleTimeout = DefaultTokenIdleTimeout
	}
	s.metadata = s.encodeMetadata()
	return s
}

// consentURL 报告 raw 是本站的路径或 https 地址（本机开发可以用 http://localhost）。
func consentURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || raw == "" || u.Fragment != "" {
		return false
	}
	if u.Scheme == "" && u.Host == "" {
		return strings.HasPrefix(u.Path, "/") && !strings.HasPrefix(u.Path, "//")
	}
	return u.Host != "" && (u.Scheme == "https" || u.Scheme == "http" && loopback(u.Hostname()))
}

func validate(opts Options) error {
	u, err := url.Parse(opts.Issuer)
	switch {
	case err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || strings.TrimSuffix(u.Path, "/") != "":
		return errors.Newf("issuer %q must be an origin such as https://blog.example.com", opts.Issuer)
	case u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname())):
		return errors.Newf("issuer %q must use https", opts.Issuer)
	case len(opts.Resources) == 0:
		return errors.New("at least one resource is required")
	case len(opts.Scopes) == 0:
		return errors.New("at least one scope is required")
	case !consentURL(opts.ConsentURL):
		return errors.Newf("consent URL %q must be a path such as /oauth/consent or an https URL", opts.ConsentURL)
	}
	for _, r := range opts.Resources {
		if err := (mcpx.Resource{URL: r, AuthorizationServers: []string{opts.Issuer}}).Validate(); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, sc := range opts.Scopes {
		if sc.Name == "" || strings.ContainsAny(sc.Name, " \t\r\n\"\\") || seen[sc.Name] {
			return errors.Newf("invalid or duplicate scope %q", sc.Name)
		}
		seen[sc.Name] = true
	}
	for _, c := range opts.Clients {
		if c.ID == "" || metadataURL(c.ID) || strings.HasPrefix(c.ID, registeredPrefix) || len(c.RedirectURIs) == 0 {
			return errors.Newf("pre-registered client %q needs a non-URL ID and redirect URIs", c.ID)
		}
	}
	return nil
}

// Routes 注册授权服务器的端点。r 是挂在 Issuer 根上的路由。同意页的接口由 Register 注册在 API 上。
//
//	GET  /.well-known/oauth-authorization-server  元数据（RFC 8414）
//	GET  /oauth/authorize                         校验授权请求，跳到 Options.ConsentURL
//	POST /oauth/token                             授权码换令牌
//	POST /oauth/revoke                            撤销令牌（RFC 7009）
//	POST /oauth/register                          动态客户端注册（RFC 7591），Options.DynamicRegistration 为 true 时
func (s *Server) Routes(r chi.Router) {
	cors := httpx.CORS(httpx.DefaultCORSConfig())
	r.With(cors).Get(MetadataPath, s.serveMetadata)
	r.Get(AuthorizePath, s.authorize)
	r.With(cors).Post(TokenPath, s.token)
	r.With(cors).Post(RevokePath, s.revoke)
	r.With(cors).Options(TokenPath, func(http.ResponseWriter, *http.Request) {})
	r.With(cors).Options(RevokePath, func(http.ResponseWriter, *http.Request) {})
	if s.opts.DynamicRegistration {
		r.With(cors).Post(RegisterPath, s.register)
		r.With(cors).Options(RegisterPath, func(http.ResponseWriter, *http.Request) {})
	}
}

// Resource 返回 resource 作为受保护资源的信息，交给 mcpx.HandlerOptions.Resource 和 mcpx.ResourceMetadata。
// resource 不在 Options.Resources 中时 panic。
func (s *Server) Resource(resource string) mcpx.Resource {
	for _, r := range s.opts.Resources {
		if r == resource {
			return mcpx.Resource{URL: resource, AuthorizationServers: []string{s.issuer}, Scopes: s.scopeNames()}
		}
	}
	panic("oauthserver: unknown resource " + resource)
}

func (s *Server) scopeNames() []string {
	names := make([]string, len(s.opts.Scopes))
	for i, sc := range s.opts.Scopes {
		names[i] = sc.Name
	}
	return names
}

func (s *Server) encodeMetadata() []byte {
	meta := map[string]any{
		"issuer":                                         s.issuer,
		"authorization_endpoint":                         s.issuer + AuthorizePath,
		"token_endpoint":                                 s.issuer + TokenPath,
		"revocation_endpoint":                            s.issuer + RevokePath,
		"scopes_supported":                               s.scopeNames(),
		"response_types_supported":                       []string{"code"},
		"response_modes_supported":                       []string{"query"},
		"grant_types_supported":                          []string{"authorization_code"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"revocation_endpoint_auth_methods_supported":     []string{"none"},
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
	}
	if s.opts.DynamicRegistration {
		meta["registration_endpoint"] = s.issuer + RegisterPath
	}
	data, err := json.Marshal(meta)
	if err != nil {
		panic(errors.Wrap(err, "oauthserver: encode metadata"))
	}
	return data
}

func (s *Server) serveMetadata(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(s.metadata)
}

// Cleanup 删除最多 limit 个过期的授权码，返回删除的个数。由应用定时调用。
func (s *Server) Cleanup(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, errors.New("oauthserver: cleanup limit must be positive")
	}
	expired := s.db.NewSelect().Model((*code)(nil)).Column("code_hash").
		Where("expires_at <= ?", time.Now().UTC()).OrderExpr("expires_at").Limit(int64(limit))
	res, err := s.db.NewDelete().Model((*code)(nil)).Where("code_hash IN (?)", expired).Exec(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "oauthserver: cleanup")
	}
	n, err := res.RowsAffected()
	return int(n), errors.Wrap(err, "oauthserver: cleanup")
}

//go:embed migrations
var migrationFiles embed.FS

// Migrations 返回授权码表 oauth_codes 和动态注册客户端表 oauth_clients 的迁移，供应用发布。令牌保存在 apitoken 的表中。
func Migrations() fs.FS {
	f, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err)
	}
	return f
}
