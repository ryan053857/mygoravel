// Package user 只声明第三方登录用到的部分，账号关联的写法见第三方登录一章。
package user

import (
	"context"

	"github.com/uptrace/bun"
)

type User struct {
	ID     string
	Scopes []string
}

// ExternalAccount 是第三方返回的账号信息。
type ExternalAccount struct {
	Provider      string
	ProviderID    string
	Email         string
	EmailVerified bool
	Name          string
}

// SignInWith 按第三方账号找到或创建用户。
type SignInWith struct {
	db *bun.DB
}

func NewSignInWith(db *bun.DB) *SignInWith { return &SignInWith{db: db} }

func (a *SignInWith) Handle(ctx context.Context, acc ExternalAccount) (*User, error) {
	return &User{ID: "u1"}, nil
}
