package pdftest_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ryan053857/mygoravel/document/convert"
	convertpdf "github.com/ryan053857/mygoravel/document/convert/pdf"
	"github.com/ryan053857/mygoravel/pdf"
	"github.com/ryan053857/mygoravel/pdf/pdftest"
)

func html(s string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, s)
		return err
	})
}

func TestRenderer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := pdftest.New()
	_, ok := r.Last()
	assert.False(t, ok)

	data, err := r.Render(ctx, pdf.Document{HTML: html("<h1>发票 1</h1>")})
	require.NoError(t, err)
	assert.Equal(t, pdftest.Blank, data)
	data[0] = 'x'
	assert.Equal(t, byte('%'), pdftest.Blank[0], "Render must return a copy")

	_, err = r.Render(ctx, pdf.Document{HTML: html("<h1>发票 2</h1>")})
	require.NoError(t, err)
	assert.Equal(t, []string{"<h1>发票 1</h1>", "<h1>发票 2</h1>"}, r.Rendered())
	last, ok := r.Last()
	assert.True(t, ok)
	assert.Equal(t, "<h1>发票 2</h1>", last)

	boom := errors.New("boom")
	r.FailWith(boom)
	_, err = r.Render(ctx, pdf.Document{HTML: html("x")})
	assert.ErrorIs(t, err, boom)
	assert.Len(t, r.Rendered(), 2)

	r.Reset()
	assert.Empty(t, r.Rendered())
	_, err = r.Render(ctx, pdf.Document{HTML: html("x")})
	assert.NoError(t, err)

	_, err = r.Render(ctx, pdf.Document{})
	assert.Error(t, err)
}

func TestBlankIsValidPDF(t *testing.T) {
	t.Parallel()
	c := convertpdf.New(convertpdf.Options{})
	t.Cleanup(func() { c.Close() })
	doc, err := c.Convert(context.Background(), convert.Input{Reader: bytes.NewReader(pdftest.Blank)})
	require.NoError(t, err)
	assert.Len(t, doc.Sections, 1)
}
