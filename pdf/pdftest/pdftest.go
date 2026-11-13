// Package pdftest 提供记录 HTML 而不真正生成 PDF 的 pdf.Renderer，用于测试。
package pdftest

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/cockroachdb/errors"

	"github.com/go-tenon/tenon/pdf"
)

// Renderer 渲染并记录每份文档的 HTML，返回一页空白的 PDF，可并发使用。
type Renderer struct {
	mu       sync.Mutex
	rendered []string
	err      error
}

var _ pdf.Renderer = (*Renderer)(nil)

// New 创建一个空的 Renderer。
func New() *Renderer { return &Renderer{} }

// Render 渲染 doc.HTML 并记录，返回 Blank；设置了 FailWith 时返回该错误且不记录。
func (r *Renderer) Render(ctx context.Context, doc pdf.Document) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if doc.HTML == nil {
		return nil, errors.New("pdftest: document has no HTML")
	}
	var html bytes.Buffer
	if err := doc.HTML.Render(ctx, &html); err != nil {
		return nil, errors.Wrap(err, "pdftest: render html")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	r.rendered = append(r.rendered, html.String())
	return slices.Clone(Blank), nil
}

// FailWith 让之后的 Render 都返回 err；传 nil 恢复正常。
func (r *Renderer) FailWith(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// Rendered 返回已记录的 HTML，按生成顺序排列。
func (r *Renderer) Rendered() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.rendered)
}

// Last 返回最后一份 HTML；还没有时返回空字符串和 false。
func (r *Renderer) Last() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.rendered) == 0 {
		return "", false
	}
	return r.rendered[len(r.rendered)-1], true
}

// Reset 清空记录并取消 FailWith。
func (r *Renderer) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rendered, r.err = nil, nil
}

// Blank 是一页空白的 A4 PDF，Render 返回它的副本。
var Blank = blank()

func blank() []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, obj := range objects {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return b.Bytes()
}
