package errorx

// Kind 是与传输方式无关的错误分类。HTTP、命令行、队列等边界各自把它映射为
// 状态码、退出码或重试策略；客户端使用 Code 区分具体的业务失败。
type Kind int

const (
	InvalidArgument Kind = iota + 1
	Unauthenticated
	PermissionDenied
	NotFound
	Conflict
	ResourceExhausted
	Unavailable
	Internal
)

// String 返回稳定的 kind 名称。
func (k Kind) String() string {
	switch k {
	case InvalidArgument:
		return "invalid_argument"
	case Unauthenticated:
		return "unauthenticated"
	case PermissionDenied:
		return "permission_denied"
	case NotFound:
		return "not_found"
	case Conflict:
		return "conflict"
	case ResourceExhausted:
		return "resource_exhausted"
	case Unavailable:
		return "unavailable"
	default:
		return "internal"
	}
}

// systemic 报告分类是否代表系统故障而非可预期的业务结果。
func (k Kind) systemic() bool { return k == Unavailable || k == Internal }

// kinds 列出全部分类，新增分类时同步追加。
var kinds = []Kind{InvalidArgument, Unauthenticated, PermissionDenied, NotFound, Conflict, ResourceExhausted, Unavailable, Internal}

// parseKind 是 String 的逆运算，未知名称返回 Internal。
func parseKind(s string) Kind {
	for _, k := range kinds {
		if k.String() == s {
			return k
		}
	}
	return Internal
}
