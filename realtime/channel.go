package realtime

import (
	"context"
	"regexp"
	"strings"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/identity"
)

// Params 是频道名中 {name} 占位符匹配到的值。
type Params map[string]string

// Authorizer 决定 sub 能否订阅频道，p 是频道名中占位符的值。ctx 带有连接建立时请求的值（例如租户）和主体。
// 返回 false 或 4xx 类错误表示拒绝；返回 5xx 类错误表示暂时无法判断，客户端稍后会重新订阅。
type Authorizer func(ctx context.Context, sub identity.Subject, p Params) (bool, error)

// Rule 是一类频道的订阅规则，由 Public、Private 或 Presence 创建。
type Rule struct {
	authorize Authorizer         // Private
	presence  PresenceAuthorizer // Presence；两者都为 nil 表示公开
	publish   bool
}

// Public 返回公开频道的规则：任何连接都可以订阅，包括没有登录的连接。
func Public() Rule { return Rule{} }

// Private 返回私有频道的规则：只有已认证、并且 authorize 同意的主体可以订阅。authorize 为 nil 时 panic。
//
// 授权只在订阅时进行：之后主体失去访问权限时（例如被移出会话），已有的订阅保持到连接断开，
// 需要立即生效时调用 Realtime.Unsubscribe。
func Private(authorize Authorizer) Rule {
	if authorize == nil {
		panic("realtime: nil authorizer")
	}
	return Rule{authorize: authorize}
}

// ClientPublish 返回允许客户端直接向频道发布消息的规则，用于「正在输入」这类不经过服务器业务逻辑的提示。
// 只有已经订阅频道的客户端可以发布。这类消息只发给 WebSocket 客户端，Hub 的订阅收不到；
// 内容由客户端决定，服务器不做校验，接收方不应当把它当作可信的数据。
func (r Rule) ClientPublish() Rule {
	r.publish = true
	return r
}

type channelRule struct {
	pattern string
	re      *regexp.Regexp
	names   []string
	rule    Rule
}

var placeholder = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// compile 把 chat:{conversation} 这样的模式编译为规则。占位符匹配一段字母、数字、_ 和 -。
func compile(pattern string, rule Rule) (channelRule, error) {
	c := channelRule{pattern: pattern, rule: rule}
	if err := broadcast.ValidateTopic(placeholder.ReplaceAllString(pattern, "x")); err != nil {
		return c, errors.Wrapf(err, "realtime: channel pattern %q", pattern)
	}
	var expr strings.Builder
	expr.WriteString("^")
	last := 0
	for _, m := range placeholder.FindAllStringSubmatchIndex(pattern, -1) {
		name := pattern[m[2]:m[3]]
		for _, seen := range c.names {
			if seen == name {
				return c, errors.Newf("realtime: channel pattern %q repeats {%s}", pattern, name)
			}
		}
		c.names = append(c.names, name)
		expr.WriteString(regexp.QuoteMeta(pattern[last:m[0]]))
		expr.WriteString(`([A-Za-z0-9_-]+)`)
		last = m[1]
	}
	expr.WriteString(regexp.QuoteMeta(pattern[last:]))
	expr.WriteString("$")
	c.re = regexp.MustCompile(expr.String())
	return c, nil
}

func (c channelRule) match(channel string) (Params, bool) {
	m := c.re.FindStringSubmatch(channel)
	if m == nil {
		return nil, false
	}
	p := make(Params, len(c.names))
	for i, name := range c.names {
		p[name] = m[i+1]
	}
	return p, true
}
