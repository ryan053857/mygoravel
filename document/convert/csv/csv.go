// Package csv 把逗号分隔值转换为 GFM 表格。
//
// 空记录被跳过。剩下的第一条记录是表头，除非设置了 Options.NoHeader：此时每条记录都是正文，
// 放在一个同样宽度的空表头之下。较短的行补齐到最宽的记录。
package csv

import (
	"context"
	stdcsv "encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"

	"github.com/ryan053857/mygoravel/document/convert"
	"github.com/ryan053857/mygoravel/document/internal/mdwrite"
	"github.com/ryan053857/mygoravel/document/internal/source"
	"github.com/ryan053857/mygoravel/document/internal/textdecode"
)

// Options 配置 CSV 转换器，零值即默认配置。
type Options struct {
	convert.Limits
	// Fallback 解码既没有声明编码、又不是合法 UTF-8 的输入，例如 simplifiedchinese.GB18030。
	// nil 时把无效字节替换为 U+FFFD。
	Fallback encoding.Encoding
	// Comma 是字段分隔符，零值表示 ','。
	Comma rune
	// NoHeader 把第一条记录当作数据。
	NoHeader bool
	// LazyQuotes 允许不带引号的字段中出现引号，以及带引号的字段中出现未转义的引号。
	LazyQuotes bool
}

// Converter 转换 CSV，可以并发使用。
type Converter struct {
	opts Options
}

// New 返回 CSV 转换器。
func New(opts Options) *Converter {
	return &Converter{opts: opts}
}

// Convert 把 in 转换为 Markdown 表格。Comma 不合法属于配置错误，在读取之前报告。
func (c *Converter) Convert(ctx context.Context, in convert.Input) (convert.Document, error) {
	if c.opts.Comma != 0 && (c.opts.Comma == '"' || c.opts.Comma == '\r' || c.opts.Comma == '\n' ||
		c.opts.Comma == utf8.RuneError || !utf8.ValidRune(c.opts.Comma)) {
		return convert.Document{}, fmt.Errorf("csv: invalid Comma %q", c.opts.Comma)
	}
	limits := c.opts.Effective()
	data, err := source.Read(ctx, in, limits.MaxBytes)
	if err != nil {
		return convert.Document{}, err
	}
	reader := stdcsv.NewReader(strings.NewReader(textdecode.Normalize(textdecode.Decode(data, in.Charset, c.opts.Fallback))))
	reader.FieldsPerRecord, reader.LazyQuotes, reader.ReuseRecord = -1, c.opts.LazyQuotes, true
	if c.opts.Comma != 0 {
		reader.Comma = c.opts.Comma
	}
	table := mdwrite.NewTable(limits.MaxOutputBytes, !c.opts.NoHeader)
	for count := 0; ; count++ {
		if count%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return convert.Document{}, err
			}
		}
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return convert.Document{}, fmt.Errorf("%w: %w", convert.ErrCorrupt, err)
		}
		if err := table.Add(record); err != nil {
			return convert.Document{}, err
		}
	}
	w := mdwrite.New(limits.MaxOutputBytes)
	w.WriteString(table.Markdown())
	if err := w.Err(); err != nil {
		return convert.Document{}, err
	}
	return convert.Document{Markdown: w.String()}, nil
}
