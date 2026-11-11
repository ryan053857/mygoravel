// Package xlsx 把 Excel 工作簿（.xlsx）转换为 Markdown。
//
// 每个工作表成为一个 convert.KindSheet 类的 Section，包括作为二级标题的工作表名，以及第一个非空行
// 为表头的 GFM 表格。单元格显示按数字格式格式化后的值，除非设置了 Options.RawValues；
// 合并区域的值保留在它的第一个单元格中。隐藏的工作表被跳过，除非设置了 Options.IncludeHidden；
// 跳过的和空的工作表保留一个空的 Section，工作表编号与工作簿一致。
package xlsx

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/xuri/excelize/v2"

	"github.com/ryan053857/mygoravel/document/convert"
	"github.com/ryan053857/mygoravel/document/internal/mdwrite"
	"github.com/ryan053857/mygoravel/document/internal/ooxml"
	"github.com/ryan053857/mygoravel/document/internal/source"
)

// Options 配置 Excel 转换器，零值即默认配置。
type Options struct {
	convert.Limits
	// RawValues 显示单元格中保存的原始值，而不是按数字格式格式化后的值。
	RawValues bool
	// IncludeHidden 也转换隐藏的工作表。
	IncludeHidden bool
}

// Converter 转换 Excel 工作簿，可以并发使用。
type Converter struct {
	opts Options
}

// New 返回 Excel 转换器。
func New(opts Options) *Converter {
	return &Converter{opts: opts}
}

// Convert 把 in 转换为 Markdown，每个工作表一个 Section。
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
	if err := checkExpanded(pkg.Files(), limits.MaxExpandedBytes); err != nil {
		return convert.Document{}, err
	}
	// 行迭代器会跳过某些格式错误的 XML，所以先检查每个 XML 部件。
	parts, err := pkg.XMLParts(ctx)
	if err != nil {
		return convert.Document{}, err
	}
	for _, name := range parts {
		if err := pkg.CheckXML(ctx, name); err != nil {
			return convert.Document{}, err
		}
	}
	return c.render(ctx, data, limits)
}

// render 转换检查过的工作簿。表格库中的 panic 报告为 convert.ErrCorrupt。
func (c *Converter) render(ctx context.Context, data []byte, limits convert.Limits) (doc convert.Document, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			doc, err = convert.Document{}, fmt.Errorf("%w: %v", convert.ErrCorrupt, recovered)
		}
	}()
	// 表格库把大的部件解压到临时文件；每次转换使用一个目录，无论打开是否成功都会删除它们。
	dir, err := os.MkdirTemp("", "document-xlsx-")
	if err != nil {
		return convert.Document{}, fmt.Errorf("xlsx: temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	file, err := excelize.OpenReader(bytes.NewReader(data), excelize.Options{TmpDir: dir})
	if file != nil {
		defer func() { _ = file.Close() }()
	}
	if err != nil {
		return convert.Document{}, fmt.Errorf("%w: %w", convert.ErrCorrupt, err)
	}
	w := mdwrite.New(limits.MaxOutputBytes)
	var sections []convert.Section
	for number, sheet := range file.GetSheetList() {
		// 一个工作表的表格可以使用它之前的分隔符和工作表标题之后剩下的输出上限。
		left := int64(-1)
		if limits.MaxOutputBytes >= 0 {
			left = limits.MaxOutputBytes - int64(w.Len()+len("## "+mdwrite.Cell(sheet)+"\n\n"))
			if w.Len() > 0 {
				left -= 2
			}
			left = max(left, 0)
		}
		table, err := c.sheet(ctx, file, sheet, mdwrite.NewTableWithin(left, limits.MaxOutputBytes, true))
		if err != nil {
			return convert.Document{}, err
		}
		if table != "" && w.Len() > 0 {
			w.WriteString("\n\n")
		}
		start := w.Len()
		if table != "" {
			w.WriteString("## " + mdwrite.Cell(sheet) + "\n\n" + table)
		}
		if err := w.Err(); err != nil {
			return convert.Document{}, err
		}
		sections = append(sections, convert.Section{Kind: convert.KindSheet, Number: number + 1, Name: sheet, Start: start, End: w.Len()})
	}
	if err := w.Err(); err != nil {
		return convert.Document{}, err
	}
	return convert.Document{Markdown: w.String(), Sections: sections}, nil
}

// checkExpanded 在各条目声明的大小之和超过 maxExpanded 时返回 expanded 类的 LimitError；
// maxExpanded < 0 表示不限制。zip 读取器保证任何条目解压后都不会超过声明的大小。
func checkExpanded(entries []*zip.File, maxExpanded int64) error {
	if maxExpanded < 0 {
		return nil
	}
	limit, expanded := uint64(maxExpanded), uint64(0)
	for _, entry := range entries {
		if entry.UncompressedSize64 > limit-expanded {
			return &convert.LimitError{Limit: convert.LimitExpanded, Max: maxExpanded}
		}
		expanded += entry.UncompressedSize64
	}
	return nil
}

// sheet 把一个工作表渲染到 table；工作表隐藏或为空时返回 ""。
func (c *Converter) sheet(ctx context.Context, file *excelize.File, sheet string, table *mdwrite.Table) (string, error) {
	// 读不出可见性的工作表按可见处理。
	if visible, err := file.GetSheetVisible(sheet); err == nil && !visible && !c.opts.IncludeHidden {
		return "", nil
	}
	rows, err := file.Rows(sheet)
	if err != nil {
		return "", fmt.Errorf("%w: %w", convert.ErrCorrupt, err)
	}
	defer func() { _ = rows.Close() }()
	for count := 0; rows.Next(); count++ {
		if count%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		row, err := rows.Columns(excelize.Options{RawCellValue: c.opts.RawValues})
		if err != nil {
			return "", fmt.Errorf("%w: %w", convert.ErrCorrupt, err)
		}
		if err := table.Add(row); err != nil {
			return "", err
		}
	}
	if err := rows.Error(); err != nil {
		return "", fmt.Errorf("%w: %w", convert.ErrCorrupt, err)
	}
	return table.Markdown(), nil
}
