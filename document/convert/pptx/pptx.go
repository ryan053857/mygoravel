// Package pptx 把 PowerPoint 演示文稿（.pptx）转换为 Markdown。
//
// 每张幻灯片成为一个 convert.KindSlide 类的 Section，包括作为一级标题的标题占位符、文本框、表格、
// 以表格形式呈现的图表数据，以及演讲者备注。隐藏的幻灯片被跳过，除非设置了 Options.IncludeHidden；
// 它们仍然记录一个空的 Section，幻灯片编号与演示文稿一致。
package pptx

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ryan053857/mygoravel/document/convert"
	"github.com/ryan053857/mygoravel/document/internal/mdwrite"
	"github.com/ryan053857/mygoravel/document/internal/ooxml"
	"github.com/ryan053857/mygoravel/document/internal/source"
)

// Options 配置 PowerPoint 转换器，零值即默认配置。
type Options struct {
	convert.Limits
	// SkipNotes 不输出演讲者备注。
	SkipNotes bool
	// IncludeHidden 也转换隐藏的幻灯片。
	IncludeHidden bool
}

// Converter 转换 PowerPoint 演示文稿，可以并发使用。
type Converter struct {
	opts Options
}

// New 返回 PowerPoint 转换器。
func New(opts Options) *Converter {
	return &Converter{opts: opts}
}

// slide 渲染一张幻灯片的各个部分。
type slide struct {
	ctx           context.Context
	pkg           *ooxml.Package
	relationships map[string]ooxml.Relationship
	limits        convert.Limits
	title         string
	// output 是留给这张幻灯片的块及其分隔符的输出预算，负数表示不限制。
	output int64
}

// Convert 把 in 转换为 Markdown，每张幻灯片一个 Section。
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
	presentation, err := pkg.RequirePart(ctx, "ppt/presentation.xml")
	if err != nil {
		return convert.Document{}, err
	}
	if presentation.Child("presentation") == nil {
		return convert.Document{}, fmt.Errorf("%w: ppt/presentation.xml has no presentation", convert.ErrCorrupt)
	}
	relationships, err := pkg.Relationships(ctx, "ppt/presentation.xml")
	if err != nil {
		return convert.Document{}, err
	}
	w := mdwrite.New(limits.MaxOutputBytes)
	var sections []convert.Section
	number := 0
	for _, item := range presentation.Child("presentation").Child("sldIdLst").Elements() {
		if item.Name != "sldId" {
			continue
		}
		number++
		// 幻灯片可以使用它之前的分隔符之后剩下的预算。
		output := int64(-1)
		if limits.MaxOutputBytes >= 0 {
			output = limits.MaxOutputBytes - int64(w.Len())
			if w.Len() > 0 {
				output = max(output-2, 0)
			}
		}
		blocks, title, err := c.slide(ctx, pkg, relationships[item.Attr("r:id")].Target, limits, output)
		if err != nil {
			return convert.Document{}, err
		}
		// 相邻幻灯片的块之间用一个空行分隔，空行不属于任何一个 Section。
		if len(blocks) > 0 && w.Len() > 0 {
			w.WriteString("\n\n")
		}
		start := w.Len()
		w.WriteString(strings.Join(blocks, "\n\n"))
		if err := w.Err(); err != nil {
			return convert.Document{}, err
		}
		sections = append(sections, convert.Section{Kind: convert.KindSlide, Number: number, Name: title, Start: start, End: w.Len()})
	}
	if err := w.Err(); err != nil {
		return convert.Document{}, err
	}
	if err := ctx.Err(); err != nil {
		return convert.Document{}, err
	}
	return convert.Document{Markdown: w.String(), Sections: sections}, nil
}

// slide 在 output 字节内渲染 target 处的幻灯片，返回它的块和标题。
func (c *Converter) slide(ctx context.Context, pkg *ooxml.Package, target string, limits convert.Limits, output int64) ([]string, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	root, err := pkg.RequirePart(ctx, target)
	if err != nil {
		return nil, "", err
	}
	if root.Child("sld") == nil {
		return nil, "", fmt.Errorf("%w: %s is not a slide", convert.ErrCorrupt, target)
	}
	if show := root.Child("sld").Attr("show"); (show == "0" || show == "false") && !c.opts.IncludeHidden {
		return nil, "", nil
	}
	relationships, err := pkg.Relationships(ctx, target)
	if err != nil {
		return nil, "", err
	}
	s := &slide{ctx: ctx, pkg: pkg, relationships: relationships, limits: limits, output: output}
	var blocks []string
	if err := s.blocks(root.Child("sld").Child("cSld").Child("spTree"), &blocks); err != nil {
		return nil, "", err
	}
	if c.opts.SkipNotes {
		return blocks, s.title, nil
	}
	// 只有备注正文占位符是演讲者备注，幻灯片图像和编号不是。
	ids := make([]string, 0, len(relationships))
	for id := range relationships {
		ids = append(ids, id)
	}
	// rId2、rId10 这类关系编号先按长度、再按文字排序。
	slices.SortFunc(ids, func(a, b string) int { return cmp.Or(cmp.Compare(len(a), len(b)), strings.Compare(a, b)) })
	for _, id := range ids {
		item := relationships[id]
		if item.Type != "notesSlide" {
			continue
		}
		notes, err := pkg.RequirePart(ctx, item.Target)
		if err != nil {
			return nil, "", err
		}
		for _, shape := range notes.Child("notes").Child("cSld").Child("spTree").Elements() {
			if shape.Name == "sp" && shape.Child("nvSpPr").Child("nvPr").Child("ph").Attr("type") == "body" {
				if text := shapeText(shape); text != "" {
					if err := s.add(&blocks, mdwrite.Paragraph(text)); err != nil {
						return nil, "", err
					}
				}
			}
		}
	}
	return blocks, s.title, nil
}

// blocks 按形状顺序渲染文本框、表格和图表，并展开组合。
func (s *slide) blocks(tree *ooxml.Node, blocks *[]string) error {
	for _, shape := range tree.Elements() {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		switch shape.Name {
		case "sp":
			text := shapeText(shape)
			if text == "" {
				continue
			}
			if kind := shape.Child("nvSpPr").Child("nvPr").Child("ph").Attr("type"); kind == "title" || kind == "ctrTitle" {
				title := strings.Join(strings.Fields(text), " ")
				if s.title == "" {
					s.title = title
				}
				if err := s.add(blocks, "# "+title); err != nil {
					return err
				}
				continue
			}
			if err := s.add(blocks, mdwrite.Paragraph(text)); err != nil {
				return err
			}
		case "graphicFrame":
			data := shape.Child("graphic").Child("graphicData")
			if chart := data.Child("chart"); chart != nil {
				if err := s.chart(s.relationships[chart.Attr("r:id")].Target, blocks); err != nil {
					return err
				}
				continue
			}
			table := s.table(*blocks)
			for _, row := range data.Child("tbl").Elements() {
				if row.Name != "tr" {
					continue
				}
				var cells []string
				for _, cell := range row.Elements() {
					if cell.Name == "tc" {
						cells = append(cells, slideText(cell.Child("txBody")))
					}
				}
				if err := table.Add(cells); err != nil {
					return err
				}
			}
			if err := s.add(blocks, table.Markdown()); err != nil {
				return err
			}
		case "grpSp":
			if err := s.blocks(shape, blocks); err != nil {
				return err
			}
		case "AlternateContent":
			if err := s.blocks(shape.Child("Choice"), blocks); err != nil {
				return err
			}
		}
	}
	return nil
}

// add 追加一个非空的块，并把它和它的分隔符计入输出预算。
func (s *slide) add(blocks *[]string, block string) error {
	if block == "" {
		return nil
	}
	if s.output >= 0 {
		size := int64(len(block))
		if len(*blocks) > 0 {
			size += 2
		}
		if size > s.output {
			return &convert.LimitError{Limit: convert.LimitOutput, Max: s.limits.MaxOutputBytes}
		}
		s.output -= size
	}
	*blocks = append(*blocks, block)
	return nil
}

// table 返回一个表格，它的上限是把之前的分隔符加入 blocks 之后剩下的输出预算。
func (s *slide) table(blocks []string) *mdwrite.Table {
	left := s.output
	if left >= 0 && len(blocks) > 0 {
		left = max(left-2, 0)
	}
	return mdwrite.NewTableWithin(left, s.limits.MaxOutputBytes, true)
}

// chart 把图表的标题和数据加入 blocks。分类系列组成一个表格：第一列是分类，每个系列一列；
// 散点和气泡系列组成每个数据点一行的表格：系列、x、y 和大小。没有系列的图表什么也不加。
func (s *slide) chart(target string, blocks *[]string) error {
	root, err := s.pkg.RequirePart(s.ctx, target)
	if err != nil {
		return err
	}
	chart := root.Child("chartSpace").Child("chart")
	var categorySeries, pointSeries []*ooxml.Node
	for _, plot := range chart.Child("plotArea").Elements() {
		for _, series := range plot.Elements() {
			switch {
			case series.Name != "ser":
			case series.Child("val") != nil:
				categorySeries = append(categorySeries, series)
			case series.Child("yVal") != nil:
				pointSeries = append(pointSeries, series)
			}
		}
	}
	if len(categorySeries)+len(pointSeries) == 0 {
		return nil
	}
	if err := s.add(blocks, mdwrite.Paragraph(chartTitle(chart.Child("title")))); err != nil {
		return err
	}
	if len(categorySeries) > 0 {
		table, err := s.categoryTable(categorySeries, *blocks)
		if err != nil {
			return err
		}
		if err := s.add(blocks, table); err != nil {
			return err
		}
	}
	if len(pointSeries) > 0 {
		table, err := s.pointTable(pointSeries, *blocks)
		if err != nil {
			return err
		}
		if err := s.add(blocks, table); err != nil {
			return err
		}
	}
	return nil
}

// chartTitle 从富文本或缓存的字符串引用中取出图表标题。
func chartTitle(title *ooxml.Node) string {
	if text := strings.Join(strings.Fields(slideText(title)), " "); text != "" {
		return text
	}
	cache := cacheValues(title.Child("tx"))
	values := make([]string, 0, len(cache.values))
	for _, index := range cache.indices() {
		values = append(values, cache.values[index])
	}
	return strings.Join(values, " ")
}

// seriesName 从引用缓存或字面值中取出系列名称。
func seriesName(series *ooxml.Node) string {
	if names := cacheValues(series.Child("tx")); names.length > 0 {
		return names.values[0]
	}
	if value := series.Child("tx").Child("v"); value != nil {
		return value.Text
	}
	return ""
}

// categoryTable 渲染分类系列：第一列是分类，每个系列一列，任何一个系列有值的数据点序号占一行。
// 表格的上限是 blocks 之后剩下的输出预算。
func (s *slide) categoryTable(series []*ooxml.Node, blocks []string) (string, error) {
	header := []string{""}
	var columns []points
	var labels points
	for _, item := range series {
		header = append(header, seriesName(item))
		columns = append(columns, cacheValues(item.Child("val")))
		if labels.length == 0 {
			labels = cacheValues(item.Child("cat"))
		}
	}
	table := s.table(blocks)
	if err := table.SetHeader(header); err != nil {
		return "", err
	}
	for i, index := range union(append(columns, labels)) {
		if i%1024 == 0 {
			if err := s.ctx.Err(); err != nil {
				return "", err
			}
		}
		row := []string{labels.values[index]}
		for _, column := range columns {
			row = append(row, column.values[index])
		}
		if err := table.Add(row); err != nil {
			return "", err
		}
	}
	return table.Markdown(), nil
}

// pointTable 渲染散点和气泡系列，系列的 x、y 或大小有值的每个数据点序号占一行。
// 表格的上限是 blocks 之后剩下的输出预算。
func (s *slide) pointTable(series []*ooxml.Node, blocks []string) (string, error) {
	header := []string{"", "x", "y"}
	bubbles := slices.ContainsFunc(series, func(item *ooxml.Node) bool { return item.Child("bubbleSize") != nil })
	if bubbles {
		header = append(header, "size")
	}
	table := s.table(blocks)
	if err := table.SetHeader(header); err != nil {
		return "", err
	}
	for _, item := range series {
		name := seriesName(item)
		xs, ys, sizes := cacheValues(item.Child("xVal")), cacheValues(item.Child("yVal")), cacheValues(item.Child("bubbleSize"))
		for i, index := range union([]points{xs, ys, sizes}) {
			if i%1024 == 0 {
				if err := s.ctx.Err(); err != nil {
					return "", err
				}
			}
			row := []string{name, xs.values[index], ys.values[index]}
			if bubbles {
				row = append(row, sizes.values[index])
			}
			if err := table.Add(row); err != nil {
				return "", err
			}
		}
	}
	return table.Markdown(), nil
}

// maxPoints 限制从图表缓存读取的数据点序号。
const maxPoints = 1 << 16

// points 是图表缓存中按数据点序号排列的值。缺少的数据点读作空；length 是最大序号加一。
type points struct {
	values map[int]string
	length int
}

// indices 按递增顺序返回数据点序号。
func (p points) indices() []int {
	return union([]points{p})
}

// union 按递增顺序返回 caches 中任何一个有值的数据点序号。
func union(caches []points) []int {
	seen := map[int]bool{}
	var indices []int
	for _, cache := range caches {
		for index := range cache.values {
			if !seen[index] {
				seen[index] = true
				indices = append(indices, index)
			}
		}
	}
	slices.Sort(indices)
	return indices
}

// cacheValues 返回图表缓存中按数据点序号排列的值。嵌套缓存的数据点接在已经读到的之后，相当于追加。
func cacheValues(node *ooxml.Node) points {
	values := points{values: map[int]string{}}
	values.read(node)
	return values
}

// read 加入 node 下的数据点，序号从当前长度开始。
func (p *points) read(node *ooxml.Node) {
	origin := p.length
	for _, child := range node.Elements() {
		if child.Name != "pt" {
			p.read(child)
			continue
		}
		index, err := strconv.Atoi(child.Attr("idx"))
		if err != nil || index < 0 || index >= maxPoints {
			continue
		}
		p.length = max(p.length, origin+index+1)
		p.values[origin+index] = ""
		if value := child.Child("v"); value != nil {
			p.values[origin+index] = value.Text
		}
	}
}

// shapeText 返回形状中非空的段落，每段一行。
func shapeText(shape *ooxml.Node) string {
	var paragraphs []string
	for _, paragraph := range shape.Child("txBody").Elements() {
		if text := strings.TrimSpace(slideText(paragraph)); paragraph.Name == "p" && text != "" {
			paragraphs = append(paragraphs, text)
		}
	}
	return strings.Join(paragraphs, "\n")
}

// slideText 连接文字和域；换行成为换行符，段落之间用空格分隔。
func slideText(node *ooxml.Node) string {
	var builder strings.Builder
	writeSlideText(node, &builder)
	return builder.String()
}

// writeSlideText 把 slideText 对 node 返回的文字追加到 builder。
func writeSlideText(node *ooxml.Node, builder *strings.Builder) {
	for _, child := range node.Elements() {
		switch child.Name {
		case "t":
			builder.WriteString(child.Text)
		case "br":
			builder.WriteString("\n")
		case "p":
			writeSlideText(child, builder)
			builder.WriteString(" ")
		case "pPr", "rPr", "endParaRPr":
		default:
			writeSlideText(child, builder)
		}
	}
}
