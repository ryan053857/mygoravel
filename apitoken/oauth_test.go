package apitoken_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-tenon/tenon/apitoken"
	"github.com/go-tenon/tenon/identity"
)

const mcpResource = "https://blog.example.com/mcp"

func grant() apitoken.Grant {
	return apitoken.Grant{
		ClientID: "https://claude.ai/oauth/claude-code-client-metadata", ClientName: "Claude Code",
		Resource: mcpResource, Scopes: []string{"articles:read"},
		Lifetime: 10 * time.Hour, IdleTimeout: time.Hour,
	}
}

func TestOAuthToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	acme := alice
	acme.Tenant = "acme"
	plain, err := s.Authorize(ctx, acme, grant())
	if err != nil {
		t.Fatal(err)
	}

	// 只能用于授权时的资源：REST 用的 Validate 和其他资源都不接受。
	if _, ok := valid(t, s, plain); ok {
		t.Fatal("Validate accepted an OAuth token")
	}
	if _, err := s.ValidateFor("https://other.example.com/mcp")(ctx, plain); !errors.Is(err, apitoken.ErrInvalid) {
		t.Fatalf("other resource: %v", err)
	}
	sub, err := s.ValidateFor(mcpResource)(ctx, plain)
	if err != nil {
		t.Fatal(err)
	}
	if sub.ID != "alice" || sub.Tenant != "acme" || sub.Method != "bearer" || !slices.Equal(sub.Scopes, []string{"articles:read"}) {
		t.Fatalf("subject %+v", sub)
	}

	// ValidateFor 照常接受登录 token 和个人访问令牌。
	if _, err := s.ValidateFor(mcpResource)(ctx, login(t, s, alice, apitoken.Device{})); err != nil {
		t.Fatalf("login token: %v", err)
	}
	pat := create(t, s, alice, apitoken.Personal{Name: "ci", Scopes: read})
	if sub, err := s.ValidateFor(mcpResource)(ctx, pat); err != nil || !slices.Equal(sub.Scopes, read) {
		t.Fatalf("personal token: %+v %v", sub, err)
	}

	// 出现在列表中，带着客户端和资源。
	list, err := s.List(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	var found apitoken.Token
	for _, tok := range list {
		if tok.Kind == apitoken.KindOAuth {
			found = tok
		}
	}
	if found.ClientName != "Claude Code" || found.Resource != mcpResource || found.ClientID == "" || found.Tenant != "acme" {
		t.Fatalf("listed %+v", found)
	}

	// “退出其他设备”不影响已连接的应用。
	if _, err := s.RevokeOthers(ctx, alice); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateFor(mcpResource)(ctx, plain); err != nil {
		t.Fatalf("after RevokeOthers: %v", err)
	}

	// 客户端交回令牌（RFC 7009）。不是 OAuth 令牌时什么也不做。
	if err := s.RevokeToken(ctx, pat); err != nil {
		t.Fatal(err)
	}
	if _, ok := valid(t, s, pat); !ok {
		t.Fatal("RevokeToken revoked a personal token")
	}
	if err := s.RevokeToken(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateFor(mcpResource)(ctx, plain); !errors.Is(err, apitoken.ErrInvalid) {
		t.Fatalf("after RevokeToken: %v", err)
	}
	if err := s.RevokeToken(ctx, "nonexistent"); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthTokenSlidingExpiry(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		s := newStore(t)
		validate := s.ValidateFor(mcpResource)
		plain, err := s.Authorize(ctx, alice, grant())
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Minute)
		if _, err := validate(ctx, plain); err != nil {
			t.Fatalf("used within idle timeout: %v", err)
		}
		time.Sleep(50 * time.Minute)
		if _, err := validate(ctx, plain); err != nil {
			t.Fatalf("use extends the token: %v", err)
		}
		time.Sleep(time.Hour)
		if _, err := validate(ctx, plain); !errors.Is(err, apitoken.ErrInvalid) {
			t.Fatalf("idle token: %v", err)
		}
	})
}

func TestAuthorizeValidation(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	for name, edit := range map[string]func(*apitoken.Grant){
		"no client":   func(g *apitoken.Grant) { g.ClientID = "" },
		"no resource": func(g *apitoken.Grant) { g.Resource = "" },
		"no scopes":   func(g *apitoken.Grant) { g.Scopes = nil },
		"bad scope":   func(g *apitoken.Grant) { g.Scopes = []string{"a b"} },
		"no lifetime": func(g *apitoken.Grant) { g.Lifetime = 0 },
	} {
		g := grant()
		edit(&g)
		if _, err := s.Authorize(context.Background(), alice, g); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := s.Authorize(context.Background(), identity.Subject{Type: "user"}, grant()); err == nil {
		t.Error("authorized an empty subject")
	}
}
