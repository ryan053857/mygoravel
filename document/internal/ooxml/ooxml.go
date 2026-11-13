// Package ooxml 在展开上限内读取 Office Open XML 包。
package ooxml

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/go-tenon/tenon/document/convert"
)

// relationshipNamespaces 是 Transitional 和 Strict 文档中关系属性的命名空间；这类属性的键带 "r:" 前缀。
var relationshipNamespaces = map[string]bool{
	"http://schemas.openxmlformats.org/officeDocument/2006/relationships": true,
	"http://purl.oclc.org/ooxml/officeDocument/relationships":             true,
}

// utf8BOM 可能出现在 XML 部件的开头。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// oleMagic 是复合文档二进制格式的开头，加密的 OOXML 和旧的 Office 格式使用这种格式。
var oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// encryptionInfo 是加密的 OOXML 容器中那个流的 UTF-16LE 名称。
var encryptionInfo = []byte("E\x00n\x00c\x00r\x00y\x00p\x00t\x00i\x00o\x00n\x00I\x00n\x00f\x00o\x00")

// MaxDepth 是部件中元素允许的最深嵌套，更深的部件报告为 convert.ErrCorrupt。Office 软件的嵌套远比它浅。
const MaxDepth = 256

// ElementCost 是解析后的部件中每个元素在自身字节之外计入展开上限的量。
// 一个解析后的元素大约占用它的两倍，所以解析后的树最多约为上限的两倍。
const ElementCost = 64

// Node 是一个使用本地名称的 XML 元素；Text 是它直接包含的字符数据。
type Node struct {
	Name     string
	attrs    []attr
	Children []*Node
	Text     string
}

// attr 是以本地名称为键的属性，关系属性的键是 "r:" 加本地名称。
type attr struct {
	key, value string
}

// Child 返回第一个名为 name 的子元素，没有时返回 nil。
func (n *Node) Child(name string) *Node {
	if n == nil {
		return nil
	}
	for _, child := range n.Children {
		if child.Name == name {
			return child
		}
	}
	return nil
}

// Elements 返回子元素，节点为 nil 时返回 nil。
func (n *Node) Elements() []*Node {
	if n == nil {
		return nil
	}
	return n.Children
}

// Attr 返回属性值，节点为 nil 时返回 ""。
func (n *Node) Attr(name string) string {
	if n == nil {
		return ""
	}
	// 重复的属性以最后一个为准。
	for i := len(n.attrs) - 1; i >= 0; i-- {
		if n.attrs[i].key == name {
			return n.attrs[i].value
		}
	}
	return ""
}

// Relationship 是部件的一个关系；Type 是类型 URI 的最后一段，内部目标是包内的绝对路径。
type Relationship struct {
	Type     string
	Target   string
	External bool
}

// Package 是一个打开的 OOXML 包。
type Package struct {
	archive *zip.Reader
	max     int64
	budget  *budget // 不限制时为 nil
}

// budget 是展开上限的剩余量。
type budget struct {
	max, left int64
}

// spend 从 b 中扣除 n，用量超过上限时返回 expanded 类的 *convert.LimitError。nil 表示不限制。
func (b *budget) spend(n int64) error {
	if b == nil {
		return nil
	}
	if n > b.left {
		b.left = -1
		return &convert.LimitError{Limit: convert.LimitExpanded, Max: b.max}
	}
	b.left -= n
	return nil
}

// meteredReader 把读到的字节计入预算，并保留第一个超限错误。
type meteredReader struct {
	r      io.Reader
	budget *budget
	err    error
}

// Read 从底层 reader 读取，并为返回的字节计数。
func (m *meteredReader) Read(p []byte) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	n, err := m.r.Read(p)
	if spendErr := m.budget.spend(int64(n)); spendErr != nil {
		m.err = spendErr
		return n, spendErr
	}
	return n, err
}

// Open 把 data 作为 OOXML 包打开，maxExpanded < 0 表示不限制。加密的文档返回 convert.ErrEncrypted，
// 旧的二进制 Office 文件返回 convert.ErrUnsupported；其他不是 zip 的内容，或者有两个条目的名字
// 只在大小写或分隔符上不同的，返回 convert.ErrCorrupt。
func Open(data []byte, maxExpanded int64) (*Package, error) {
	if bytes.HasPrefix(data, oleMagic) {
		if bytes.Contains(data, encryptionInfo) {
			return nil, convert.ErrEncrypted
		}
		return nil, fmt.Errorf("%w: legacy binary Office format", convert.ErrUnsupported)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", convert.ErrCorrupt, err)
	}
	// 不同的读取器对同名的两个条目以哪个为准并不一致，所以重复的条目视为损坏。
	names := map[string]bool{}
	for _, entry := range archive.File {
		name := strings.ToLower(strings.ReplaceAll(entry.Name, `\`, "/"))
		if names[name] {
			return nil, fmt.Errorf("%w: duplicate part %s", convert.ErrCorrupt, entry.Name)
		}
		names[name] = true
	}
	pkg := &Package{archive: archive, max: maxExpanded}
	if maxExpanded >= 0 {
		pkg.budget = &budget{max: maxExpanded, left: maxExpanded}
	}
	return pkg, nil
}

// Files 返回包中的条目。
func (p *Package) Files() []*zip.File {
	return p.archive.File
}

// ReadPart 解析名为 name 的 XML 部件，不存在时返回 nil。它的字节和元素计入展开上限；
// 每读一批记号检查一次取消。
func (p *Package) ReadPart(ctx context.Context, name string) (*Node, error) {
	return p.readPart(ctx, name, p.budget)
}

// uncounted 为之后还会再次计数的读取返回一个等于整个展开上限的预算；包不限制时返回 nil。
func (p *Package) uncounted() *budget {
	if p.budget == nil {
		return nil
	}
	return &budget{max: p.max, left: p.max}
}

// readPart 解析名为 name 的 XML 部件，把它的字节和元素计入 b。元素嵌套超过 MaxDepth 的部件返回
// convert.ErrCorrupt。
func (p *Package) readPart(ctx context.Context, name string, b *budget) (*Node, error) {
	file, err := p.archive.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", convert.ErrCorrupt, err)
	}
	defer func() { _ = file.Close() }()
	reader := &meteredReader{r: file, budget: b}
	decoder := xml.NewDecoder(reader)
	root := &Node{}
	// texts[i] 收集 stack[i] 的字符数据，直到这个元素结束。
	stack, texts := []*Node{root}, [][]byte{nil}
	for count := 0; ; count++ {
		if count%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		token, err := decoder.Token()
		if reader.err != nil {
			return nil, reader.err
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", convert.ErrCorrupt, name, err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			if len(stack) > MaxDepth {
				return nil, fmt.Errorf("%w: %s nests elements deeper than %d", convert.ErrCorrupt, name, MaxDepth)
			}
			if err := b.spend(ElementCost); err != nil {
				return nil, err
			}
			node := &Node{Name: token.Name.Local}
			if len(token.Attr) > 0 {
				node.attrs = make([]attr, len(token.Attr))
			}
			for i, a := range token.Attr {
				key := a.Name.Local
				if relationshipNamespaces[a.Name.Space] {
					key = "r:" + key
				}
				node.attrs[i] = attr{key: key, value: a.Value}
			}
			parent := stack[len(stack)-1]
			parent.Children = append(parent.Children, node)
			stack = append(stack, node)
			if len(texts) < len(stack) {
				texts = append(texts, nil)
			}
			texts[len(stack)-1] = texts[len(stack)-1][:0]
		case xml.EndElement:
			if len(stack) > 1 {
				top := len(stack) - 1
				stack[top].Text = string(texts[top])
				stack = stack[:top]
			}
		case xml.CharData:
			top := len(stack) - 1
			texts[top] = append(texts[top], token...)
		}
	}
	for i, node := range stack {
		node.Text = string(texts[i])
	}
	return root, nil
}

// CheckXML 把名为 name 的 XML 部件读到末尾而不保留，格式不正确时返回 convert.ErrCorrupt：
// 元素不匹配或未关闭、根元素不止一个、根元素之外有文字，或者元素嵌套超过 MaxDepth。
// 读取计入展开上限；每读一批记号检查一次取消。
func (p *Package) CheckXML(ctx context.Context, name string) error {
	file, err := p.archive.Open(name)
	if err != nil {
		return fmt.Errorf("%w: %w", convert.ErrCorrupt, err)
	}
	defer func() { _ = file.Close() }()
	reader := &meteredReader{r: file, budget: p.budget}
	decoder := xml.NewDecoder(reader)
	depth, roots := 0, 0
	for count := 0; ; count++ {
		if count%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		token, err := decoder.Token()
		if reader.err != nil {
			return reader.err
		}
		if errors.Is(err, io.EOF) {
			if roots != 1 {
				return fmt.Errorf("%w: %s has %d root elements", convert.ErrCorrupt, name, roots)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %s: %w", convert.ErrCorrupt, name, err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
			}
			if depth >= MaxDepth {
				return fmt.Errorf("%w: %s nests elements deeper than %d", convert.ErrCorrupt, name, MaxDepth)
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && roots == 0 {
				token = bytes.TrimPrefix(token, utf8BOM)
			}
			if depth == 0 && len(bytes.TrimSpace(token)) > 0 {
				return fmt.Errorf("%w: %s has text outside its root element", convert.ErrCorrupt, name)
			}
		}
	}
}

// xmlRelationships 是读取器会把目标当作 XML 内容解析的关系类型，它们的目标总是要检查。
var xmlRelationships = map[string]bool{
	"officeDocument": true, "worksheet": true, "sharedStrings": true, "styles": true, "theme": true,
}

// caseInsensitiveParts 是读取器不论名字大小写都能找到的部件。
var caseInsensitiveParts = map[string]bool{"xl/sharedstrings.xml": true}

// fixedXMLParts 是读取器从固定路径加载的部件，比较时不区分大小写；存在时总是要检查。
var fixedXMLParts = map[string]bool{
	"xl/workbook.xml": true, "xl/sharedstrings.xml": true, "xl/styles.xml": true, "xl/theme/theme1.xml": true,
	"word/document.xml": true, "word/styles.xml": true, "word/numbering.xml": true, "ppt/presentation.xml": true,
}

// XMLParts 返回需要按 XML 检查的部件：[Content_Types].xml 按部件名或扩展名声明为 XML 的部件、
// 读取器从固定路径加载的部件、读取器会当作 XML 解析其内容的关系目标，以及内容看起来像 XML 的
// 其他关系目标。这项检查防范的是损坏的文件；特意构造、让读取器以不同方式解析某个部件的包，
// 仍然可能转换出更少的文字，就像一个本来就只有这么多文字的文件。目标按读取器的方式解析：
// 反斜杠当作斜杠，去掉点号路径段。[Content_Types].xml 在这里严格检查并只计数一次；
// 无法打开的部件留给需要它的读取器处理。
func (p *Package) XMLParts(ctx context.Context) ([]string, error) {
	const contentTypes = "[Content_Types].xml"
	if file, err := p.archive.Open(contentTypes); err == nil {
		_ = file.Close()
		if err := p.CheckXML(ctx, contentTypes); err != nil {
			return nil, err
		}
	}
	types, err := p.readPart(ctx, contentTypes, p.uncounted())
	if err != nil {
		return nil, err
	}
	isXML := func(contentType string) bool {
		contentType = strings.ToLower(strings.TrimSpace(contentType))
		return strings.HasSuffix(contentType, "+xml") || strings.HasSuffix(contentType, "/xml")
	}
	defaults, overrides := map[string]bool{}, map[string]bool{}
	for _, item := range types.Child("Types").Elements() {
		switch item.Name {
		case "Default":
			defaults[strings.ToLower(item.Attr("Extension"))] = isXML(item.Attr("ContentType"))
		case "Override":
			overrides[strings.ToLower(strings.TrimPrefix(item.Attr("PartName"), "/"))] = isXML(item.Attr("ContentType"))
		}
	}
	declared := func(name string) bool {
		lower := strings.ToLower(name)
		if xmlPart, ok := overrides[lower]; ok {
			return xmlPart
		}
		return defaults[strings.TrimPrefix(path.Ext(lower), ".")]
	}
	// 关系目标来自用单独预算读取的关系部件；这些部件也是 XML，和其他部件一样检查和计数。
	targets := map[string]bool{} // 目标 -> 是否总是检查
	var required [][2]string     // 每个核心关系目标的两种解析结果
	for _, entry := range p.archive.File {
		name := strings.ReplaceAll(entry.Name, `\`, "/")
		if !strings.HasSuffix(name, ".rels") {
			continue
		}
		root, err := p.readPart(ctx, name, p.uncounted())
		if err != nil {
			return nil, err
		}
		base := path.Dir(path.Dir(name))
		for _, item := range root.Child("Relationships").Elements() {
			if item.Attr("TargetMode") == "External" {
				continue
			}
			// 不同的读取器在去掉点号路径段之前还是之后把反斜杠转为斜杠并不一致，所以两种解析结果都记录。
			raw := item.Attr("Target")
			always := xmlRelationships[path.Base(item.Attr("Type"))]
			cleaned := strings.ReplaceAll(raw, `\`, "/")
			if strings.HasPrefix(cleaned, "/") {
				cleaned = strings.TrimPrefix(path.Clean(cleaned), "/")
			} else {
				cleaned = path.Join(base, cleaned)
			}
			literal := strings.ReplaceAll(path.Clean(raw), `\`, "/")
			if strings.HasPrefix(literal, "/") {
				literal = strings.TrimPrefix(literal, "/")
			} else if base != "." {
				literal = base + "/" + literal
			}
			for _, target := range []string{cleaned, literal} {
				targets[target] = targets[target] || always
			}
			if always {
				required = append(required, [2]string{cleaned, literal})
			}
		}
	}
	// 关系指向的核心部件必须在某一种解析结果下作为文件存在；与读取器一样，共享字符串不区分大小写匹配。
	present, folded := map[string]bool{}, map[string]bool{}
	for _, entry := range p.archive.File {
		normalized := strings.ReplaceAll(entry.Name, `\`, "/")
		if strings.HasSuffix(normalized, "/") {
			continue
		}
		present[normalized], folded[strings.ToLower(normalized)] = true, true
	}
	exists := func(name string) bool {
		return present[name] || caseInsensitiveParts[strings.ToLower(name)] && folded[strings.ToLower(name)]
	}
	for _, target := range required {
		if !exists(target[0]) && !exists(target[1]) {
			return nil, fmt.Errorf("%w: missing part %s", convert.ErrCorrupt, target[0])
		}
	}
	var names []string
	for _, entry := range p.archive.File {
		normalized := strings.ReplaceAll(entry.Name, `\`, "/")
		if normalized == contentTypes || strings.HasSuffix(normalized, "/") {
			continue
		}
		always, targeted := targets[normalized]
		if !targeted {
			always, targeted = targets[path.Clean(normalized)]
		}
		always = always || fixedXMLParts[strings.ToLower(normalized)]
		if declared(entry.Name) || strings.HasSuffix(entry.Name, ".rels") || always || targeted && looksLikeXML(entry) {
			names = append(names, normalized)
		}
	}
	return names, nil
}

// looksLikeXML 报告条目的内容在可选的 BOM 和任意空白之后是否以 "<" 开头。
func looksLikeXML(entry *zip.File) bool {
	file, err := entry.Open()
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	reader := bufio.NewReader(file)
	if head, err := reader.Peek(len(utf8BOM)); err == nil && bytes.Equal(head, utf8BOM) {
		_, _ = reader.Discard(len(utf8BOM))
	}
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return false
		}
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		}
		return b == '<'
	}
}

// RequirePart 是用于文档不可缺少的部件的 ReadPart；部件不存在时返回 convert.ErrCorrupt。
func (p *Package) RequirePart(ctx context.Context, name string) (*Node, error) {
	if name == "" {
		return nil, fmt.Errorf("%w: missing relationship target", convert.ErrCorrupt)
	}
	root, err := p.ReadPart(ctx, name)
	if err == nil && root == nil {
		return nil, fmt.Errorf("%w: missing part %s", convert.ErrCorrupt, name)
	}
	return root, err
}

// Relationships 读取 part 的关系。内部目标相对于部件所在目录解析，以斜杠开头时相对于包的根。
func (p *Package) Relationships(ctx context.Context, part string) (map[string]Relationship, error) {
	directory, name := path.Split(part)
	root, err := p.ReadPart(ctx, directory+"_rels/"+name+".rels")
	if err != nil {
		return nil, err
	}
	relationships := map[string]Relationship{}
	for _, item := range root.Child("Relationships").Elements() {
		target, external := item.Attr("Target"), item.Attr("TargetMode") == "External"
		if !external {
			if strings.HasPrefix(target, "/") {
				target = strings.TrimPrefix(target, "/")
			} else {
				target = path.Join(directory, target)
			}
		}
		relationships[item.Attr("Id")] = Relationship{Type: path.Base(item.Attr("Type")), Target: target, External: external}
	}
	return relationships, nil
}
