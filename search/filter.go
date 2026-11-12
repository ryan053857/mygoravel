package search

import (
	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
)

// Filter 是过滤条件，用 Eq、In、Gt、Gte、Lt、Lte、And、Or、Not 组合。
// 条件按字段的 JSON 名称引用过滤字段，值转换为字段的类型后比较，例如 int 字段可以传 int64。
// 多值字段（切片）只要有一个值满足条件就算满足。
type Filter interface {
	build(s *schema, e *sqlExpr) error
}

// Eq 要求字段等于 value。
func Eq(field string, value any) Filter { return compare{field, "=", value} }

// Gt 要求字段大于 value。
func Gt(field string, value any) Filter { return compare{field, ">", value} }

// Gte 要求字段大于等于 value。
func Gte(field string, value any) Filter { return compare{field, ">=", value} }

// Lt 要求字段小于 value。
func Lt(field string, value any) Filter { return compare{field, "<", value} }

// Lte 要求字段小于等于 value。
func Lte(field string, value any) Filter { return compare{field, "<=", value} }

// In 要求字段等于 values 中的一个，values 为空时没有文档满足。
func In[T any](field string, values ...T) Filter {
	anys := make([]any, len(values))
	for i, v := range values {
		anys[i] = v
	}
	return in{field, anys}
}

// And 要求满足所有条件，没有条件时总是满足。
func And(filters ...Filter) Filter { return logic{"AND", "1 = 1", filters} }

// Or 要求满足任意一个条件，没有条件时总是不满足。
func Or(filters ...Filter) Filter { return logic{"OR", "1 = 0", filters} }

// Not 要求不满足 f。对多值字段，Not(Eq(f, v)) 表示没有一个值等于 v。
func Not(f Filter) Filter { return not{f} }

const valueExists = "EXISTS (SELECT 1 FROM search_values AS v WHERE v.entry_id = e.id AND v.field = ? AND v.value "

func lookup(s *schema, name string) (*field, error) {
	f := s.filters[name]
	if f == nil {
		return nil, errors.Newf("search: %q is not a filter field", name)
	}
	return f, nil
}

type compare struct {
	field string
	op    string
	value any
}

func (c compare) build(s *schema, e *sqlExpr) error {
	f, err := lookup(s, c.field)
	if err != nil {
		return err
	}
	v, err := f.encodeAny(c.value)
	if err != nil {
		return err
	}
	e.add(valueExists+c.op+" ?)", c.field, v)
	return nil
}

type in struct {
	field  string
	values []any
}

func (c in) build(s *schema, e *sqlExpr) error {
	f, err := lookup(s, c.field)
	if err != nil {
		return err
	}
	if len(c.values) == 0 {
		e.add("1 = 0")
		return nil
	}
	encoded := make([]string, len(c.values))
	for i, v := range c.values {
		if encoded[i], err = f.encodeAny(v); err != nil {
			return err
		}
	}
	e.add(valueExists+"IN (?))", c.field, bun.List(encoded))
	return nil
}

type logic struct {
	op      string
	empty   string
	filters []Filter
}

func (l logic) build(s *schema, e *sqlExpr) error {
	if len(l.filters) == 0 {
		e.add(l.empty)
		return nil
	}
	e.add("(")
	for i, f := range l.filters {
		if f == nil {
			return errors.New("search: nil filter")
		}
		if i > 0 {
			e.add(" " + l.op + " ")
		}
		if err := f.build(s, e); err != nil {
			return err
		}
	}
	e.add(")")
	return nil
}

type not struct{ f Filter }

func (n not) build(s *schema, e *sqlExpr) error {
	if n.f == nil {
		return errors.New("search: nil filter")
	}
	e.add("NOT (")
	if err := n.f.build(s, e); err != nil {
		return err
	}
	e.add(")")
	return nil
}
