package vector

import (
	"time"

	"github.com/cockroachdb/errors"
	"github.com/qdrant/go-client/qdrant"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Filter 是过滤条件，用 Eq、In、Gt、Gte、Lt、Lte、And、Or、Not 组合。
// 条件按字段的 JSON 名称引用过滤字段，值转换为字段的类型后比较，例如 int 字段可以传 int64。
// 多值字段（切片）只要有一个值满足条件就算满足。范围条件只能用于数字和时间字段。
type Filter interface {
	condition(s *schema) (*qdrant.Condition, error)
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
func And(filters ...Filter) Filter { return logic{and: true, filters: filters} }

// Or 要求满足任意一个条件，没有条件时总是不满足。
func Or(filters ...Filter) Filter { return logic{filters: filters} }

// Not 要求不满足 f。对多值字段，Not(Eq(f, v)) 表示没有一个值等于 v。
func Not(f Filter) Filter { return not{f} }

// never 是总不满足的条件：没有 ID 的 has_id。
func never() *qdrant.Condition { return qdrant.NewHasID() }

func lookup(s *schema, name string) (*field, error) {
	f := s.filters[name]
	if f == nil {
		return nil, errors.Newf("vector: %q is not a filter field", name)
	}
	return f, nil
}

type compare struct {
	field string
	op    string
	value any
}

func (c compare) condition(s *schema) (*qdrant.Condition, error) {
	f, err := lookup(s, c.field)
	if err != nil {
		return nil, err
	}
	v, err := f.valueOf(c.value)
	if err != nil {
		return nil, err
	}
	switch f.kind {
	case kindKeyword, kindBool:
		if c.op != "=" {
			return nil, errors.Newf("vector: field %q supports only Eq and In", c.field)
		}
		if f.kind == kindBool {
			return qdrant.NewMatchBool(f.name, v.Bool()), nil
		}
		s, err := f.keyword(v)
		return qdrant.NewMatchKeyword(f.name, s), err
	case kindInt:
		n, err := f.integer(v)
		if err != nil {
			return nil, err
		}
		if c.op == "=" {
			return qdrant.NewMatchInt(f.name, n), nil
		}
		return qdrant.NewRange(f.name, numberRange(c.op, float64(n))), nil
	case kindFloat:
		return qdrant.NewRange(f.name, numberRange(c.op, v.Float())), nil
	default:
		t := timestamppb.New(v.Interface().(time.Time))
		r := &qdrant.DatetimeRange{}
		switch c.op {
		case "=":
			r.Gte, r.Lte = t, t
		case ">":
			r.Gt = t
		case ">=":
			r.Gte = t
		case "<":
			r.Lt = t
		default:
			r.Lte = t
		}
		return qdrant.NewDatetimeRange(f.name, r), nil
	}
}

func numberRange(op string, x float64) *qdrant.Range {
	r := &qdrant.Range{}
	switch op {
	case "=":
		r.Gte, r.Lte = &x, &x
	case ">":
		r.Gt = &x
	case ">=":
		r.Gte = &x
	case "<":
		r.Lt = &x
	default:
		r.Lte = &x
	}
	return r
}

type in struct {
	field  string
	values []any
}

func (c in) condition(s *schema) (*qdrant.Condition, error) {
	f, err := lookup(s, c.field)
	if err != nil {
		return nil, err
	}
	if len(c.values) == 0 {
		return never(), nil
	}
	switch f.kind {
	case kindKeyword:
		keywords := make([]string, len(c.values))
		for i, value := range c.values {
			v, err := f.valueOf(value)
			if err != nil {
				return nil, err
			}
			if keywords[i], err = f.keyword(v); err != nil {
				return nil, err
			}
		}
		return qdrant.NewMatchKeywords(f.name, keywords...), nil
	case kindInt:
		ints := make([]int64, len(c.values))
		for i, value := range c.values {
			v, err := f.valueOf(value)
			if err != nil {
				return nil, err
			}
			if ints[i], err = f.integer(v); err != nil {
				return nil, err
			}
		}
		return qdrant.NewMatchInts(f.name, ints...), nil
	default:
		filters := make([]Filter, len(c.values))
		for i, v := range c.values {
			filters[i] = Eq(c.field, v)
		}
		return Or(filters...).condition(s)
	}
}

type logic struct {
	and     bool
	filters []Filter
}

func (l logic) condition(s *schema) (*qdrant.Condition, error) {
	if len(l.filters) == 0 {
		if l.and {
			return qdrant.NewFilterAsCondition(&qdrant.Filter{}), nil
		}
		return never(), nil
	}
	conds := make([]*qdrant.Condition, len(l.filters))
	for i, f := range l.filters {
		if f == nil {
			return nil, errors.New("vector: nil filter")
		}
		c, err := f.condition(s)
		if err != nil {
			return nil, err
		}
		conds[i] = c
	}
	if l.and {
		return qdrant.NewFilterAsCondition(&qdrant.Filter{Must: conds}), nil
	}
	return qdrant.NewFilterAsCondition(&qdrant.Filter{Should: conds}), nil
}

type not struct{ f Filter }

func (n not) condition(s *schema) (*qdrant.Condition, error) {
	if n.f == nil {
		return nil, errors.New("vector: nil filter")
	}
	c, err := n.f.condition(s)
	if err != nil {
		return nil, err
	}
	return qdrant.NewFilterAsCondition(&qdrant.Filter{MustNot: []*qdrant.Condition{c}}), nil
}
