package identity

import (
	"strings"

	"github.com/cockroachdb/errors"
)

// SubjectType 是一套账号体系的名称，例如 staff、member、customer，由应用声明：
//
//	const Customer identity.SubjectType = "customer"
//
// 它以小写字母开头，只包含小写字母、数字和下划线，最长 64 字节。框架只定义 System。
type SubjectType string

// System 是框架和应用自己的后台主体，例如队列任务、定时任务和命令行，不属于任何账号体系。
const System SubjectType = "system"

const (
	// maxType 与迁移中 subject_type 一类的列宽 VARCHAR(64) 一致。
	maxType = 64
	// maxID 与迁移中 subject_id 一类的列宽 VARCHAR(191) 一致。
	maxID = 191
)

// Validate 检查名称的格式。
func (t SubjectType) Validate() error {
	if t == "" || len(t) > maxType {
		return errors.Newf("identity: subject type %q must be 1 to %d bytes", t, maxType)
	}
	for i, c := range []byte(t) {
		if !('a' <= c && c <= 'z' || i > 0 && ('0' <= c && c <= '9' || c == '_')) {
			return errors.Newf("identity: subject type %q must start with a-z and contain only a-z, 0-9 and _", t)
		}
	}
	return nil
}

// Ref 引用一个主体：哪一套账号体系（Type）中的哪一个（ID）。
//
// ID 是不透明的字符串，只需在同一个 Type 内唯一：不同账号体系的表可以各自使用自增 ID、UUID 或外部系统的标识，
// 框架保存主体的地方都同时记录 Type 和 ID，ID 相同的两个账号不会混淆。
type Ref struct {
	Type SubjectType
	ID   string
}

// IsZero 报告 r 是否为零值，例如没有代为登录者。
func (r Ref) IsZero() bool { return r == Ref{} }

// Validate 检查 Type 的格式，以及 ID 不为空、不超过 191 字节。
func (r Ref) Validate() error {
	if err := r.Type.Validate(); err != nil {
		return err
	}
	if r.ID == "" || len(r.ID) > maxID {
		return errors.Newf("identity: subject ID must be 1 to %d bytes", maxID)
	}
	return nil
}

// String 返回 type:id，例如 customer:42，用于缓存键、广播主题这类只能放一个字符串的地方；零值返回 ""。
// Type 不含冒号，ParseRef 可以还原。
func (r Ref) String() string {
	if r.IsZero() {
		return ""
	}
	return string(r.Type) + ":" + r.ID
}

// ParseRef 解析 Ref.String 的结果。
func ParseRef(s string) (Ref, error) {
	typ, id, ok := strings.Cut(s, ":")
	if !ok {
		return Ref{}, errors.Newf("identity: invalid subject reference %q", s)
	}
	r := Ref{Type: SubjectType(typ), ID: id}
	if err := r.Validate(); err != nil {
		return Ref{}, err
	}
	return r, nil
}
