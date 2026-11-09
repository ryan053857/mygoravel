package config

import (
	"strings"

	"github.com/cockroachdb/errors"
	"go.yaml.in/yaml/v4"
)

// lookupFunc 查找环境变量；第二个返回值表示是否已设置（空字符串仍算已设置）。
type lookupFunc func(key string) (string, bool)

func expandNode(n *yaml.Node, lookup lookupFunc) error {
	switch n.Kind {
	case yaml.ScalarNode:
		return expandScalar(n, lookup)
	case yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			if err := expandNode(n.Content[i], lookup); err != nil {
				return err
			}
		}
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			if err := expandNode(c, lookup); err != nil {
				return err
			}
		}
	}
	return nil
}

func expandScalar(n *yaml.Node, lookup lookupFunc) error {
	if !strings.Contains(n.Value, "${") {
		return nil
	}
	v, err := expand(n.Value, lookup)
	if err != nil {
		return errors.Wrapf(err, "line %d", n.Line)
	}
	n.Value = v
	if n.Style&yaml.TaggedStyle == 0 {
		n.Tag = ""
	}
	return nil
}

func expand(s string, lookup lookupFunc) (string, error) {
	var b strings.Builder
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			b.WriteString(s)
			return b.String(), nil
		}
		if i > 0 && s[i-1] == '$' {
			b.WriteString(s[:i-1])
			b.WriteString("${")
			s = s[i+2:]
			continue
		}
		b.WriteString(s[:i])

		end := strings.IndexByte(s[i+2:], '}')
		if end < 0 {
			return "", errors.New("unterminated environment reference")
		}
		ref := s[i+2 : i+2+end]
		s = s[i+2+end+1:]

		name, def, hasDef := strings.Cut(ref, ":-")
		if !validName(name) {
			return "", errors.New("invalid environment variable reference")
		}
		val, ok := lookup(name)
		switch {
		case hasDef && val == "":
			val = def
		case !ok:
			return "", errors.Newf("environment variable %s is not set (use ${%s:-} to allow empty)", name, name)
		}
		b.WriteString(val)
	}
}

func validName(name string) bool {
	if name == "" {
		return false
	}
	for i, c := range name {
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
