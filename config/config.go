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

// Validator 由配置类型在解码后自检。
type Validator interface {
	Validate() error
}

// Document 保存解析后的 YAML 节点和按段解码选项。
type Document struct {
	root     *yaml.Node // DocumentNode，或 nil 表示空流
	sections map[string]struct{}
	lookup   LookupFunc
	path     string // 仅用于错误前缀，可为空
}

// ReadFile 读取 path 并解析为 Document；envfile 的相对路径基于 path 所在目录。
func ReadFile(path string, opts ...Option) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrap(err, "config")
	}
	doc, err := parse(data, filepath.Dir(path), opts)
	if err != nil {
		return nil, errors.Wrapf(err, "config: %s", path)
	}
	doc.path = path
	return doc, nil
}

// Parse 从 YAML 字节解析 Document；envfile 的相对路径基于当前工作目录。
func Parse(data []byte, opts ...Option) (*Document, error) {
	doc, err := parse(data, "", opts)
	if err != nil {
		return nil, errors.Wrap(err, "config")
	}
	return doc, nil
}

// parse 校验文档结构并加载 envfile；环境变量在 Decode 时按段展开。
func parse(data []byte, dir string, opts []Option) (*Document, error) {
	o := applyOptions(opts)
	if len(o.sections) == 0 {
		return nil, errors.New("at least one section must be declared with WithSections")
	}
	if _, ok := o.sections[envFileKey]; ok {
		return nil, errors.Newf("%q is a reserved key and cannot be declared as a section", envFileKey)
	}
	root, err := parseDocument(data)
	if err != nil {
		return nil, safeYAMLError(err, nil, "")
	}
	lookup := o.lookup
	if root != nil {
		if err := rejectNulls(root); err != nil {
			return nil, err
		}
		if err := checkAllowedSections(root, o.sections); err != nil {
			return nil, err
		}
		if node := topLevel(root, envFileKey); node != nil {
			env, err := loadEnvFile(node, dir, o.lookup)
			if err != nil {
				return nil, err
			}
			lookup = withFallback(o.lookup, env)
		}
	}
	return &Document{root: root, sections: o.sections, lookup: lookup}, nil
}

// Decode 将名为 section 的顶层段解码到 dst（必须是非 nil 指针）。
// 仅在该段内展开环境变量；段缺失时保留 dst 中的默认值。
// 若 dst 实现 Validator，解码成功后调用 Validate。
func (d *Document) Decode(section string, dst any) error {
	if d == nil {
		return errors.New("config: nil document")
	}
	if _, ok := d.sections[section]; !ok {
		return errors.Newf("config: section %q is not declared", section)
	}
	if v := reflect.ValueOf(dst); v.Kind() != reflect.Pointer || v.IsNil() {
		return errors.Newf("config: destination must be a non-nil pointer, got %T", dst)
	}

	node := topLevel(d.root, section)
	if node != nil {
		cloned, err := cloneNode(node)
		if err != nil {
			return err
		}
		if err := expandNode(cloned, d.lookup); err != nil {
			return wrapPath(d.path, errors.Wrapf(err, "section %s", section))
		}
		if err := cloned.Load(dst, yaml.WithKnownFields()); err != nil {
			return wrapPath(d.path, safeYAMLError(err, cloned, section))
		}
	}

	if v, ok := dst.(Validator); ok {
		if err := v.Validate(); err != nil {
			return wrapPath(d.path, errors.Wrapf(err, "section %s: invalid configuration", section))
		}
	}
	return nil
}

// HasSection 报告文档是否包含指定顶层段。
func (d *Document) HasSection(section string) bool {
	return d != nil && topLevel(d.root, section) != nil
}

func wrapPath(path string, err error) error {
	if path == "" {
		return errors.Wrap(err, "config")
	}
	return errors.Wrapf(err, "config: %s", path)
}

func topLevel(root *yaml.Node, key string) *yaml.Node {
	body := mappingBody(root)
	if body == nil {
		return nil
	}
	for i := 0; i+1 < len(body.Content); i += 2 {
		k := body.Content[i]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			return body.Content[i+1]
		}
	}
	return nil
}

func mappingBody(root *yaml.Node) *yaml.Node {
	if root == nil {
		return nil
	}
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

func checkAllowedSections(root *yaml.Node, allowed map[string]struct{}) error {
	body := mappingBody(root)
	if body == nil {
		return errors.New("configuration root must be a mapping")
	}
	if err := checkMappings(body); err != nil {
		return err
	}
	for i := 0; i < len(body.Content); i += 2 {
		k := body.Content[i]
		if k.Value == envFileKey {
			continue
		}
		if _, ok := allowed[k.Value]; !ok {
			return errors.Newf("line %d: unknown top-level section %q", k.Line, k.Value)
		}
	}
	return nil
}

func checkMappings(n *yaml.Node) error {
	if n.Kind == yaml.MappingNode {
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
		if err := checkMappings(c); err != nil {
			return err
		}
	}
	return nil
}

func rejectNulls(n *yaml.Node) error {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" {
		return errors.Newf("line %d: explicit null is not allowed (use {} or [] for empty collections, or omit the field to keep defaults)", n.Line)
	}
	for _, c := range n.Content {
		if err := rejectNulls(c); err != nil {
			return err
		}
	}
	return nil
}

// cloneNode 保留源码位置，并为每次 Decode 创建独立节点和别名副本。
func cloneNode(n *yaml.Node) (*yaml.Node, error) {
	active := map[*yaml.Node]bool{}
	var clone func(*yaml.Node) (*yaml.Node, error)
	clone = func(n *yaml.Node) (*yaml.Node, error) {
		if n == nil {
			return nil, nil
		}
		if active[n] {
			return nil, errors.New("config: cyclic YAML alias")
		}
		active[n] = true
		defer delete(active, n)
		if n.Kind == yaml.AliasNode {
			return clone(n.Alias)
		}
		out := *n
		out.Content = nil
		out.Anchor = ""
		out.Alias = nil
		for _, child := range n.Content {
			c, err := clone(child)
			if err != nil {
				return nil, err
			}
			out.Content = append(out.Content, c)
		}
		return &out, nil
	}
	return clone(n)
}

// safeYAMLError 将 YAML 解码诊断替换为字段路径与源码位置。
func safeYAMLError(err error, node *yaml.Node, section string) error {
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
		field := section
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
				for i := 0; i < len(n.Content); i += 2 {
					k, v := n.Content[i], n.Content[i+1]
					name := path + "." + k.Value
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
		walk(node, section)
		out = append(out, errors.Newf("%s at line %d column %d: invalid YAML value, type or field", field, e.Mark.Line, e.Mark.Column))
	}
	return errors.Join(out...)
}
