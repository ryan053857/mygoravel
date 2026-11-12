// Package user 只声明文档中其他文件用到的部分：账号体系的名称见认证一章，账号关联的写法见第三方登录一章。
package user

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/identity"
)

// Type 是博客用户这套账号体系的名称，见认证一章的核心概念。
const Type identity.SubjectType = "user"

type User struct {
	ID          string
	Permissions []string
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
