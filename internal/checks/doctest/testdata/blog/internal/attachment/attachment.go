// Package attachment 声明文档中只以片段给出的部分：附件分段的模型。vector.go 和 find.go 从文档中提取。
package attachment

import (
	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// AttachmentChunk 是附件切分出的一个分段，见 documents.md#processing-uploads。
type AttachmentChunk struct {
	bun.BaseModel `bun:"table:attachment_chunks"`
	ArticleID     uuid.UUID `bun:"article_id"`
	FileID        uuid.UUID `bun:"file_id"`
	Position      int       `bun:"position"`
	Text          string    `bun:"text"`
	Context       string    `bun:"context"`
	Start, End    int
	Pages         []int `bun:"pages,array"`
}
