package html

import (
	"math"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/ryan053857/mygoravel/document/convert"
)

// HTML 标准规定的合并上限，更大的值会被浏览器截断。
const (
	maxColspan = 1000
	maxRowspan = 65534
)

// 展开表格产生的副本按估计的工作量计数：每个复制的节点计 nodeBytes，复制的文字或属性每个字节计
// textWeight，无论是否会被渲染。它近似转换这些副本的代价，不是严格的内存上限。
const (
	nodeBytes  = 64
	textWeight = 16
)

// inlineCode 是转换库渲染为行内代码的元素。
var inlineCode = map[atom.Atom]bool{atom.Code: true, atom.Var: true, atom.Samp: true, atom.Kbd: true, atom.Tt: true}

// prepareTables 从最内层开始把每个表格展开为没有合并单元格的矩形，并转义单元格中行内代码里的竖线。
// 展开一个表格之前，先把它要产生的副本计入 maxExpanded 的剩余量（负数表示不限制）；
// 放不下的表格返回 expanded 类的 *convert.LimitError。链接和图片地址连同它们所基于的 baseURL 一起计数。
func prepareTables(doc *xhtml.Node, maxExpanded int64, baseURL string) error {
	budget := maxExpanded
	var tables []*xhtml.Node
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if n.Type == xhtml.ElementNode && n.DataAtom == atom.Table {
			tables = append(tables, n)
		}
	}
	walk(doc)
	for _, table := range tables {
		// 转换库把包含表格的表格渲染为内层表格前后的文字，所以这类表格保持原样；复制它的单元格会重复内层表格。
		if containsTable(table) {
			continue
		}
		cost, ok := expandTable(table, budget, int64(len(baseURL)))
		if !ok {
			return &convert.LimitError{Limit: convert.LimitExpanded, Max: maxExpanded}
		}
		if budget >= 0 {
			budget -= cost
		}
	}
	return nil
}

// row 是表格的一行，以及它所在行组最后一行的序号。
type row struct {
	node     *xhtml.Node
	groupEnd int
}

// expandTable 改写 table 的各行，使每行每列一个单元格，合并的单元格重复内容。
// 返回副本的代价；超出 budget（负数表示不限制）时返回 false，表格保持不变。
func expandTable(table *xhtml.Node, budget, baseURL int64) (int64, bool) {
	rows := tableRows(table)
	if len(rows) == 0 {
		return 0, true
	}
	// 分配之前先计数：每行实际占用的宽度来自各单元格跨越的行上的差分数组，补齐的单元格是最宽一行剩下的部分。
	widths := make([]int64, len(rows)+1)
	var cost, area int64
	for i, r := range rows {
		for _, cell := range rowCells(r.node) {
			colspan, rowspan := spans(cell, r.groupEnd-i+1)
			widths[i] += int64(colspan)
			widths[i+rowspan] -= int64(colspan)
			slots := int64(colspan) * int64(rowspan)
			cost = addSaturated(cost, mulSaturated(copyCost(cell, baseURL), slots-1))
			area += slots
		}
	}
	var width, active int64
	for i := range rows {
		active += widths[i]
		width = max(width, active)
	}
	cost = addSaturated(cost, mulSaturated(max(int64(len(rows))*width-area, 0), nodeBytes))
	if budget >= 0 && cost > budget {
		return 0, false
	}

	grid := make([][]*xhtml.Node, len(rows))
	for i, r := range rows {
		column := 0
		for _, cell := range rowCells(r.node) {
			for column < len(grid[i]) && grid[i][column] != nil {
				column++
			}
			colspan, rowspan := spans(cell, r.groupEnd-i+1)
			for k := i; k < i+rowspan; k++ {
				for len(grid[k]) < column+colspan {
					grid[k] = append(grid[k], nil)
				}
				for c := column; c < column+colspan; c++ {
					grid[k][c] = cell
				}
			}
			column += colspan
		}
	}
	gridWidth := 0
	for i := range grid {
		gridWidth = max(gridWidth, len(grid[i]))
	}
	for _, r := range rows {
		for _, cell := range rowCells(r.node) {
			r.node.RemoveChild(cell)
		}
	}
	// 每个单元格只处理一次；它的第一个位置复用它，其余位置放副本。
	placed := map[*xhtml.Node]bool{}
	for i, r := range rows {
		for c := range gridWidth {
			var cell *xhtml.Node
			switch {
			case c >= len(grid[i]) || grid[i][c] == nil:
				cell = &xhtml.Node{Type: xhtml.ElementNode, Data: "td", DataAtom: atom.Td}
			case placed[grid[i][c]]:
				cell = clone(grid[i][c])
			default:
				cell = grid[i][c]
				cell.Attr = withoutSpans(cell.Attr)
				escapeCodePipes(cell)
				placed[cell] = true
			}
			r.node.AppendChild(cell)
		}
	}
	return cost, true
}

// copyCost 返回复制一次 cell 的计数：它的节点、文字和属性，每个链接和图片再加上它所基于的 base URL。
// 结果在 math.MaxInt64 处饱和。
func copyCost(cell *xhtml.Node, baseURL int64) int64 {
	var cost int64
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		cost = addSaturated(cost, addSaturated(nodeBytes, mulSaturated(int64(len(n.Data)), textWeight)))
		for _, attr := range n.Attr {
			cost = addSaturated(cost, mulSaturated(int64(len(attr.Key)+len(attr.Val)), textWeight))
		}
		if n.Type == xhtml.ElementNode && (n.DataAtom == atom.A || n.DataAtom == atom.Img) {
			cost = addSaturated(cost, mulSaturated(baseURL, textWeight))
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(cell)
	return cost
}

// addSaturated 把非负数 a 和 b 相加，在 math.MaxInt64 处饱和。
func addSaturated(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// mulSaturated 把非负数 a 和 b 相乘，在 math.MaxInt64 处饱和。
func mulSaturated(a, b int64) int64 {
	if a != 0 && b > math.MaxInt64/a {
		return math.MaxInt64
	}
	return a * b
}

// containsTable 报告表格的单元格中是否有另一个表格。
func containsTable(table *xhtml.Node) bool {
	var found bool
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		for child := n.FirstChild; child != nil && !found; child = child.NextSibling {
			if child.DataAtom == atom.Table {
				found = true
				return
			}
			walk(child)
		}
	}
	walk(table)
	return found
}

// tableRows 返回 table 的各行和它们所在行组的结束位置，包括行组中的行，不包括嵌套表格的行。
// 直接位于表格下的连续多行组成一个行组。
func tableRows(table *xhtml.Node) []row {
	var rows []row
	group := func(start int) {
		for i := start; i < len(rows); i++ {
			rows[i].groupEnd = len(rows) - 1
		}
	}
	direct := -1
	for child := table.FirstChild; child != nil; child = child.NextSibling {
		switch child.DataAtom {
		case atom.Tr:
			if direct < 0 {
				direct = len(rows)
			}
			rows = append(rows, row{node: child})
		case atom.Thead, atom.Tbody, atom.Tfoot:
			if direct >= 0 {
				group(direct)
				direct = -1
			}
			start := len(rows)
			for r := child.FirstChild; r != nil; r = r.NextSibling {
				if r.DataAtom == atom.Tr {
					rows = append(rows, row{node: r})
				}
			}
			group(start)
		}
	}
	if direct >= 0 {
		group(direct)
	}
	return rows
}

// rowCells 返回一行中的 td 和 th 子元素。
func rowCells(tr *xhtml.Node) []*xhtml.Node {
	var cells []*xhtml.Node
	for child := tr.FirstChild; child != nil; child = child.NextSibling {
		if child.DataAtom == atom.Td || child.DataAtom == atom.Th {
			cells = append(cells, child)
		}
	}
	return cells
}

// spans 返回单元格截断后的 colspan 和 rowspan。rowspan 为 0 时延伸到行组末尾（还剩 remaining 行），
// 任何合并都不会越过行组。
func spans(cell *xhtml.Node, remaining int) (int, int) {
	colspan, rowspan := 1, 1
	for _, attr := range cell.Attr {
		value, ok := spanValue(attr.Val)
		if !ok {
			continue
		}
		switch attr.Key {
		case "colspan":
			colspan = min(max(value, 1), maxColspan)
		case "rowspan":
			rowspan = min(value, maxRowspan)
			if value == 0 {
				rowspan = remaining
			}
		}
	}
	return colspan, min(max(rowspan, 1), remaining)
}

// spanValue 像浏览器一样解析合并属性开头的数字，过长的数字饱和。
func spanValue(value string) (int, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "+")
	n, digits := 0, 0
	for digits < len(value) && value[digits] >= '0' && value[digits] <= '9' {
		if n < maxRowspan {
			n = n*10 + int(value[digits]-'0')
		}
		digits++
	}
	return n, digits > 0
}

// withoutSpans 去掉 colspan 和 rowspan 属性。
func withoutSpans(attrs []xhtml.Attribute) []xhtml.Attribute {
	kept := attrs[:0:0]
	for _, attr := range attrs {
		if attr.Key != "colspan" && attr.Key != "rowspan" {
			kept = append(kept, attr)
		}
	}
	return kept
}

// clone 深拷贝 n，不包括它的父节点和兄弟节点。
func clone(n *xhtml.Node) *xhtml.Node {
	copied := &xhtml.Node{Type: n.Type, Data: n.Data, DataAtom: n.DataAtom, Namespace: n.Namespace,
		Attr: append([]xhtml.Attribute(nil), n.Attr...)}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		copied.AppendChild(clone(child))
	}
	return copied
}

// escapeCodePipes 转义单元格中行内代码里的 '|'，这是 GFM 表格的要求。
func escapeCodePipes(cell *xhtml.Node) {
	var walk func(n *xhtml.Node, inCode bool)
	walk = func(n *xhtml.Node, inCode bool) {
		if n.Type == xhtml.TextNode && inCode {
			n.Data = strings.ReplaceAll(n.Data, "|", `\|`)
		}
		inCode = inCode || inlineCode[n.DataAtom]
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child, inCode)
		}
	}
	walk(cell, false)
}
