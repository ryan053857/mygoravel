// Package user 只声明第三方登录用到的部分，账号关联的写法见第三方登录一章。
package user

import "context"

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

type Service struct{}

func (s *Service) SignInWith(ctx context.Context, acc ExternalAccount) (*User, error) {
	return &User{ID: "u1"}, nil
}
