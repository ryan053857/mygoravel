package config

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/cockroachdb/errors"
)

// Defaulter 由配置类型实现。Load 在解码前对 T 及其所有嵌套字段调用 SetDefaults，
// 子字段先于父结构体，因此父结构体可以覆盖子字段的默认值。
type Defaulter interface {
	SetDefaults()
}

// Validator 由配置类型实现。Load 在解码后对 T 及其所有嵌套字段调用 Validate，
// 子字段先于父结构体，所有错误合并返回。
type Validator interface {
	Validate() error
}

func setDefaults(dst any) {
	walk(reflect.ValueOf(dst), "", func(d Defaulter, _ string) { d.SetDefaults() })
}

func validate(dst any) error {
	var errs []error
	walk(reflect.ValueOf(dst), "", func(val Validator, path string) {
		if err := val.Validate(); err != nil {
			if path != "" {
				err = errors.Wrapf(err, "%s", path)
			}
			errs = append(errs, err)
		}
	})
	if len(errs) == 0 {
		return nil
	}
	return errors.Wrap(errors.Join(errs...), "invalid configuration")
}

// walk 后序遍历 v：先访问所有子值（结构体字段、元素、map 值），再对实现了 I 的 v 本身调用 visit。
// path 是 YAML 字段路径，例如 http.trusted_proxies[0]。
//
// 内嵌字段（例如 `yaml:",inline"`）的方法会提升到外层结构体，此时只调用外层一次，
// 避免同一方法执行两遍；外层自己定义了该方法时也只调用外层，与 Go 的方法覆盖语义一致。
func walk[I any](v reflect.Value, path string, visit func(I, string)) {
	walkValue(v, path, visit, false)
}

func walkValue[I any](v reflect.Value, path string, visit func(I, string), skipSelf bool) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			walkValue(v.Elem(), path, visit, skipSelf)
		}
		return
	case reflect.Struct:
		_, outer := as[I](v)
		t := v.Type()
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			if name, skip := fieldName(f); !skip {
				walkValue(v.Field(i), joinPath(path, name), visit, f.Anonymous && outer)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			walkValue(v.Index(i), fmt.Sprintf("%s[%d]", path, i), visit, false)
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			walkValue(iter.Value(), joinPath(path, fmt.Sprint(iter.Key())), visit, false)
		}
	}
	if skipSelf {
		return
	}
	if i, ok := as[I](v); ok {
		visit(i, path)
	}
}

// as 返回 v（或可寻址时的 &v）实现的接口 I。
func as[I any](v reflect.Value) (I, bool) {
	if v.CanAddr() {
		if i, ok := v.Addr().Interface().(I); ok {
			return i, true
		}
	}
	if v.CanInterface() {
		if i, ok := v.Interface().(I); ok {
			return i, true
		}
	}
	var zero I
	return zero, false
}

// fieldName 返回字段在 YAML 中的名称；内嵌字段返回空名以沿用父路径。
func fieldName(f reflect.StructField) (name string, skip bool) {
	tag, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
	switch {
	case tag == "-":
		return "", true
	case f.Anonymous && tag == "":
		return "", false
	case tag != "":
		return tag, false
	default:
		return strings.ToLower(f.Name), false
	}
}

func joinPath(path, name string) string {
	if name == "" {
		return path
	}
	if path == "" {
		return name
	}
	return path + "." + name
}
