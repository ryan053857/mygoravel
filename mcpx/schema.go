package mcpx

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/danielgtaylor/huma/v2"
)

// defsPrefix 是工具 schema 中嵌套类型的引用前缀：每个工具的 schema 自带用到的类型，放在 $defs 中。
const defsPrefix = "#/$defs/"

// schema 是一个类型按 Huma 规则生成的 JSON Schema，以及校验它所需的 registry。
type schema struct {
	registry huma.Registry
	root     *huma.Schema
	mode     huma.ValidateMode
	typ      string         // 顶层的 type，object 表示结构体或 map
	json     map[string]any // 发给客户端的 schema，带 $defs
}

// newSchema 为 t 生成 schema。每个类型使用自己的 registry，$defs 中只有它用到的类型。
func newSchema(t reflect.Type, mode huma.ValidateMode) *schema {
	registry := huma.NewMapRegistry(defsPrefix, huma.DefaultSchemaNamer)
	root := registry.Schema(t, false, "")
	s := &schema{registry: registry, root: root, mode: mode}
	if root == nil {
		return s
	}
	data, err := json.Marshal(root)
	if err != nil {
		panic(errors.Wrapf(err, "mcpx: encode schema of %s", t))
	}
	if err := json.Unmarshal(data, &s.json); err != nil {
		panic(errors.Wrapf(err, "mcpx: encode schema of %s", t))
	}
	s.typ, _ = s.json["type"].(string)
	if defs := registry.Map(); len(defs) > 0 {
		s.json["$defs"] = defs
	}
	return s
}

// validate 按 schema 校验 raw，通过后把它解码到 dst，返回每个不符合的字段的描述；raw 为空时视为 {}。
func (s *schema) validate(raw json.RawMessage, dst any) []string {
	var err error
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	var v any
	if err = json.Unmarshal(raw, &v); err != nil {
		return []string{"arguments are not valid JSON: " + err.Error()}
	}
	if obj, ok := v.(map[string]any); ok {
		s.fillDefaults(s.root, obj)
		if raw, err = json.Marshal(obj); err != nil {
			return []string{err.Error()}
		}
	}
	res := &huma.ValidateResult{}
	huma.Validate(s.registry, s.root, huma.NewPathBuffer([]byte{}, 0), s.mode, v, res)
	if len(res.Errors) > 0 {
		problems := make([]string, len(res.Errors))
		for i, err := range res.Errors {
			problems[i] = describe(err)
		}
		return problems
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return []string{err.Error()}
	}
	return nil
}

// fillDefaults 为 obj 中没有提供的字段填上 schema 的 default，嵌套的对象同样处理，与 Huma 处理请求体的方式一致：
// 例如嵌入 page.OffsetParams 的参数，没有传 per_page 时按 20 处理。
func (s *schema) fillDefaults(sc *huma.Schema, obj map[string]any) {
	if sc != nil && sc.Ref != "" {
		sc = s.registry.SchemaFromRef(sc.Ref)
	}
	if sc == nil {
		return
	}
	for name, prop := range sc.Properties {
		value, ok := obj[name]
		if !ok && prop.Default != nil {
			obj[name] = prop.Default
			continue
		}
		if nested, isObj := value.(map[string]any); isObj {
			s.fillDefaults(prop, nested)
		}
	}
}

// describe 返回一个校验错误的描述，例如 "id: expected string to be RFC 4122 uuid"。
func describe(err error) string {
	var d *huma.ErrorDetail
	if errors.As(err, &d) {
		if loc := strings.TrimPrefix(d.Location, "."); loc != "" {
			return loc + ": " + d.Message
		}
		return d.Message
	}
	return err.Error()
}
