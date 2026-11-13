// Package article 声明文档中只以片段给出的部分：模型、业务错误、共享函数，以及 Create、PublishScheduled。
// publish.go、get.go、routes.go、dto.go、import.go 和 export.go 从文档中提取。
package article

import (
	"context"
	"database/sql"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/go-tenon/tenon/errorx"
	"github.com/go-tenon/tenon/identity"
)

const (
	StatusDraft     = "draft"
	StatusPublished = "published"
)

type Article struct {
	ID          uuid.UUID  `bun:"id,pk"`
	AuthorID    string     `bun:"author_id,notnull"`
	Title       string     `bun:"title,notnull"`
	Slug        string     `bun:"slug,notnull"`
	Content     string     `bun:"content,notnull"`
	Status      string     `bun:"status,notnull"`
	PublishedAt *time.Time `bun:"published_at"`
}

var (
	ErrNotFound         = errorx.Define(errorx.NotFound, "article.not_found")
	ErrForbidden        = errorx.Define(errorx.PermissionDenied, "article.forbidden")
	ErrSlugTaken        = errorx.Define(errorx.Conflict, "article.slug_taken")
	ErrAlreadyPublished = errorx.Define(errorx.Conflict, "article.already_published")
)

type Changes struct {
	Title   string `json:"title" sheet:"标题" minLength:"1" maxLength:"200"`
	Slug    string `json:"slug" sheet:"Slug" maxLength:"100" pattern:"^[a-z0-9]+(-[a-z0-9]+)*$"`
	Content string `json:"content" sheet:"正文,内容"`
}

func find(ctx context.Context, db bun.IDB, id uuid.UUID) (*Article, error) {
	item := new(Article)
	err := db.NewSelect().Model(item).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound.New()
	}
	if err != nil {
		return nil, errors.Wrap(err, "load article")
	}
	return item, nil
}

func CanEdit(subject identity.Subject, item *Article) bool {
	return subject.Can("articles:admin") || item.AuthorID == subject.ID
}

type Create struct{ db *bun.DB }

func NewCreate(db *bun.DB) *Create { return &Create{db: db} }

func (a *Create) Handle(ctx context.Context, subject identity.Subject, c Changes) (*Article, error) {
	return &Article{}, nil
}

type CreateInput struct {
	Body Changes
}

func (a *Create) HTTP(ctx context.Context, in *CreateInput) (*ArticleOutput, error) {
	subject, _ := identity.FromContext(ctx)
	item, err := a.Handle(ctx, subject, in.Body)
	if err != nil {
		return nil, err
	}
	return &ArticleOutput{Body: toDTO(item)}, nil
}

type PublishScheduled struct{ db *bun.DB }

func NewPublishScheduled(db *bun.DB) *PublishScheduled { return &PublishScheduled{db: db} }

func (a *PublishScheduled) Handle(ctx context.Context) error { return nil }
