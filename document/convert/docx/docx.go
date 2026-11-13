// Package docx 把 Word 文档（.docx）转换为 Markdown。
//
// 标题来自标题样式、Title 样式和大纲级别；编号和项目符号段落成为列表项；表格成为 GFM 表格，
// 横向合并的单元格重复内容，纵向合并的单元格把值向下延续；外部超链接成为 Markdown 链接。
// Word 文档没有固定的页，所以 Document 没有 Section。
package docx

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-tenon/tenon/document/convert"
	"github.com/go-tenon/tenon/document/internal/mdwrite"
	"github.com/go-tenon/tenon/document/internal/ooxml"
	"github.com/go-tenon/tenon/document/internal/source"
)

// maxListDepth 是 Word 支持的最深列表级别，从 0 开始计。
const maxListDepth = 8

// linkTarget 编码尖括号链接地址中不能出现的字符。
var linkTarget = strings.NewReplacer("<", "%3C", ">", "%3E", "\n", "%0A")

// styleInfo 是段落样式提供的信息：父样式、标题级别和编号。
type styleInfo struct {
	basedOn     string
	heading     int
	numID, ilvl string
}

// noHeading 标记明确不是标题的样式，停止继承。
const noHeading = -1

// maxStyleChain 限制沿 basedOn 链接追溯的次数。
const maxStyleChain = 16

// Options 配置 Word 转换器，零值即默认配置。
type Options struct {
	convert.Limits
}

// Converter 转换 Word 文档，可以并发使用。
type Converter struct {
	opts Options
}

// New 返回 Word 转换器。
func New(opts Options) *Converter {
	return &Converter{opts: opts}
}

// document 保存渲染正文需要的信息：标题样式、列表格式和链接地址。
type document struct {
	ctx     context.Context
	limits  convert.Limits
	ordered map[string]map[string]bool
	links   map[string]string
	styles  map[string]styleInfo
	// numAbstract 把编号实例映射到它的抽象定义；styleLevels 把抽象定义中的样式映射到它们使用的级别。
	numAbstract map[string]string
	styleLevels map[string]map[string]string
	inCell      bool // 表格单元格不转义：块标记在那里不起作用
	inLink      bool // 链接文字中的超链接只渲染为文字
	// output 是留给顶层块及其分隔符的输出预算，负数表示不限制。
	output int64
}

// Convert 把 in 转换为 Markdown。
func (c *Converter) Convert(ctx context.Context, in convert.Input) (convert.Document, error) {
	limits := c.opts.Effective()
	data, err := source.Read(ctx, in, limits.MaxBytes)
	if err != nil {
		return convert.Document{}, err
	}
	if len(data) == 0 {
		return convert.Document{}, fmt.Errorf("%w: empty file", convert.ErrCorrupt)
	}
	pkg, err := ooxml.Open(data, limits.MaxExpandedBytes)
	if err != nil {
		return convert.Document{}, err
	}
	root, err := pkg.RequirePart(ctx, "word/document.xml")
	if err != nil {
		return convert.Document{}, err
	}
	body := root.Child("document").Child("body")
	if body == nil {
		return convert.Document{}, fmt.Errorf("%w: word/document.xml has no body", convert.ErrCorrupt)
	}
	d := &document{ctx: ctx, limits: limits, ordered: map[string]map[string]bool{},
		links: map[string]string{}, styles: map[string]styleInfo{}, numAbstract: map[string]string{},
		styleLevels: map[string]map[string]string{}, output: limits.MaxOutputBytes}
	if err := d.load(ctx, pkg); err != nil {
		return convert.Document{}, err
	}
	w := mdwrite.New(limits.MaxOutputBytes)
	var blocks []string
	if err := d.blocks(body, &blocks); err != nil {
		return convert.Document{}, err
	}
	w.WriteString(strings.Join(blocks, "\n\n"))
	if err := w.Err(); err != nil {
		return convert.Document{}, err
	}
	if err := ctx.Err(); err != nil {
		return convert.Document{}, err
	}
	return convert.Document{Markdown: w.String()}, nil
}

// load 读取超链接地址、标题样式和列表格式。
func (d *document) load(ctx context.Context, pkg *ooxml.Package) error {
	relationships, err := pkg.Relationships(ctx, "word/document.xml")
	if err != nil {
		return err
	}
	for id, item := range relationships {
		if item.External {
			d.links[id] = item.Target
		}
	}
	styles, err := pkg.ReadPart(ctx, "word/styles.xml")
	if err != nil {
		return err
	}
	for _, style := range styles.Child("styles").Elements() {
		id := style.Attr("styleId")
		if style.Name != "style" || id == "" {
			continue
		}
		info := styleInfo{basedOn: style.Child("basedOn").Attr("val")}
		name := strings.ToLower(style.Child("name").Attr("val"))
		if name == "title" {
			info.heading = 1
		} else if number, found := strings.CutPrefix(name, "heading "); found {
			info.heading, _ = strconv.Atoi(number)
		}
		// 明确的大纲级别优先于样式名；级别 9 是正文。
		if outline, err := strconv.Atoi(style.Child("pPr").Child("outlineLvl").Attr("val")); err == nil {
			info.heading = noHeading
			if outline < 9 {
				info.heading = outline + 1
			}
		}
		list := style.Child("pPr").Child("numPr")
		info.numID, info.ilvl = list.Child("numId").Attr("val"), list.Child("ilvl").Attr("val")
		d.styles[id] = info
	}
	numbering, err := pkg.ReadPart(ctx, "word/numbering.xml")
	if err != nil {
		return err
	}
	abstract := map[string]map[string]bool{}
	for _, item := range numbering.Child("numbering").Elements() {
		switch item.Name {
		case "abstractNum":
			id := item.Attr("abstractNumId")
			levels, linked := map[string]bool{}, map[string]string{}
			for _, level := range item.Children {
				if level.Name == "lvl" {
					levels[level.Attr("ilvl")] = orderedFormat(level.Child("numFmt").Attr("val"))
					if style := level.Child("pStyle").Attr("val"); style != "" {
						linked[style] = level.Attr("ilvl")
					}
				}
			}
			abstract[id], d.styleLevels[id] = levels, linked
		case "num":
			id, base := item.Attr("numId"), item.Child("abstractNumId").Attr("val")
			d.numAbstract[id] = base
			// 级别覆盖只为这个实例替换抽象定义中的格式。
			levels := map[string]bool{}
			for ilvl, ordered := range abstract[base] {
				levels[ilvl] = ordered
			}
			for _, override := range item.Children {
				if override.Name == "lvlOverride" {
					if format := override.Child("lvl").Child("numFmt"); format != nil {
						levels[override.Attr("ilvl")] = orderedFormat(format.Attr("val"))
					}
				}
			}
			d.ordered[id] = levels
		}
	}
	return nil
}

// orderedFormat 报告编号格式是计数还是项目符号。
func orderedFormat(format string) bool {
	return format != "bullet" && format != "none" && format != ""
}

// styleHeading 返回样式或它所基于的样式的标题级别。
func (d *document) styleHeading(id string) int {
	for range maxStyleChain {
		info, ok := d.styles[id]
		if !ok {
			return 0
		}
		if info.heading == noHeading {
			return 0
		}
		if info.heading > 0 {
			return info.heading
		}
		id = info.basedOn
	}
	return 0
}

// styleNumbering 返回样式使用的编号实例和级别。沿 basedOn 链最近的 numId 生效；
// 级别也取链上最近的一个，抽象编号定义关联到某个样式的级别会替换该样式自己的 ilvl。
func (d *document) styleNumbering(id string) (string, string) {
	var chain []styleInfo
	var names []string
	numID := ""
	for current := id; current != "" && len(chain) < maxStyleChain; {
		info, ok := d.styles[current]
		if !ok {
			break
		}
		chain, names = append(chain, info), append(names, current)
		if numID == "" {
			numID = info.numID
		}
		current = info.basedOn
	}
	if numID == "" {
		return "", ""
	}
	linked := d.styleLevels[d.numAbstract[numID]]
	for i, info := range chain {
		if level := cmp.Or(linked[names[i]], info.ilvl); level != "" {
			return numID, level
		}
	}
	return numID, ""
}

// blocks 按文档顺序渲染段落和表格，并展开内容控件。
func (d *document) blocks(node *ooxml.Node, blocks *[]string) error {
	for _, child := range node.Children {
		if err := d.ctx.Err(); err != nil {
			return err
		}
		switch child.Name {
		case "p":
			if err := d.add(blocks, d.paragraph(child)); err != nil {
				return err
			}
		case "tbl":
			block, err := d.table(child)
			if err != nil {
				return err
			}
			if err := d.add(blocks, block); err != nil {
				return err
			}
		case "sectPr":
		default:
			if err := d.blocks(child, blocks); err != nil {
				return err
			}
		}
	}
	return nil
}

// add 追加一个非空的块。在表格单元格之外，块和它的分隔符在加入时就计入输出预算。
func (d *document) add(blocks *[]string, block string) error {
	if block == "" {
		return nil
	}
	if !d.inCell && d.output >= 0 {
		size := int64(len(block))
		if len(*blocks) > 0 {
			size += 2
		}
		if size > d.output {
			return &convert.LimitError{Limit: convert.LimitOutput, Max: d.limits.MaxOutputBytes}
		}
		d.output -= size
	}
	*blocks = append(*blocks, block)
	return nil
}

// paragraph 按样式、大纲级别和编号渲染一个段落。
func (d *document) paragraph(node *ooxml.Node) string {
	var builder strings.Builder
	d.text(node, &builder)
	text := strings.TrimSpace(builder.String())
	if text == "" {
		return ""
	}
	properties := node.Child("pPr")
	style := properties.Child("pStyle").Attr("val")
	level := d.styleHeading(style)
	if outline, err := strconv.Atoi(properties.Child("outlineLvl").Attr("val")); err == nil {
		level = 0
		if outline < 9 {
			level = outline + 1
		}
	}
	if level > 0 {
		return strings.Repeat("#", min(level, 6)) + " " + strings.Join(strings.Fields(text), " ")
	}
	if d.inCell {
		return text
	}
	// 段落直接设置的编号优先于样式的编号；numId 0 去掉编号，缺少级别时为 0。
	numID, ilvl := d.styleNumbering(style)
	if list := properties.Child("numPr"); list != nil {
		if id := list.Child("numId").Attr("val"); id != "" {
			numID = id
		}
		if value := list.Child("ilvl").Attr("val"); value != "" {
			ilvl = value
		}
	}
	if numID != "" && numID != "0" {
		if ilvl == "" {
			ilvl = "0"
		}
		depth, _ := strconv.Atoi(ilvl)
		marker := "- "
		if d.ordered[numID][ilvl] {
			marker = "1. "
		}
		// 每级三个空格，能够对齐 "1. " 和 "- " 两种上级列表项的内容。
		indent := strings.Repeat("   ", min(max(depth, 0), maxListDepth))
		return indent + marker + strings.ReplaceAll(mdwrite.Paragraph(text), "\n", "\n"+indent+"   ")
	}
	return mdwrite.Paragraph(text)
}

// text 追加文字、制表符和换行；外部超链接成为 Markdown 链接。
func (d *document) text(node *ooxml.Node, builder *strings.Builder) {
	for _, child := range node.Children {
		switch child.Name {
		case "t":
			builder.WriteString(child.Text)
		case "tab":
			builder.WriteString(" ")
		case "br", "cr":
			builder.WriteString("\n")
		case "hyperlink":
			target := d.links[child.Attr("r:id")]
			if target == "" || d.inLink {
				d.text(child, builder)
				continue
			}
			var label strings.Builder
			d.inLink = true
			d.text(child, &label)
			d.inLink = false
			text := strings.NewReplacer("[", `\[`, "]", `\]`).Replace(strings.Join(strings.Fields(label.String()), " "))
			builder.WriteString("[" + text + "](<" + linkTarget.Replace(target) + ">)")
		case "pPr", "rPr", "drawing", "pict", "object", "Fallback":
		default:
			d.text(child, builder)
		}
	}
}

// table 渲染表格；横向合并的单元格重复内容，纵向合并的单元格把值向下延续。
func (d *document) table(node *ooxml.Node) (string, error) {
	table := mdwrite.NewTableWithin(d.output, d.limits.MaxOutputBytes, true)
	var previous []string
	for _, row := range node.Children {
		if row.Name != "tr" {
			continue
		}
		var cells []string
		for _, cell := range row.Children {
			if cell.Name != "tc" {
				continue
			}
			var paragraphs []string
			outer := d.inCell
			d.inCell = true
			err := d.blocks(cell, &paragraphs)
			d.inCell = outer
			if err != nil {
				return "", err
			}
			text := strings.Join(paragraphs, " ")
			properties := cell.Child("tcPr")
			if merge := properties.Child("vMerge"); merge != nil && merge.Attr("val") != "restart" && len(cells) < len(previous) {
				text = previous[len(cells)]
			}
			span, err := strconv.Atoi(properties.Child("gridSpan").Attr("val"))
			if err != nil || span < 1 {
				span = 1
			}
			for range min(span, 64) {
				cells = append(cells, text)
			}
		}
		if err := table.Add(cells); err != nil {
			return "", err
		}
		previous = cells
	}
	return table.Markdown(), nil
}
