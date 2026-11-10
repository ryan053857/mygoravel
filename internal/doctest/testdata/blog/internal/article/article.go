// Package article 只声明 bootstrap 和 cli 用到的部分，完整代码见数据库、错误处理和授权章节。
package article

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/identity"
)

var ErrSlugTaken = errors.New("article: slug taken")

type Article struct {
	ID uuid.UUID
}

type Changes struct {
	Title, Slug, Content string
}

type Service struct {
	db *bun.DB
}

func NewService(db *bun.DB) *Service { return &Service{db: db} }

func (s *Service) Create(ctx context.Context, subject identity.Subject, c Changes) (*Article, error) {
	return &Article{}, nil
}

func (s *Service) Publish(ctx context.Context, subject identity.Subject, id uuid.UUID) (*Article, error) {
	return &Article{ID: id}, nil
}

func (s *Service) PublishScheduled(ctx context.Context) error { return nil }
