package mdwrite

import (
	"strings"

	"github.com/ryan053857/mygoravel/document/convert"
)

// lineEndings 把 CRLF 和单独的 CR 转为 LF，并去掉 NUL 字节。
var lineEndings = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\x00", "")

// Paragraph 转义原文，使任何一行都不会开始一个 Markdown 块结构：换行转为 LF，去掉 NUL 字节和缩进，
// 行首的标记加反斜杠转义。
func Paragraph(text string) string {
	lines := strings.Split(lineEndings.Replace(text), "\n")
	for i, line := range lines {
		lines[i] = escapeLine(strings.TrimLeft(line, " \t"))
	}
	return strings.Join(lines, "\n")
}

// escapeLine 用反斜杠转义行首的块标记。
func escapeLine(line string) string {
	if line == "" {
		return line
	}
	switch line[0] {
	case '#', '>', '-', '+', '*', '=', '|', '`', '~', '<', '_':
		return `\` + line
	}
	digits := 0
	for digits < len(line) && digits < 10 && line[digits] >= '0' && line[digits] <= '9' {
		digits++
	}
	if digits > 0 && digits < len(line) && (line[digits] == '.' || line[digits] == ')') {
		return line[:digits] + `\` + line[digits:]
	}
	return line
}

// cellEscapes 转义表格单元格中的反斜杠和竖线。
var cellEscapes = strings.NewReplacer(`\`, `\\`, "|", `\|`, "\x00", "")

// Cell 合并表格单元格中的空白，去掉 NUL 字节，并转义反斜杠和竖线。
func Cell(text string) string {
	return cellEscapes.Replace(strings.Join(strings.Fields(text), " "))
}

// Table 收集 GFM 表格的各行，在输出上限内渲染。所有单元格都为空的行被跳过。
type Table struct {
	header      []string
	hasHeader   bool
	firstHeader bool
	rows        [][]string
	width       int
	cellBytes   int64
	left        int64 // 渲染结果最多可以占用的字节数
	max         int64 // 错误中报告的上限
}

// NewTable 返回渲染结果不超过 maxBytes 的 Table，maxBytes < 0 表示不限制。firstRowHeader 为 true 时
// 第一行是表头；否则除非调用 SetHeader，表格渲染在一个空的表头之下。
func NewTable(maxBytes int64, firstRowHeader bool) *Table {
	return NewTableWithin(maxBytes, maxBytes, firstRowHeader)
}

// NewTableWithin 与 NewTable 相同，只是表格只能使用输出上限 maxBytes 中剩余的 left 字节，
// 错误中报告的是 maxBytes；left < 0 表示不限制。
func NewTableWithin(left, maxBytes int64, firstRowHeader bool) *Table {
	return &Table{left: left, max: maxBytes, firstHeader: firstRowHeader}
}

// SetHeader 设置表头，单元格都为空时也保留。
func (t *Table) SetHeader(row []string) error {
	for _, cell := range t.header {
		t.cellBytes -= int64(len(cell))
	}
	t.header, t.hasHeader, t.firstHeader = escapeCells(row), true, false
	t.width = max(t.width, len(t.header))
	for _, cell := range t.header {
		t.cellBytes += int64(len(cell))
	}
	return t.check()
}

// Add 追加一行。渲染后的表格会超出上限时返回 *convert.LimitError。
func (t *Table) Add(row []string) error {
	cells := escapeCells(row)
	blank := true
	for _, cell := range cells {
		blank = blank && cell == ""
	}
	if blank {
		return nil
	}
	if t.firstHeader && !t.hasHeader {
		t.header, t.hasHeader = cells, true
	} else {
		t.rows = append(t.rows, cells)
	}
	t.width = max(t.width, len(cells))
	for _, cell := range cells {
		t.cellBytes += int64(len(cell))
	}
	return t.check()
}

// escapeCells 对 row 的每个单元格应用 Cell。与前一个单元格相同的单元格共用转义结果，
// 所以合并单元格展开后重复的内容只转义一次。
func escapeCells(row []string) []string {
	cells := make([]string, len(row))
	for i, cell := range row {
		if i > 0 && cell == row[i-1] {
			cells[i] = cells[i-1]
			continue
		}
		cells[i] = Cell(cell)
	}
	return cells
}

// Len 返回已经添加的行数，包括表头。
func (t *Table) Len() int {
	if t.hasHeader {
		return len(t.rows) + 1
	}
	return len(t.rows)
}

// size 返回渲染后表格的长度。
func (t *Table) size() int64 {
	if !t.hasHeader && len(t.rows) == 0 {
		return 0
	}
	width := int64(t.width)
	rows := int64(len(t.rows)) + 1 // 表头行，无论是否为空
	// 每行是 "|" 加上每列一个 " cell |" 和换行；分隔行是 "|" 加上每列一个 " --- |"；最后的换行不计。
	return rows*(2+3*width) + t.cellBytes + 2 + 6*width - 1
}

// check 报告表格是否仍在上限之内。
func (t *Table) check() error {
	if t.left >= 0 && t.size() > t.left {
		return &convert.LimitError{Limit: convert.LimitOutput, Max: t.max}
	}
	return nil
}

// Markdown 渲染表格，较短的行补齐到最宽的行。既没有行也没有表头的表格渲染为 ""。
func (t *Table) Markdown() string {
	if !t.hasHeader && len(t.rows) == 0 {
		return ""
	}
	var b strings.Builder
	row := func(cells []string) {
		b.WriteString("|")
		for column := range t.width {
			cell := ""
			if column < len(cells) {
				cell = cells[column]
			}
			b.WriteString(" " + cell + " |")
		}
		b.WriteString("\n")
	}
	row(t.header)
	b.WriteString("|" + strings.Repeat(" --- |", t.width) + "\n")
	for _, cells := range t.rows {
		row(cells)
	}
	return strings.TrimSuffix(b.String(), "\n")
}
