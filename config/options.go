package config

import "os"

// LookupFunc 查找环境变量；第二个返回值表示是否已设置（空字符串仍算已设置）。
type LookupFunc func(key string) (string, bool)

// Option 配置 Document 的解析选项。
type Option func(*options)

type options struct {
	sections map[string]struct{}
	lookup   LookupFunc
}

func applyOptions(opts []Option) options {
	o := options{
		sections: make(map[string]struct{}),
		lookup:   os.LookupEnv,
	}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// WithSections 声明允许的顶层配置段；文档中出现未声明段时解析失败。
// 至少声明一个段；空文档始终合法。envfile 是保留键，不能声明为段。
func WithSections(names ...string) Option {
	return func(o *options) {
		for _, n := range names {
			o.sections[n] = struct{}{}
		}
	}
}

// WithLookup 替换默认的进程环境查找（os.LookupEnv），通常只在测试中使用。
// envfile 中的变量仍作为 fn 未命中时的后备；fn 为 nil 时视为空环境。
func WithLookup(fn LookupFunc) Option {
	return func(o *options) {
		if fn == nil {
			fn = func(string) (string, bool) { return "", false }
		}
		o.lookup = fn
	}
}
