// Package sheet 读写 CSV 和 Excel（XLSX）表格，用于导入和导出数据。
//
// 导入时，Decode 按结构体的 json 标签和 sheet 标签找到每个字段所在的列，
// 用 form.Decode 按 Huma 标签验证每一行，规则与 JSON 接口和页面表单相同：
//
//	type Changes struct {
//		Title string `json:"title" sheet:"标题" minLength:"1" maxLength:"200"`
//		Slug  string `json:"slug" sheet:"Slug,链接" maxLength:"100"`
//	}
//
//	r, err := sheet.NewReader(file, sheet.XLSX)
//	defer r.Close()
//	records, err := sheet.Decode[Changes](ctx, r)
//	for rec, err := range records {
//		// rec.Line、rec.Data、rec.Errors
//	}
//
// 导出时，Columns 描述每一列，Writer 边写边输出，不需要把整张表放在内存中：
//
//	w := sheet.NewWriter(out, sheet.XLSX)
//	w.Write(columns.Header()...)
//	for _, item := range items {
//		w.Write(columns.Row(item)...)
//	}
//	err := w.Close()
package sheet

import (
	"path"
	"strings"

	"github.com/go-tenon/tenon/errorx"
)

var (
	// ErrFormat 表示不支持的文件类型，只支持 .csv 和 .xlsx。
	ErrFormat = errorx.Define(errorx.InvalidArgument, "sheet.unsupported_format")
	// ErrBadFile 表示文件无法按表格读取，例如损坏的 XLSX 或引号不配对的 CSV。
	ErrBadFile = errorx.Define(errorx.InvalidArgument, "sheet.bad_file")
	// ErrNoHeader 表示文件中没有表头。
	ErrNoHeader = errorx.Define(errorx.InvalidArgument, "sheet.no_header")
	// ErrMissingColumns 表示缺少必填字段对应的列，公开参数 columns 是缺少的列名，以顿号分隔。
	ErrMissingColumns = errorx.Define(errorx.InvalidArgument, "sheet.missing_columns")
)

// Format 是表格文件的格式，值就是文件扩展名。
type Format string

const (
	CSV  Format = "csv"
	XLSX Format = "xlsx"
)

// FormatOf 按文件名的扩展名返回格式（不区分大小写），不是 .csv 或 .xlsx 时返回 ErrFormat。
func FormatOf(filename string) (Format, error) {
	switch f := Format(strings.ToLower(strings.TrimPrefix(path.Ext(filename), "."))); f {
	case CSV, XLSX:
		return f, nil
	}
	return "", ErrFormat.New()
}

// ContentType 返回格式的 MIME 类型。
func (f Format) ContentType() string {
	if f == XLSX {
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	}
	return "text/csv; charset=utf-8"
}
