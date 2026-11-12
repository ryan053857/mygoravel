package database

import "strings"

// maxQueryText 是链路和慢查询日志中 SQL 的最大字节数，超出的部分截断。
const maxQueryText = 2048

// sanitize 把 SQL 中的字面量换成 ?，用于链路和慢查询日志。
//
// Bun 把参数直接写进 SQL，事件中的语句带着密码哈希、token、手机号这类值；换掉之后只剩语句的结构，
// 同一条语句不同参数的执行看起来一样，可以放心地发往日志和链路平台。替换的是单引号字符串（包括 E'…'）、
// 美元引号字符串和数字；双引号中的标识符、$1 形式的占位符和注释原样保留。结果超过 maxQueryText 时截断。
func sanitize(query string) string {
	var b strings.Builder
	b.Grow(min(len(query), maxQueryText+3))
	n := len(query)
	for i := 0; i < n && b.Len() < maxQueryText; {
		c := query[i]
		switch {
		case c == '\'' || (c == 'E' || c == 'e') && i+1 < n && query[i+1] == '\'' && !identByte(prev(query, i)):
			escapes := c != '\''
			if escapes {
				i++
			}
			i = skipString(query, i+1, escapes)
			b.WriteByte('?')
		case c == '"':
			end := strings.IndexByte(query[i+1:], '"')
			if end < 0 {
				end = n - i - 2
			}
			b.WriteString(query[i : i+end+2])
			i += end + 2
		case c == '$' && i+1 < n && isDigit(query[i+1]):
			j := i + 1
			for j < n && isDigit(query[j]) {
				j++
			}
			b.WriteString(query[i:j])
			i = j
		case c == '$' && !identByte(prev(query, i)):
			if tag, ok := dollarTag(query, i); ok {
				end := strings.Index(query[i+len(tag):], tag)
				if end < 0 {
					i = n
				} else {
					i += len(tag) + end + len(tag)
				}
				b.WriteByte('?')
				break
			}
			b.WriteByte(c)
			i++
		case c == '-' && i+1 < n && query[i+1] == '-':
			end := strings.IndexByte(query[i:], '\n')
			if end < 0 {
				end = n - i
			}
			b.WriteString(query[i : i+end])
			i += end
		case c == '/' && i+1 < n && query[i+1] == '*':
			end := strings.Index(query[i+2:], "*/")
			if end < 0 {
				end = n - i - 4
			}
			b.WriteString(query[i : i+end+4])
			i += end + 4
		case isDigit(c) && !identByte(prev(query, i)):
			j := i
			for j < n && (isDigit(query[j]) || query[j] == '.' ||
				(query[j] == 'e' || query[j] == 'E') && j+1 < n && (isDigit(query[j+1]) || query[j+1] == '-' || query[j+1] == '+') ||
				(query[j] == '-' || query[j] == '+') && (query[j-1] == 'e' || query[j-1] == 'E')) {
				j++
			}
			b.WriteByte('?')
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	if b.Len() >= maxQueryText {
		s := b.String()[:maxQueryText]
		return strings.ToValidUTF8(s, "") + "..."
	}
	return b.String()
}

// skipString 从字符串内容的第一个字节 i 开始，返回结束引号之后的位置。连续两个单引号表示引号本身；
// escapes 为 true 时（E'…'）反斜杠转义下一个字节。没有结束引号时返回 len(s)。
func skipString(s string, i int, escapes bool) int {
	for i < len(s) {
		switch {
		case escapes && s[i] == '\\':
			i += 2
		case s[i] == '\'' && i+1 < len(s) && s[i+1] == '\'':
			i += 2
		case s[i] == '\'':
			return i + 1
		default:
			i++
		}
	}
	return len(s)
}

// dollarTag 返回从 i 开始的美元引号标签，例如 $$ 或 $body$。
func dollarTag(s string, i int) (string, bool) {
	for j := i + 1; j < len(s); j++ {
		switch c := s[j]; {
		case c == '$':
			return s[i : j+1], true
		case !identByte(c) || j == i+1 && isDigit(c):
			return "", false
		}
	}
	return "", false
}

func prev(s string, i int) byte {
	if i == 0 {
		return ' '
	}
	return s[i-1]
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// identByte 报告 c 能否出现在标识符中：此时紧跟其后的数字或引号是标识符的一部分，例如 t1、v2。
func identByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || isDigit(c) || c == '_' || c == '$' || c >= 0x80
}
