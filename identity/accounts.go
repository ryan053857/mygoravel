package identity

import (
	"context"

	"github.com/cockroachdb/errors"
)

// Accounts 是一个入口接受的账号体系：键是账号体系的 SubjectType，值是这套账号的 Resolver，
// 例如按用户表重新加载主体、按租户加载权限。值为 nil 表示直接使用凭据给出的主体。
//
// rest.Options、mcpx.HandlerOptions 和 realtime.Options 都用它声明入口接受哪些主体：凭据的 Type 不在其中时，
// 与凭据无效相同。一个入口可以接受多套账号，同一个 Resolver 也可以用于多个入口：
//
//	identity.Accounts{Member: roles.Resolve, Customer: customers.Resolve}
type Accounts map[SubjectType]Resolver

// Validate 检查每个账号体系的名称，见 SubjectType.Validate。
func (a Accounts) Validate() error {
	for t := range a {
		if err := t.Validate(); err != nil {
			return errors.Wrap(err, "identity: accounts")
		}
	}
	return nil
}

// Resolve 是一个 Resolver：按 sub.Type 调用对应账号体系的 Resolver；sub.Type 不在 a 中时返回 ErrUnauthenticated。
func (a Accounts) Resolve(ctx context.Context, sub Subject) (Subject, error) {
	resolve, ok := a[sub.Type]
	switch {
	case !ok:
		return Subject{}, ErrUnauthenticated.New()
	case resolve == nil:
		return sub, nil
	}
	return resolve(ctx, sub)
}
