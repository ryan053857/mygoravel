package oauthserver

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
)

// RegisterPath 是动态客户端注册的端点，Options.DynamicRegistration 为 true 时注册。
const RegisterPath = "/oauth/register"

const (
	// registeredPrefix 是动态注册的客户端 ID 的前缀，与 https 地址形式的 client_id 区分开。
	registeredPrefix = "dcr_"
	maxRedirectURIs  = 10
	maxClientName    = 100
	maxRegistration  = 16 << 10
)

// registeredClient 是 oauth_clients 表中一个动态注册的客户端。
type registeredClient struct {
	bun.BaseModel `bun:"table:oauth_clients,alias:ocl"`

	ID           string    `bun:"id,pk"`
	Name         string    `bun:"name,notnull"`
	RedirectURIs []string  `bun:"redirect_uris,type:jsonb,notnull"`
	CreatedAt    time.Time `bun:"created_at,notnull"`
	LastUsedAt   time.Time `bun:"last_used_at,notnull"`
}

// registration 是注册请求（RFC 7591 2）中本包使用的字段。
type registration struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
}

// register 处理动态客户端注册（RFC 7591）：只登记公开客户端，回调地址必须是 https 或本机的 http 地址。
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var req registration
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRegistration)).Decode(&req); err != nil {
		writeRegistrationError(w, "invalid_client_metadata", "the request body must be a JSON object")
		return
	}
	if msg := checkRegistration(req); msg != "" {
		code := "invalid_client_metadata"
		if strings.HasPrefix(msg, "redirect") {
			code = "invalid_redirect_uri"
		}
		writeRegistrationError(w, code, msg)
		return
	}
	now := time.Now().UTC()
	c := &registeredClient{
		ID: registeredPrefix + randomID(), Name: strings.TrimSpace(req.ClientName), RedirectURIs: req.RedirectURIs,
		CreatedAt: now, LastUsedAt: now,
	}
	if c.Name == "" {
		c.Name = host(c.RedirectURIs[0])
	}
	if _, err := s.db.NewInsert().Model(c).Exec(r.Context()); err != nil {
		s.logger.ErrorContext(r.Context(), "oauth registration failed", "error", err)
		writeTokenError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	s.logger.InfoContext(r.Context(), "oauth client registered", "client_id", c.ID, "client_name", c.Name)
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  c.ID,
		"client_id_issued_at":        now.Unix(),
		"client_name":                c.Name,
		"redirect_uris":              c.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
	})
}

// checkRegistration 检查注册请求，返回不合格的原因；合格时返回空字符串。
func checkRegistration(req registration) string {
	switch {
	case len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > maxRedirectURIs:
		return "redirect_uris must have 1 to 10 entries"
	case req.TokenEndpointAuthMethod != "" && req.TokenEndpointAuthMethod != "none":
		return "only public clients are supported: token_endpoint_auth_method must be none"
	case len(req.GrantTypes) > 0 && !slices.Contains(req.GrantTypes, "authorization_code"):
		return "grant_types must include authorization_code"
	case len(req.ResponseTypes) > 0 && !slices.Contains(req.ResponseTypes, "code"):
		return "response_types must include code"
	case utf8.RuneCountInString(req.ClientName) > maxClientName || strings.ContainsAny(req.ClientName, "\r\n"):
		return "client_name must be a single line of at most 100 characters"
	}
	for _, raw := range req.RedirectURIs {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.Fragment != "" || u.User != nil ||
			(u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname()))) {
			return "redirect_uris must be https URLs, or http URLs on localhost"
		}
	}
	return ""
}

// registered 返回动态注册的客户端 id，并记录它被用于授权；不存在时返回 nil。
func (s *Server) registered(ctx context.Context, id string) (*client, error) {
	var c registeredClient
	err := s.db.NewUpdate().Model(&c).Set("last_used_at = ?", time.Now().UTC()).
		Where("id = ?", id).Returning("*").Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.Wrap(err, "load registered client")
	}
	return &client{id: c.ID, name: c.Name, redirectURIs: c.RedirectURIs}, nil
}

// CleanupClients 返回删除超过 keep 没有用于授权的动态注册客户端的清理函数，每次最多删除 limit 个，用于定时任务。
// 已签发的令牌不受影响；客户端之后再连接时重新注册。keep 不大于 0 时 panic。
func (s *Server) CleanupClients(keep time.Duration) func(ctx context.Context, limit int) (int, error) {
	if keep <= 0 {
		panic("oauthserver: keep must be positive")
	}
	return func(ctx context.Context, limit int) (int, error) {
		if limit <= 0 {
			return 0, errors.New("oauthserver: cleanup limit must be positive")
		}
		old := s.db.NewSelect().Model((*registeredClient)(nil)).Column("id").
			Where("last_used_at < ?", time.Now().Add(-keep).UTC()).OrderExpr("last_used_at").Limit(int64(limit))
		res, err := s.db.NewDelete().Model((*registeredClient)(nil)).Where("id IN (?)", old).Exec(ctx)
		if err != nil {
			return 0, errors.Wrap(err, "oauthserver: cleanup clients")
		}
		n, err := res.RowsAffected()
		return int(n), errors.Wrap(err, "oauthserver: cleanup clients")
	}
}

func writeRegistrationError(w http.ResponseWriter, code, description string) {
	writeTokenError(w, http.StatusBadRequest, code, description)
}

// randomID 生成 22 个字符的随机标识。
func randomID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
