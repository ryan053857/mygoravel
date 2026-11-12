package docs

import (
	"bytes"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	gmtext "github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// newMarkdown 返回解析和渲染文档的 goldmark：GFM、自动生成的标题 ID、{#id} 属性，
// 以及本包的代码块和提示块渲染。html 为 true 时原样输出 Markdown 中的 HTML。
func newMarkdown(rawHTML bool) goldmark.Markdown {
	opts := []renderer.Option{renderer.WithNodeRenderers(util.Prioritized(nodeRenderer{}, 100))}
	if rawHTML {
		opts = append(opts, html.WithUnsafe())
	}
	return goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID(), parser.WithAttribute()),
		goldmark.WithRendererOptions(opts...),
	)
}

// parse 解析 src，标题 ID 在这一篇中唯一，中文等非 ASCII 字符保留在 ID 中。
func parse(md goldmark.Markdown, src []byte) ast.Node {
	ctx := parser.NewContext(parser.WithIDs(&headingIDs{used: map[string]bool{}}))
	return md.Parser().Parse(gmtext.NewReader(src), parser.WithContext(ctx))
}

// headingIDs 生成标题 ID：字母和数字转为小写保留（包括中文），空白和连字符变成 -，其余标点去掉；
// 重复时依次加 -1、-2。与 GitHub 的规则相近。
type headingIDs struct{ used map[string]bool }

func (s *headingIDs) Generate(value []byte, _ ast.NodeKind) []byte {
	var b strings.Builder
	for _, r := range strings.TrimSpace(string(value)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r):
			b.WriteRune(unicode.ToLower(r))
		case r == '-' || r == '_':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte('-')
		}
	}
	base := b.String()
	if base == "" {
		base = "section"
	}
	id := base
	for i := 1; s.used[id]; i++ {
		id = base + "-" + strconv.Itoa(i)
	}
	s.used[id] = true
	return []byte(id)
}

func (s *headingIDs) Put(value []byte) { s.used[string(value)] = true }

// heading 是本页目录中的一项。
type heading struct {
	Level int
	ID    string
	Text  string
}

// takeTitle 去掉文档开头的一级标题并返回它的文字；开头不是一级标题时返回空。
func takeTitle(doc ast.Node, src []byte) string {
	h, ok := doc.FirstChild().(*ast.Heading)
	if !ok || h.Level != 1 {
		return ""
	}
	doc.RemoveChild(doc, h)
	return plainText(h, src)
}

// outline 收集二、三级标题作为本页目录，并在二到四级标题末尾加上指向自己的锚点链接。
func outline(doc ast.Node, src []byte) []heading {
	var toc []heading
	var headings []*ast.Heading
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if h, ok := n.(*ast.Heading); ok && entering {
			headings = append(headings, h)
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	for _, h := range headings {
		v, ok := h.AttributeString("id")
		id, _ := v.([]byte)
		if !ok || len(id) == 0 || h.Level < 2 || h.Level > 4 {
			continue
		}
		if h.Level <= 3 {
			toc = append(toc, heading{Level: h.Level, ID: string(id), Text: plainText(h, src)})
		}
		link := ast.NewLink()
		link.Destination = append([]byte("#"), id...)
		link.SetAttributeString("class", []byte("docs-anchor"))
		link.AppendChild(link, ast.NewString([]byte("#")))
		h.AppendChild(h, link)
	}
	return toc
}

// plainText 返回 n 中的文字，不含 Markdown 标记。
func plainText(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := c.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(src))
			if t.SoftLineBreak() || t.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(t.Value)
		case *ast.RawHTML:
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(b.String())
}

// alertKinds 是 GitHub 风格的提示块：> [!NOTE] 等，独占引用块的第一行。
var alertKinds = []string{"note", "tip", "important", "warning", "caution"}

var kindAlert = ast.NewNodeKind("DocsAlert")

// alert 是由引用块转换而来的提示块。
type alert struct {
	ast.BaseBlock
	kind string
}

func (n *alert) Kind() ast.NodeKind { return kindAlert }

func (n *alert) Dump(src []byte, level int) {
	ast.DumpHelper(n, src, level, map[string]string{"Kind": n.kind}, nil)
}

// alerts 把第一行是 [!NOTE]、[!TIP]、[!IMPORTANT]、[!WARNING]、[!CAUTION] 的引用块转换为提示块。
func alerts(doc ast.Node, src []byte) {
	var quotes []*ast.Blockquote
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if q, ok := n.(*ast.Blockquote); ok && entering {
			quotes = append(quotes, q)
		}
		return ast.WalkContinue, nil
	})
	for _, q := range quotes {
		p, ok := q.FirstChild().(*ast.Paragraph)
		if !ok || p.Lines().Len() == 0 {
			continue
		}
		first := p.Lines().At(0)
		marker := strings.ToLower(strings.TrimSpace(string(first.Value(src))))
		kind, ok := strings.CutPrefix(marker, "[!")
		kind, closed := strings.CutSuffix(kind, "]")
		if !ok || !closed || !slices.Contains(alertKinds, kind) {
			continue
		}
		// 去掉第一行的文字节点（[、!NOTE、]），只剩标记时去掉整个段落。
		for c := p.FirstChild(); c != nil; {
			next := c.NextSibling()
			t, ok := c.(*ast.Text)
			if !ok || t.Segment.Start >= first.Stop {
				break
			}
			p.RemoveChild(p, c)
			c = next
		}
		if p.FirstChild() == nil {
			q.RemoveChild(q, p)
		}
		a := &alert{kind: kind}
		for c := q.FirstChild(); c != nil; {
			next := c.NextSibling()
			a.AppendChild(a, c)
			c = next
		}
		q.Parent().ReplaceChild(q.Parent(), q, a)
	}
}

// nodeRenderer 渲染代码块和提示块。
type nodeRenderer struct{}

func (nodeRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindFencedCodeBlock, renderCode)
	reg.Register(ast.KindCodeBlock, renderCode)
	reg.Register(kindAlert, renderAlert)
}

// formatter 输出带 class 的高亮代码，颜色来自 web/ui 样式表中的 chroma 样式，随暗色模式切换。
var formatter = chromahtml.New(chromahtml.WithClasses(true))

// copyIcon 是代码块的复制按钮中的图标：复制，以及复制后显示的对勾。
const copyIcon = `<svg class="docs-copy-idle" xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect width="14" height="14" x="8" y="8" rx="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>` +
	`<svg class="docs-copy-done" xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg>`

// renderCode 渲染代码块：右上角是复制按钮；围栏代码块写了 chroma 认识的语言时高亮。
func renderCode(w util.BufWriter, src []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	var code bytes.Buffer
	for i := 0; i < node.Lines().Len(); i++ {
		line := node.Lines().At(i)
		code.Write(line.Value(src))
	}
	var lexer chroma.Lexer
	if f, ok := node.(*ast.FencedCodeBlock); ok {
		if lang := f.Language(src); len(lang) > 0 {
			lexer = lexers.Get(string(lang))
		}
	}
	_, _ = w.WriteString(`<div class="docs-code"><button type="button" class="docs-copy" data-docs-copy aria-label="` + label("docs.copy") + `">` + copyIcon + "</button>")
	highlighted := false
	if lexer != nil {
		if it, err := chroma.Coalesce(lexer).Tokenise(nil, code.String()); err == nil {
			highlighted = formatter.Format(w, styles.Fallback, it) == nil
		}
	}
	if !highlighted {
		_, _ = w.WriteString(`<pre class="chroma"><code>`)
		_, _ = w.Write(util.EscapeHTML(code.Bytes()))
		_, _ = w.WriteString("</code></pre>")
	}
	_, _ = w.WriteString("</div>\n")
	return ast.WalkSkipChildren, nil
}

func renderAlert(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*alert)
	if entering {
		_, _ = w.WriteString(`<div class="docs-alert" data-kind="` + n.kind + `"><p class="docs-alert-title">` + label("docs.alert."+n.kind) + "</p>\n")
	} else {
		_, _ = w.WriteString("</div>\n")
	}
	return ast.WalkContinue, nil
}
