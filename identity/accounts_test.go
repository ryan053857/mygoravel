package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ryan053857/mygoravel/identity"
)

func TestAccounts(t *testing.T) {
	accounts := identity.Accounts{
		"member": func(_ context.Context, sub identity.Subject) (identity.Subject, error) {
			sub.Permissions = []string{"articles:*"}
			return sub, nil
		},
		"staff": nil,
	}
	ctx := context.Background()
	if sub, err := accounts.Resolve(ctx, identity.Subject{Type: "member", ID: "1"}); err != nil || !sub.Can("articles:write") {
		t.Fatalf("member: %+v %v", sub, err)
	}
	if sub, err := accounts.Resolve(ctx, identity.Subject{Type: "staff", ID: "1"}); err != nil || sub.Ref() != (identity.Ref{Type: "staff", ID: "1"}) {
		t.Fatalf("staff without resolver: %+v %v", sub, err)
	}
	for _, typ := range []identity.SubjectType{"customer", ""} {
		if _, err := accounts.Resolve(ctx, identity.Subject{Type: typ, ID: "1"}); !errors.Is(err, identity.ErrUnauthenticated) {
			t.Errorf("%q: %v", typ, err)
		}
	}
	if _, err := identity.Accounts(nil).Resolve(ctx, identity.Subject{Type: "member", ID: "1"}); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Errorf("empty accounts: %v", err)
	}
	if accounts.Validate() != nil || identity.Accounts(nil).Validate() != nil {
		t.Error("valid accounts rejected")
	}
	if (identity.Accounts{"Member": nil}).Validate() == nil {
		t.Error("invalid account type accepted")
	}
}
