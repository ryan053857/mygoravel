// Package mdwrite 在输出上限内生成转换器的输出。
package mdwrite

import (
	"strings"

	"github.com/ryan053857/mygoravel/document/convert"
)

// Writer 累积 Markdown。超出输出上限后保留错误，忽略之后的写入。
type Writer struct {
	b   strings.Builder
	max int64
	err error
}

// New 返回最多写入 maxBytes 字节的 Writer，maxBytes < 0 表示不限制。
func New(maxBytes int64) *Writer {
	return &Writer{max: maxBytes}
}

// WriteString 追加 s。
func (w *Writer) WriteString(s string) {
	if w.err != nil {
		return
	}
	if w.max >= 0 && int64(w.b.Len()+len(s)) > w.max {
		w.err = &convert.LimitError{Limit: convert.LimitOutput, Max: w.max}
		return
	}
	w.b.WriteString(s)
}

// Len 返回已经写入的字节数。
func (w *Writer) Len() int {
	return w.b.Len()
}

// Err 返回超出上限的错误，没有时为 nil。
func (w *Writer) Err() error {
	return w.err
}

// String 返回已经写入的 Markdown。
func (w *Writer) String() string {
	return w.b.String()
}

// Fence 把 content 写进代码围栏，围栏比内容中最长的反引号串更长。写入之前先检查上限。
func (w *Writer) Fence(info, content string) {
	if w.err != nil {
		return
	}
	longest, run := 0, 0
	for i := 0; i < len(content); i++ {
		if content[i] == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fenceLen := max(3, longest+1)
	newline := ""
	if !strings.HasSuffix(content, "\n") {
		newline = "\n"
	}
	if w.err == nil && w.max >= 0 && int64(w.b.Len()+2*fenceLen+len(info)+1+len(content)+len(newline)) > w.max {
		w.err = &convert.LimitError{Limit: convert.LimitOutput, Max: w.max}
		return
	}
	fence := strings.Repeat("`", fenceLen)
	w.WriteString(fence + info + "\n")
	w.WriteString(content)
	w.WriteString(newline + fence)
}
