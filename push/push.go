package push

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/identity"
)

// ErrInvalidDevice 表示注册的设备不合法：平台不是 Android、IOS、HarmonyOS 之一，或者 token 为空、过长。
var ErrInvalidDevice = errorx.Define(errorx.InvalidArgument, "push.invalid_device")

// maxToken 是设备 token 的最大字节数。阿里云的 deviceId 是 32 个字符，FCM 的 registration token 一百多个字符。
const maxToken = 512

// Platform 是设备的操作系统。不同平台在推送服务中通常是不同的应用，分开推送。
type Platform string

const (
	Android   Platform = "android"
	IOS       Platform = "ios"
	HarmonyOS Platform = "harmony"
)

// Valid 报告 p 是否是 Android、IOS、HarmonyOS 之一。
func (p Platform) Valid() bool {
	return p == Android || p == IOS || p == HarmonyOS
}

// Device 是一台接收推送的设备，也是 push_devices 表的 Bun 模型。
type Device struct {
	bun.BaseModel `bun:"table:push_devices,alias:pd"`

	ID          uuid.UUID            `bun:"id,pk,type:uuid"` // UUIDv7
	Platform    Platform             `bun:"platform,notnull"`
	Token       string               `bun:"token,notnull"` // 推送服务给设备的标识，例如阿里云的 deviceId
	Tenant      string               `bun:"tenant,notnull"`
	SubjectType identity.SubjectType `bun:"subject_type,notnull"`
	SubjectID   string               `bun:"subject_id,notnull"`
	Credential  string               `bun:"credential,notnull"` // 注册时所用凭据的 ID
	CreatedAt   time.Time            `bun:"created_at,notnull"`
	UpdatedAt   time.Time            `bun:"updated_at,notnull"` // 最近一次注册的时间
}

// Register 把设备登记给 sub：之后发给 sub 所在租户中这个主体的推送会发到这台设备。
// App 在登录后、以及每次启动时调用，token 是推送服务的客户端 SDK 给出的设备标识。
//
// 一台设备同时只属于一个主体：已经登记给别人的设备改为登记给 sub，例如在同一台手机上换了账号。
// 同时记下 sub.Credential，撤销这个凭据时用 Unregister 一起删除，退出登录的设备不再收到推送。
func Register(ctx context.Context, db bun.IDB, sub identity.Subject, platform Platform, token string) error {
	if err := sub.Ref().Validate(); err != nil {
		return errors.Wrap(err, "push")
	}
	if !platform.Valid() {
		return ErrInvalidDevice.Wrap(errors.Newf("push: unknown platform %q", platform))
	}
	if token == "" || len(token) > maxToken {
		return ErrInvalidDevice.Wrap(errors.Newf("push: device token must be 1 to %d bytes", maxToken))
	}
	now := time.Now().UTC()
	d := &Device{
		ID: uuid.Must(uuid.NewV7()), Platform: platform, Token: token,
		Tenant: sub.Tenant, SubjectType: sub.Type, SubjectID: sub.ID, Credential: sub.Credential,
		CreatedAt: now, UpdatedAt: now,
	}
	_, err := db.NewInsert().Model(d).
		On("CONFLICT (platform, token) DO UPDATE").
		Set("tenant = EXCLUDED.tenant").
		Set("subject_type = EXCLUDED.subject_type").
		Set("subject_id = EXCLUDED.subject_id").
		Set("credential = EXCLUDED.credential").
		Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	return errors.Wrap(err, "push: register device")
}

// Unregister 删除 sub 用凭据 credential 登记的设备，返回删除的个数。与 apitoken 的 Revoke 一起调用：
// 退出登录时传入 sub.Credential，在设备列表中移除某台设备时传入那台设备的 token ID。
func Unregister(ctx context.Context, db bun.IDB, sub identity.Subject, credential string) (int, error) {
	if credential == "" {
		return 0, nil
	}
	q := db.NewDelete().Model((*Device)(nil)).
		Where("subject_type = ? AND subject_id = ?", sub.Type, sub.ID).Where("credential = ?", credential)
	return affected(q.Exec(ctx))
}

// UnregisterOthers 删除 sub 除本次请求所用凭据（sub.Credential）以外登记的全部设备，返回删除的个数。
// 与 apitoken 的 RevokeOthers 一起调用，例如“退出其他设备”。sub 没有凭据 ID 时删除它的全部设备。
func UnregisterOthers(ctx context.Context, db bun.IDB, sub identity.Subject) (int, error) {
	q := db.NewDelete().Model((*Device)(nil)).Where("subject_type = ? AND subject_id = ?", sub.Type, sub.ID)
	if sub.Credential != "" {
		q = q.Where("credential <> ?", sub.Credential)
	}
	return affected(q.Exec(ctx))
}

// UnregisterSubject 删除主体 ref 的全部设备，返回删除的个数。与 apitoken 的 RevokeSubject 一起调用，
// 例如封禁、注销账号或修改密码之后。
func UnregisterSubject(ctx context.Context, db bun.IDB, ref identity.Ref) (int, error) {
	q := db.NewDelete().Model((*Device)(nil)).Where("subject_type = ? AND subject_id = ?", ref.Type, ref.ID)
	return affected(q.Exec(ctx))
}

// Cleanup 返回删除超过 keep 没有再注册的设备的清理函数，每次最多删除 limit 台，用于定时任务。
// App 每次启动时都会注册，长期没有更新的设备通常已经卸载了 App。keep 不大于 0 时 panic。
func Cleanup(db bun.IDB, keep time.Duration) func(ctx context.Context, limit int) (int, error) {
	if db == nil {
		panic("push: nil database")
	}
	if keep <= 0 {
		panic("push: keep must be positive")
	}
	return func(ctx context.Context, limit int) (int, error) {
		if limit <= 0 {
			return 0, errors.New("push: cleanup limit must be positive")
		}
		old := db.NewSelect().Model((*Device)(nil)).Column("id").
			Where("updated_at < ?", time.Now().Add(-keep).UTC()).OrderExpr("updated_at").Limit(int64(limit))
		return affected(db.NewDelete().Model((*Device)(nil)).Where("id IN (?)", old).Exec(ctx))
	}
}

func affected(res sql.Result, err error) (int, error) {
	if err != nil {
		return 0, errors.Wrap(err, "push: delete devices")
	}
	n, err := res.RowsAffected()
	return int(n), errors.Wrap(err, "push: delete devices")
}

//go:embed migrations
var migrationFiles embed.FS

// Migrations 返回设备表 push_devices 的迁移，供应用发布。
func Migrations() fs.FS {
	f, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err)
	}
	return f
}
