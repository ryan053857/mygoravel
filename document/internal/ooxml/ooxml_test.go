package ooxml_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ryan053857/mygoravel/document/convert"
	"github.com/ryan053857/mygoravel/document/internal/ooxml"
	"github.com/ryan053857/mygoravel/document/internal/ooxmltest"
)

// open 在 maxExpanded 下打开一个含有 part.xml 的包。
func open(t *testing.T, part string, maxExpanded int64) *ooxml.Package {
	t.Helper()
	pkg, err := ooxml.Open(ooxmltest.Build(t, map[string]string{"part.xml": part}), maxExpanded)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

// nested 返回包裹着 text 的 depth 层嵌套元素。
func nested(depth int, text string) string {
	return strings.Repeat("<x>", depth) + text + strings.Repeat("</x>", depth)
}

func TestReadPartDepth(t *testing.T) {
	root, err := open(t, nested(ooxml.MaxDepth, "deep"), -1).ReadPart(context.Background(), "part.xml")
	if err != nil {
		t.Fatalf("at the depth limit: %v", err)
	}
	node := root
	for range ooxml.MaxDepth {
		node = node.Child("x")
	}
	if node.Text != "deep" {
		t.Fatalf("innermost text = %q", node.Text)
	}
	// 嵌套深得多时直接失败，不会递归穿过它。
	for _, depth := range []int{ooxml.MaxDepth + 1, 1 << 20} {
		_, err := open(t, nested(depth, ""), -1).ReadPart(context.Background(), "part.xml")
		if !errors.Is(err, convert.ErrCorrupt) {
			t.Fatalf("depth %d: err = %v, want ErrCorrupt", depth, err)
		}
	}
}

func TestCheckXMLDepth(t *testing.T) {
	if err := open(t, nested(ooxml.MaxDepth, ""), -1).CheckXML(context.Background(), "part.xml"); err != nil {
		t.Fatalf("at the depth limit: %v", err)
	}
	for _, depth := range []int{ooxml.MaxDepth + 1, 1 << 20} {
		if err := open(t, nested(depth, ""), -1).CheckXML(context.Background(), "part.xml"); !errors.Is(err, convert.ErrCorrupt) {
			t.Fatalf("depth %d: err = %v, want ErrCorrupt", depth, err)
		}
	}
}

func TestRepeatedAttributeLastWins(t *testing.T) {
	root, err := open(t, `<r a="1" a="2"/>`, -1).ReadPart(context.Background(), "part.xml")
	if err != nil {
		t.Fatal(err)
	}
	if got := root.Child("r").Attr("a"); got != "2" {
		t.Errorf("Attr = %q, want 2", got)
	}
}

func TestReadPartTextIsLinear(t *testing.T) {
	// 注释把字符数据切成每个字节一个记号。
	const n = 1 << 18
	part := "<t>" + strings.Repeat("a<!---->", n) + "</t>"
	root, err := open(t, part, -1).ReadPart(context.Background(), "part.xml")
	if err != nil {
		t.Fatal(err)
	}
	if got := root.Child("t").Text; got != strings.Repeat("a", n) {
		t.Fatalf("text has %d bytes, want %d", len(got), n)
	}
}

func TestReadPartChargesElements(t *testing.T) {
	const n = 1000
	part := "<r>" + strings.Repeat("<a/>", n) + "</r>"
	exact := int64(len(part) + (n+1)*ooxml.ElementCost)
	if _, err := open(t, part, exact).ReadPart(context.Background(), "part.xml"); err != nil {
		t.Fatalf("at the limit: %v", err)
	}
	_, err := open(t, part, exact-1).ReadPart(context.Background(), "part.xml")
	var limit *convert.LimitError
	if !errors.As(err, &limit) || limit.Limit != convert.LimitExpanded || limit.Max != exact-1 {
		t.Fatalf("one byte over: err = %v", err)
	}
	// 只算字节放得下，加上元素就放不下。
	if int64(len(part)) >= exact-1 {
		t.Fatal("test part too small")
	}
}

func TestReadPartCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := open(t, "<r/>", -1).ReadPart(ctx, "part.xml"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestXMLPartsBoundsRelationshipParts(t *testing.T) {
	// 关系部件先读取再计数；每次读取仍然受整个上限约束。
	rels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		strings.Repeat("<a/>", 10000) + `</Relationships>`
	pkg, err := ooxml.Open(ooxmltest.Build(t, map[string]string{"_rels/.rels": rels}), 4096)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pkg.XMLParts(context.Background()); !errors.Is(err, convert.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}
