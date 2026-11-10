package files

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

const (
	StatusTemporary = "temporary" // 已登记，等待上传或业务确认；过期后由 Cleanup 回收
	StatusReady     = "ready"     // 业务已确认使用，长期保留
	StatusDeleted   = "deleted"   // 已删除，等待清理对象后删除记录
)

// Record 是 files 表模型。应用可以加列，框架只读写这里声明的列。
// URL 不入库，服务返回记录时按磁盘公开基址填写。
type Record struct {
	bun.BaseModel `bun:"table:files,alias:f"`
	ID            uuid.UUID  `bun:"id,pk,type:uuid"      json:"id"`
	Disk          string     `bun:"disk,notnull"         json:"disk"`
	Key           string     `bun:"key,notnull"          json:"key"`
	Filename      string     `bun:"filename,notnull"     json:"filename"`
	ContentType   string     `bun:"content_type,notnull" json:"content_type"`
	Size          int64      `bun:"size,notnull"         json:"size"`
	OwnerType     string     `bun:"owner_type,notnull"   json:"-"`
	OwnerID       string     `bun:"owner_id,notnull"     json:"-"`
	Status        string     `bun:"status,notnull"       json:"status"`
	ExpiresAt     *time.Time `bun:"expires_at"           json:"expires_at,omitempty"`
	CreatedAt     time.Time  `bun:"created_at,notnull"   json:"created_at"`
	UpdatedAt     time.Time  `bun:"updated_at,notnull"   json:"updated_at"`
	URL           string     `bun:"-"                    json:"url"`
}

// FileInput 描述一个待上传文件。Disk 为空时使用默认磁盘；Size 是声明大小，上传时严格核对。
type FileInput struct {
	Disk        string
	Filename    string
	ContentType string
	Size        int64
}

// Multipart 是分片直传会话。除最后一片外，每片正好 PartSize 字节。
type Multipart struct {
	UploadID  string `json:"upload_id"`
	PartSize  int64  `json:"part_size"`
	PartCount int    `json:"part_count"`
}
