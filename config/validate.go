package config

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/cockroachdb/errors"
)

// Validator 由配置类型实现。Load 解码后对 dst 及其所有嵌套字段调用 Validate，
// 子字段先于父结构体，所有错误合并返回。
type Validator interface {
	Validate() error
}

var validatorType = reflect.TypeFor[Validator]()

func validate(dst any) error {
	var errs []error
	walkValidate(reflect.ValueOf(dst), "", &errs)
	if len(errs) == 0 {
		return nil
	}
	return errors.Wrap(errors.Join(errs...), "invalid configuration")
}

func walkValidate(v reflect.Value, path string, errs *[]error) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			walkValidate(v.Elem(), path, errs)
		}
		return
	case reflect.Struct:
		t := v.Type()
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, skip := fieldName(f)
			if skip {
				continue
			}
			walkValidate(v.Field(i), joinPath(path, name), errs)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			walkValidate(v.Index(i), fmt.Sprintf("%s[%d]", path, i), errs)
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			walkValidate(iter.Value(), joinPath(path, fmt.Sprint(iter.Key())), errs)
		}
	}

	var val Validator
	switch {
	case v.CanAddr() && v.Addr().Type().Implements(validatorType):
		val = v.Addr().Interface().(Validator)
	case v.CanInterface() && v.Type().Implements(validatorType):
		val = v.Interface().(Validator)
	default:
		return
	}
	if err := val.Validate(); err != nil {
		if path != "" {
			err = errors.Wrapf(err, "%s", path)
		}
		*errs = append(*errs, err)
	}
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
	if path == "" {
		return name
	}
	return path + "." + name
}
