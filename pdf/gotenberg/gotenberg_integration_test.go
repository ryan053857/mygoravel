package gotenberg_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ryan053857/mygoravel/document/convert"
	convertpdf "github.com/ryan053857/mygoravel/document/convert/pdf"
	"github.com/ryan053857/mygoravel/pdf"
	"github.com/ryan053857/mygoravel/pdf/gotenberg"
)

func openGotenberg(t *testing.T) *gotenberg.Renderer {
	t.Helper()
	url := os.Getenv("GOTENBERG_TEST_URL")
	if url == "" {
		t.Fatal("GOTENBERG_TEST_URL is required for this test")
	}
	r, err := gotenberg.New(gotenberg.Config{URL: url})
	require.NoError(t, err)
	return r
}

var mediaBox = regexp.MustCompile(`/MediaBox \[0 0 ([0-9.]+) ([0-9.]+)\]`)

// pageSize 返回第一页的宽和高，单位 pt。
func pageSize(t *testing.T, data []byte) (float64, float64) {
	t.Helper()
	m := mediaBox.FindSubmatch(data)
	require.NotNil(t, m, "no MediaBox in PDF")
	var w, h float64
	_, err := fmt.Sscan(string(m[1])+" "+string(m[2]), &w, &h)
	require.NoError(t, err)
	return w, h
}

func text(t *testing.T, data []byte) (string, int) {
	t.Helper()
	c := convertpdf.New(convertpdf.Options{NoHeadings: true})
	t.Cleanup(func() { c.Close() })
	doc, err := c.Convert(context.Background(), convert.Input{Reader: bytes.NewReader(data)})
	require.NoError(t, err)
	return doc.Markdown, len(doc.Sections)
}

func TestGotenbergPagedMedia(t *testing.T) {
	r := openGotenberg(t)
	var rows strings.Builder
	for range 80 {
		rows.WriteString("<tr><td>会员服务（年付）</td><td>¥ 99.00</td></tr>")
	}
	data, err := r.Render(context.Background(), pdf.Document{
		HTML: html(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>发票 INV-001</title>
<link rel="stylesheet" href="invoice.css"></head><body><img src="logo.svg"><h1>发票 INV-001</h1>
<table><tbody>` + rows.String() + `</tbody></table></body></html>`),
		Assets: fstest.MapFS{
			"invoice.css": {Data: []byte(`@page { size: A5 landscape; margin: 20mm 15mm;
  @bottom-center { content: "第 " counter(page) " 页，共 " counter(pages) " 页"; } }`)},
			"logo.svg": {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"/>`)},
		},
	})
	require.NoError(t, err)

	w, h := pageSize(t, data)
	assert.InDelta(t, 595, w, 2, "A5 landscape width")
	assert.InDelta(t, 420, h, 2, "A5 landscape height")

	md, pages := text(t, data)
	assert.Greater(t, pages, 1)
	assert.Contains(t, md, "发票 INV-001")
	assert.Contains(t, md, "会员服务（年付）")
	assert.Contains(t, md, "第 1 页，共")
	assert.Contains(t, string(data), "/Title")
}

func TestGotenbergDefaultsToA4(t *testing.T) {
	r := openGotenberg(t)
	data, err := r.Render(context.Background(), pdf.Document{HTML: html(`<!doctype html><meta charset="utf-8"><p>你好</p>`)})
	require.NoError(t, err)
	w, h := pageSize(t, data)
	assert.InDelta(t, 595, w, 2)
	assert.InDelta(t, 842, h, 2)
}

func TestGotenbergMissingAsset(t *testing.T) {
	r := openGotenberg(t)
	_, err := r.Render(context.Background(), pdf.Document{
		HTML: html(`<!doctype html><link rel="stylesheet" href="missing.css"><p>x</p>`),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409")
	assert.Contains(t, err.Error(), "ERR_FILE_NOT_FOUND")
	assert.NotErrorIs(t, err, pdf.ErrUnavailable)
}
