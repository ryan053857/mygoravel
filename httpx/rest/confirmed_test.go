package rest_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/go-tenon/tenon/httpx/rest"
	"github.com/go-tenon/tenon/identity"
)

// Confirmed 只能与认证方案一起使用，并在 OpenAPI 中记录有效期。行为见 apitoken 的 TestRequireConfirmed。
func TestConfirmedDeclaration(t *testing.T) {
	t.Parallel()
	api := rest.New(rest.Options{Accounts: identity.Accounts{"user": nil}})
	handler := func(context.Context, *struct{}) (*struct{}, error) { return nil, nil }

	public := huma.Operation{OperationID: "public-confirmed", Method: http.MethodPost, Path: "/public"}
	rest.Public()(&public)
	rest.Confirmed(time.Minute)(&public)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("Confirmed on a public endpoint did not panic")
			}
		}()
		rest.Register(api, public, handler)
	}()

	op := huma.Operation{OperationID: "account-delete", Method: http.MethodDelete, Path: "/account"}
	rest.RequireBearer(api, func(context.Context, string) (identity.Subject, error) { return identity.Subject{}, nil })(&op)
	rest.Confirmed(15 * time.Minute)(&op)
	rest.Register(api, op, handler)
	if got := api.OpenAPI().Paths["/account"].Delete.Extensions["x-confirm-max-age"]; got != 900 {
		t.Fatalf("x-confirm-max-age = %v", got)
	}

	defer func() {
		if recover() == nil {
			t.Error("Confirmed(0) did not panic")
		}
	}()
	rest.Confirmed(0)
}
