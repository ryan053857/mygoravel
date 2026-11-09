package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/cockroachdb/errors"
	"go.yaml.in/yaml/v4"
)

// Load 读取 YAML 配置文件到 dst。dst 必须是非 nil 指针，调用前填好默认值。
//
// 流程：加载 envfile → 展开 ${VAR} → 严格解码（未知字段报错）→
// 对 dst 及其嵌套字段调用 Validate。文件中未写的字段保留默认值。
func Load(path string, dst any) error {
	if v := reflect.ValueOf(dst); v.Kind() != reflect.Pointer || v.IsNil() {
		return errors.Newf("config: destination must be a non-nil pointer, got %T", dst)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.Wrap(err, "config")
	}
	if err := load(data, filepath.Dir(path), dst); err != nil {
		return errors.Wrapf(err, "config: %s", path)
	}
	return nil
}

func load(data []byte, dir string, dst any) error {
	root, err := parseDocument(data)
	if err != nil {
		return safeYAMLError(err, nil)
	}
	if root != nil {
		body := mappingBody(root)
		if body == nil {
			return errors.New("configuration root must be a mapping")
		}
		if err := checkStructure(body); err != nil {
			return err
		}
		lookup := lookupFunc(os.LookupEnv)
		if node := removeKey(body, envFileKey); node != nil {
			env, err := loadEnvFile(node, dir, lookup)
			if err != nil {
				return err
			}
			lookup = withFallback(lookup, env)
		}
		if err := expandNode(body, lookup); err != nil {
			return err
		}
		if err := body.Load(dst, yaml.WithKnownFields()); err != nil {
			return safeYAMLError(err, body)
		}
	}
	return validate(dst)
}

func mappingBody(root *yaml.Node) *yaml.Node {
	n := root
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return nil
		}
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	return n
}

// removeKey 从映射中移除 key 并返回其值节点；不存在时返回 nil。
func removeKey(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v := m.Content[i+1]
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return v
		}
	}
	return nil
}

func parseDocument(data []byte) (*yaml.Node, error) {
	l, err := yaml.NewLoader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var root yaml.Node
	if err := l.Load(&root); err == io.EOF {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := l.Load(&extra); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, errors.Newf("line %d: expected a single YAML document, found more", extra.Line)
	}
	return &root, nil
}

// checkStructure 拒绝显式 null、非字符串键和重复键。
func checkStructure(n *yaml.Node) error {
	switch {
	case n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null":
		return errors.Newf("line %d: explicit null is not allowed (omit the field to keep its default)", n.Line)
	case n.Kind == yaml.MappingNode:
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.ShortTag() != "!!str" {
				return errors.Newf("line %d: mapping keys must be strings", k.Line)
			}
			if seen[k.Value] {
				return errors.Newf("line %d: duplicate mapping key", k.Line)
			}
			seen[k.Value] = true
		}
	}
	for _, c := range n.Content {
		if err := checkStructure(c); err != nil {
			return err
		}
	}
	return nil
}

// safeYAMLError 将 YAML 解码诊断替换为字段路径与源码位置，避免回显配置值。
func safeYAMLError(err error, body *yaml.Node) error {
	var many *yaml.LoadErrors
	var one *yaml.LoadError
	var failures []*yaml.LoadError
	if errors.As(err, &many) {
		failures = many.Errors
	} else if errors.As(err, &one) {
		failures = []*yaml.LoadError{one}
	} else {
		return err
	}
	var out []error
	for _, e := range failures {
		field := ""
		var walk func(*yaml.Node, string)
		walk = func(n *yaml.Node, path string) {
			if n == nil {
				return
			}
			if n.Line == e.Mark.Line && n.Column == e.Mark.Column {
				field = path
			}
			switch n.Kind {
			case yaml.MappingNode:
				for i := 0; i+1 < len(n.Content); i += 2 {
					k, v := n.Content[i], n.Content[i+1]
					name := joinPath(path, k.Value)
					if k.Line == e.Mark.Line && k.Column == e.Mark.Column {
						field = name
					}
					walk(v, name)
				}
			case yaml.SequenceNode:
				for i, c := range n.Content {
					walk(c, fmt.Sprintf("%s[%d]", path, i))
				}
			}
		}
		walk(body, "")
		if field == "" {
			field = "configuration"
		}
		out = append(out, errors.Newf("%s at line %d column %d: invalid YAML value, type or field", field, e.Mark.Line, e.Mark.Column))
	}
	return errors.Join(out...)
}

func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}
