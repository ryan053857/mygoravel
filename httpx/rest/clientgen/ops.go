package clientgen

import (
	"go/format"
	"go/token"
	"go/types"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cockroachdb/errors"
	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/httpx/rest"
)

// skip 表示端点的形式第一版不生成，原因写入 Result.Skipped。
type skip string

func (s skip) Error() string { return string(s) }

// param 是一个路径、查询或请求头参数。
type param struct {
	name    string     // 参数名，例如 id、per_page
	sel     string     // 从输入类型开始的字段选择，例如 OffsetParams.Page
	field   *types.Var // 字段
	explode bool
}

// endpoint 是分析后的端点。
type endpoint struct {
	ep            rest.Endpoint
	group, method string

	path          []param // 按路径中出现的顺序
	query, header []param
	body          types.Type // 请求体类型，没有为 nil
	inObj         *types.TypeName
	inBody        int             // Body 字段的序号
	input         *types.TypeName // 需要输入类型的副本作为参数时不为 nil
	inputKeep     fieldFilter     // input 不为 nil 时，副本保留的字段

	outBody    types.Type // 响应体类型，没有为 nil
	outObj     *types.TypeName
	outBodyAt  int             // Body 字段的序号
	output     *types.TypeName // 需要返回输出类型的副本时不为 nil
	outputKeep fieldFilter
	outStatus  string  // 输出副本中状态码字段名
	outHeaders []param // 输出副本中的响应头
}

func (g *generator) run() (*Result, error) {
	res := &Result{Files: map[string][]byte{}}
	if err := g.scanCodes(); err != nil {
		return nil, err
	}
	eps := g.api.Endpoints()
	slices.SortFunc(eps, func(a, b rest.Endpoint) int { return strings.Compare(a.OperationID, b.OperationID) })
	groups := map[string][]*endpoint{}
	for _, ep := range eps {
		e, err := g.analyze(ep)
		var s skip
		if errors.As(err, &s) {
			res.Skipped = append(res.Skipped, ep.OperationID+": "+string(s))
			continue
		}
		if err != nil {
			return nil, errors.Wrapf(err, "clientgen: %s", ep.OperationID)
		}
		groups[e.group] = append(groups[e.group], e)
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		if name != "" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, e := range groups[""] {
		if slices.Contains(names, e.method) {
			return nil, errors.Newf("clientgen: operation %s and group %s would both be Client.%s", e.ep.OperationID, e.method, e.method)
		}
		if e.method == "New" || e.method == "Options" || e.method == "Client" {
			return nil, errors.Newf("clientgen: operation %s would generate the reserved name %s", e.ep.OperationID, e.method)
		}
	}

	root := newFile(g.outPath)
	var methods strings.Builder
	for _, e := range groups[""] {
		if err := g.method(&methods, root, "c *Client", "c.c", e); err != nil {
			return nil, err
		}
	}
	for _, name := range names {
		f := newFile(g.outPath)
		var b strings.Builder
		b.WriteString("// " + name + "Group 包含 operationId 以 " + strings.ToLower(groupSegment(groups[name][0].ep.OperationID)) + "- 开头的接口。\n")
		b.WriteString("type " + name + "Group struct{ c *" + f.use(g.outPath+"/wire", "wire") + ".Client }\n\n")
		for _, e := range groups[name] {
			if err := g.method(&b, f, "g "+name+"Group", "g.c", e); err != nil {
				return nil, err
			}
		}
		file := strings.ToLower(groupSegment(groups[name][0].ep.OperationID))
		if file == "client" {
			file = "client_group"
		}
		if err := g.emit(res, file+".go", g.rootName, "", f, b.String()); err != nil {
			return nil, err
		}
	}
	if err := g.drain(); err != nil {
		return nil, err
	}
	if err := g.emit(res, "client.go", g.rootName, g.clientDoc(), root, g.client(root, names)+methods.String()); err != nil {
		return nil, err
	}
	for _, m := range g.mirrors {
		var b strings.Builder
		decls := make([]string, 0, len(m.decls)+len(m.overrides))
		for name := range m.decls {
			decls = append(decls, name)
		}
		for name := range m.overrides {
			if _, ok := m.decls[name]; !ok {
				decls = append(decls, name)
			}
		}
		slices.Sort(decls)
		for _, name := range decls {
			if d, ok := m.overrides[name]; ok {
				b.WriteString(d + "\n")
			} else {
				b.WriteString(m.decls[name] + "\n")
			}
		}
		if len(m.codes) > 0 {
			m.file.use(g.outPath+"/wire", "wire")
			b.WriteString("// 错误码，用 errors.Is(err, ErrXxx) 判断接口返回的错误。\nconst (\n")
			b.WriteString(strings.Join(m.codes, ""))
			b.WriteString(")\n")
		}
		doc := "// Package " + m.name + " 是服务端包 " + m.src + " 中接口用到的类型和错误码。\n"
		if err := g.emit(res, m.name+"/"+m.name+".go", m.name, doc, m.file, b.String()); err != nil {
			return nil, err
		}
	}
	res.Files["wire/wire.go"] = []byte(Header + "\n\n" + wireSource)
	return res, nil
}

func (g *generator) clientDoc() string {
	title := "API"
	if info := g.api.OpenAPI().Info; info != nil && info.Title != "" {
		title = info.Title
	}
	return "// Package " + g.rootName + " 是 " + title + " 的 Go 客户端，由 mygoravel 的 client 命令生成。\n"
}

func (g *generator) client(f *file, groups []string) string {
	wire := f.use(g.outPath+"/wire", "wire")
	base := ""
	if servers := g.api.OpenAPI().Servers; len(servers) > 0 {
		base = servers[0].URL
	}
	var b strings.Builder
	b.WriteString("// Options 配置客户端，见 wire.Options。\ntype Options = " + wire + ".Options\n\n")
	b.WriteString("// Client 调用接口，按 operationId 的第一段分组，例如 article-publish 是 Article.Publish。\n")
	b.WriteString("type Client struct {\n\tc *" + wire + ".Client\n\n")
	for _, name := range groups {
		b.WriteString(name + " " + name + "Group\n")
	}
	b.WriteString("}\n\n// New 创建客户端。\nfunc New(opts Options) *Client {\n")
	b.WriteString("\tc := " + wire + ".New(opts, " + strconv.Quote(base) + ")\n\treturn &Client{c: c")
	for _, name := range groups {
		b.WriteString(", " + name + ": " + name + "Group{c: c}")
	}
	b.WriteString("}\n}\n\n")
	return b.String()
}

// emit 格式化文件并加入结果。
func (g *generator) emit(res *Result, name, pkg, doc string, f *file, body string) error {
	src := Header + "\n\n" + doc + "package " + pkg + "\n\n" + f.importBlock() + body
	out, err := format.Source([]byte(src))
	if err != nil {
		return errors.Wrapf(err, "clientgen: format %s:\n%s", name, src)
	}
	res.Files[name] = out
	return nil
}

// analyze 把端点的输入输出类型分解为方法的参数和返回值。
func (g *generator) analyze(ep rest.Endpoint) (*endpoint, error) {
	if ep.Output == reflect.TypeFor[huma.StreamResponse]() {
		if ep.Events != nil {
			return nil, skip("SSE endpoints are not generated yet")
		}
		return nil, skip("streaming responses are not generated")
	}
	e := &endpoint{ep: ep}
	parts := strings.Split(ep.OperationID, "-")
	if len(parts) > 1 {
		e.group = pascal(parts[:1])
		parts = parts[1:]
	}
	e.method = pascal(parts)
	if err := g.analyzeInput(e); err != nil {
		return nil, err
	}
	if err := g.analyzeOutput(e); err != nil {
		return nil, err
	}
	return e, nil
}

func groupSegment(opID string) string {
	first, _, _ := strings.Cut(opID, "-")
	return first
}

func (g *generator) analyzeInput(e *endpoint) error {
	t := e.ep.Input
	if t.Kind() == reflect.Struct && t.NumField() == 0 {
		return nil
	}
	obj, err := g.lookup(t)
	if err != nil {
		return err
	}
	st, ok := obj.Type().Underlying().(*types.Struct)
	if !ok {
		return errors.Newf("input %s is not a struct", obj.Name())
	}
	e.inObj = obj
	keep := map[int]bool{}
	for i := range st.NumFields() {
		v := st.Field(i)
		tag := reflect.StructTag(st.Tag(i))
		switch {
		case v.Name() == "Body" && !v.Embedded():
			e.body, e.inBody, keep[i] = v.Type(), i, true
		case v.Name() == "RawBody" && !v.Embedded():
			return skip("raw and multipart request bodies are not generated yet")
		case v.Embedded() && !hasParamTag(tag):
			inner, ok := v.Type().Underlying().(*types.Struct)
			if !ok {
				continue
			}
			var found []param
			var locs []string
			if err := collectParams(inner, v.Name()+".", &found, &locs); err != nil {
				return err
			}
			hasPath := slices.Contains(locs, "path")
			other := slices.ContainsFunc(locs, func(l string) bool { return l != "path" })
			if hasPath && other {
				return skip("an embedded struct mixing path and other parameters is not supported")
			}
			for i, p := range found {
				e.add(locs[i], p)
			}
			keep[i] = other
		default:
			loc, p, err := paramOf(v, tag, "")
			if err != nil {
				return err
			}
			if loc != "" {
				e.add(loc, p)
				keep[i] = loc != "path"
			}
		}
	}
	if err := e.orderPath(); err != nil {
		return err
	}
	if len(e.query)+len(e.header) > 0 {
		e.input = obj
		e.inputKeep = func(i int, _ *types.Var, _ reflect.StructTag) bool { return keep[i] }
	}
	return nil
}

func (e *endpoint) add(loc string, p param) {
	switch loc {
	case "path":
		e.path = append(e.path, p)
	case "query":
		e.query = append(e.query, p)
	case "header":
		e.header = append(e.header, p)
	}
}

func hasParamTag(tag reflect.StructTag) bool {
	for _, k := range []string{"path", "query", "header", "cookie", "form"} {
		if tag.Get(k) != "" {
			return true
		}
	}
	return false
}

// collectParams 收集嵌入结构体中的参数，与 Huma 一样递归进入嵌入字段。
func collectParams(st *types.Struct, prefix string, out *[]param, locs *[]string) error {
	for i := range st.NumFields() {
		v := st.Field(i)
		tag := reflect.StructTag(st.Tag(i))
		if v.Embedded() && !hasParamTag(tag) {
			if inner, ok := v.Type().Underlying().(*types.Struct); ok {
				if err := collectParams(inner, prefix+v.Name()+".", out, locs); err != nil {
					return err
				}
			}
			continue
		}
		loc, p, err := paramOf(v, tag, prefix)
		if err != nil {
			return err
		}
		if loc != "" {
			*out, *locs = append(*out, p), append(*locs, loc)
		}
	}
	return nil
}

// paramOf 返回字段的参数位置；不是参数时 loc 为空。
func paramOf(v *types.Var, tag reflect.StructTag, prefix string) (string, param, error) {
	p := param{sel: prefix + v.Name(), field: v}
	switch {
	case tag.Get("path") != "":
		p.name = tag.Get("path")
		return "path", p, nil
	case tag.Get("query") != "":
		opts := strings.Split(tag.Get("query"), ",")
		p.name, p.explode = opts[0], slices.Contains(opts[1:], "explode")
		if slices.Contains(opts[1:], "deepObject") {
			return "", p, skip("deepObject query parameters are not supported")
		}
		return "query", p, nil
	case tag.Get("header") != "":
		p.name = tag.Get("header")
		return "header", p, nil
	case tag.Get("cookie") != "":
		return "", p, skip("cookie parameters are not supported")
	case tag.Get("form") != "":
		return "", p, skip("form parameters are not generated yet")
	}
	return "", p, nil
}

// orderPath 按路径模板中出现的顺序排列路径参数。
func (e *endpoint) orderPath() error {
	names := placeholders(e.ep.Path)
	if len(names) != len(e.path) {
		return errors.Newf("path %s has %d parameters but the input declares %d", e.ep.Path, len(names), len(e.path))
	}
	ordered := make([]param, len(names))
	for i, name := range names {
		j := slices.IndexFunc(e.path, func(p param) bool { return p.name == name })
		if j < 0 {
			return errors.Newf("path parameter %s is not declared in the input", name)
		}
		ordered[i] = e.path[j]
	}
	e.path = ordered
	return nil
}

func placeholders(p string) []string {
	var names []string
	for {
		i := strings.IndexByte(p, '{')
		if i < 0 {
			return names
		}
		j := strings.IndexByte(p[i:], '}')
		if j < 0 {
			return names
		}
		names = append(names, p[i+1:i+j])
		p = p[i+j+1:]
	}
}

func (g *generator) analyzeOutput(e *endpoint) error {
	t := e.ep.Output
	if t.Kind() == reflect.Struct && t.NumField() == 0 {
		return nil
	}
	obj, err := g.lookup(t)
	if err != nil {
		return err
	}
	st, ok := obj.Type().Underlying().(*types.Struct)
	if !ok {
		return errors.Newf("output %s is not a struct", obj.Name())
	}
	e.outObj = obj
	keep := map[int]bool{}
	for i := range st.NumFields() {
		v := st.Field(i)
		tag := reflect.StructTag(st.Tag(i))
		switch {
		case v.Name() == "Body" && !v.Embedded():
			if s, ok := v.Type().Underlying().(*types.Slice); ok && types.Identical(s.Elem(), types.Typ[types.Byte]) {
				return skip("raw response bodies are not generated")
			}
			e.outBody, e.outBodyAt, keep[i] = v.Type(), i, true
		case v.Name() == "Status" && !v.Embedded() && isInt(v.Type()):
			e.outStatus, keep[i] = v.Name(), true
		case tag.Get("header") != "" && !v.Embedded():
			e.outHeaders = append(e.outHeaders, param{name: tag.Get("header"), sel: v.Name(), field: v})
			keep[i] = true
		}
	}
	if e.outStatus != "" || len(e.outHeaders) > 0 {
		e.output = obj
		e.outputKeep = func(i int, _ *types.Var, _ reflect.StructTag) bool { return keep[i] }
	}
	return nil
}

func isInt(t types.Type) bool {
	b, ok := t.Underlying().(*types.Basic)
	return ok && b.Info()&types.IsInteger != 0
}

// bodyRef 返回请求体或响应体类型在文件 f 中的写法。匿名结构体在 owner 的镜像包中生成为
// <owner>Body，调用方不必重复写出带 tag 的结构体。
func (g *generator) bodyRef(owner *types.TypeName, body types.Type, kind string, f *file) (string, error) {
	st, ok := body.(*types.Struct)
	if !ok {
		return g.expr(body, f)
	}
	m, err := g.mirrorOf(owner.Pkg())
	if err != nil {
		return "", err
	}
	name := owner.Name() + "Body"
	if !m.bodies[name] {
		if owner.Pkg().Scope().Lookup(name) != nil {
			return "", errors.Newf("clientgen: the anonymous %s body of %s.%s would be named %s, which the package already declares",
				kind, owner.Pkg().Name(), owner.Name(), name)
		}
		decl, err := g.structExpr(st, m.file, nil)
		if err != nil {
			return "", errors.Wrapf(err, "%s.%s", owner.Pkg().Name(), owner.Name())
		}
		m.overrides[name] = "// " + name + " 是 " + owner.Name() + " 的" + kind + "体。\ntype " + name + " " + decl + "\n"
		m.bodies[name] = true
	}
	if m.path == f.path {
		return name, nil
	}
	return f.use(m.path, m.name) + "." + name, nil
}

// override 把输入或输出类型去掉服务端字段后的副本写入它的镜像包，返回在文件 f 中的写法。
// body 是 Body 字段的序号和类型，没有 Body 时 body 为 nil。
func (g *generator) override(obj *types.TypeName, keep fieldFilter, at int, body types.Type, kind string, f *file) (string, error) {
	m, err := g.mirrorOf(obj.Pkg())
	if err != nil {
		return "", err
	}
	if _, ok := m.overrides[obj.Name()]; !ok {
		replace := map[int]string{}
		if body != nil {
			ref, err := g.bodyRef(obj, body, kind, m.file)
			if err != nil {
				return "", err
			}
			replace[at] = ref
		}
		st := obj.Type().Underlying().(*types.Struct)
		decl, err := g.structExpr(st, m.file, keep, replace)
		if err != nil {
			return "", errors.Wrapf(err, "%s.%s", obj.Pkg().Name(), obj.Name())
		}
		m.overrides[obj.Name()] = comment(g.doc(obj.Pkg(), obj.Pos())) + "type " + obj.Name() + " " + decl + "\n"
		delete(m.decls, obj.Name())
		g.seen[obj] = true
	}
	return f.use(m.path, m.name) + "." + obj.Name(), nil
}

// locals 是生成的方法中使用的局部名称，参数名不能与它们相同。
var locals = []string{"ctx", "out", "err", "q", "h", "in", "body", "res", "g", "c"}

// method 把端点渲染为方法。
func (g *generator) method(b *strings.Builder, f *file, recv, client string, e *endpoint) error {
	ctxName := f.use("context", "context")
	wire := f.use(g.outPath+"/wire", "wire")
	// 先渲染所有类型，记下方法中用到的导入名，再给路径参数取名。
	pathTypes := make([]string, len(e.path))
	for i, p := range e.path {
		t, err := g.expr(p.field.Type(), f)
		if err != nil {
			return errors.Wrapf(err, "clientgen: %s: path parameter %s", e.ep.OperationID, p.name)
		}
		pathTypes[i] = t
	}
	var paramType, bodyExpr string
	switch {
	case e.input != nil:
		t, err := g.override(e.input, e.inputKeep, e.inBody, e.body, "请求", f)
		if err != nil {
			return errors.Wrapf(err, "clientgen: %s", e.ep.OperationID)
		}
		paramType = "in " + t
		if e.body != nil {
			bodyExpr = "in.Body"
		}
	case e.body != nil:
		t, err := g.bodyRef(e.inObj, e.body, "请求", f)
		if err != nil {
			return errors.Wrapf(err, "clientgen: %s: request body", e.ep.OperationID)
		}
		paramType, bodyExpr = "body "+t, "body"
	}
	var result string
	switch {
	case e.output != nil:
		t, err := g.override(e.output, e.outputKeep, e.outBodyAt, e.outBody, "响应", f)
		if err != nil {
			return errors.Wrapf(err, "clientgen: %s", e.ep.OperationID)
		}
		result = t
	case e.outBody != nil:
		t, err := g.bodyRef(e.outObj, e.outBody, "响应", f)
		if err != nil {
			return errors.Wrapf(err, "clientgen: %s: response body", e.ep.OperationID)
		}
		result = t
	}
	var urlName, httpName string
	if len(e.query) > 0 {
		urlName = f.use("net/url", "url")
	}
	if len(e.header) > 0 || e.output != nil {
		httpName = f.use("net/http", "http")
	}

	taken := map[string]bool{}
	for _, n := range locals {
		taken[n] = true
	}
	for n := range f.used {
		taken[n] = true
	}
	args := make([]string, len(e.path))
	for i, p := range e.path {
		args[i] = argName(p.field.Name(), taken)
		taken[args[i]] = true
	}

	b.WriteString(g.methodDoc(e))
	b.WriteString("func (" + recv + ") " + e.method + "(ctx " + ctxName + ".Context")
	for i := range e.path {
		b.WriteString(", " + args[i] + " " + pathTypes[i])
	}
	if paramType != "" {
		b.WriteString(", " + paramType)
	}
	b.WriteString(") ")
	if result != "" {
		b.WriteString("(" + result + ", error) {\n\tvar out " + result + "\n")
	} else {
		b.WriteString("error {\n")
	}
	if len(e.query) > 0 {
		b.WriteString("\tq := " + urlName + ".Values{}\n")
		for _, p := range e.query {
			b.WriteString("\t" + wire + ".SetQuery(q, " + strconv.Quote(p.name) + ", in." + p.sel + ", " + strconv.FormatBool(p.explode) + ")\n")
		}
	}
	if len(e.header) > 0 {
		b.WriteString("\th := " + httpName + ".Header{}\n")
		for _, p := range e.header {
			b.WriteString("\t" + wire + ".SetHeader(h, " + strconv.Quote(p.name) + ", in." + p.sel + ")\n")
		}
	}
	call := "\t" + "err := "
	if result == "" {
		call = "\treturn "
	}
	b.WriteString(call + client + ".Do(ctx, &" + wire + ".Request{\n")
	b.WriteString("\t\tMethod: " + strconv.Quote(e.ep.Method) + ",\n")
	b.WriteString("\t\tPath: " + pathExpr(e.ep.Path, e.path, args, wire) + ",\n")
	if len(e.query) > 0 {
		b.WriteString("\t\tQuery: q,\n")
	}
	if len(e.header) > 0 {
		b.WriteString("\t\tHeader: h,\n")
	}
	if bodyExpr != "" {
		b.WriteString("\t\tBody: " + bodyExpr + ",\n")
	}
	if !e.ep.Public {
		b.WriteString("\t\tAuth: true,\n")
	}
	switch {
	case e.output != nil:
		if e.outBody != nil {
			b.WriteString("\t\tOut: &out.Body,\n")
		}
		b.WriteString("\t\tResponse: func(res *" + httpName + ".Response) error {\n")
		if e.outStatus != "" {
			b.WriteString("\t\t\tout." + e.outStatus + " = res.StatusCode\n")
		}
		for _, h := range e.outHeaders {
			b.WriteString("\t\t\tif err := " + wire + ".ParseHeader(res, " + strconv.Quote(h.name) + ", &out." + h.sel + "); err != nil {\n\t\t\t\treturn err\n\t\t\t}\n")
		}
		b.WriteString("\t\t\treturn nil\n\t\t},\n")
	case e.outBody != nil:
		b.WriteString("\t\tOut: &out,\n")
	}
	b.WriteString("\t})\n")
	if result != "" {
		b.WriteString("\treturn out, err\n")
	}
	b.WriteString("}\n\n")
	return nil
}

func (g *generator) methodDoc(e *endpoint) string {
	var b strings.Builder
	if e.ep.Summary != "" {
		b.WriteString(comment(e.method + " " + e.ep.Summary))
	} else {
		b.WriteString(comment(e.method + " 调用 " + e.ep.OperationID + "。"))
	}
	if e.ep.Description != "" {
		b.WriteString("//\n" + comment(e.ep.Description))
	}
	b.WriteString("//\n// " + e.ep.Method + " " + e.ep.Path + "\n")
	return b.String()
}

// pathExpr 把路径模板转换为拼接参数的表达式。
func pathExpr(tmpl string, params []param, args []string, wire string) string {
	var parts []string
	rest := tmpl
	for i := range params {
		j := strings.IndexByte(rest, '{')
		k := strings.IndexByte(rest, '}')
		if lit := rest[:j]; lit != "" {
			parts = append(parts, strconv.Quote(lit))
		}
		parts = append(parts, wire+".PathValue("+args[i]+")")
		rest = rest[k+1:]
	}
	if rest != "" || len(parts) == 0 {
		parts = append(parts, strconv.Quote(rest))
	}
	return strings.Join(parts, " + ")
}

// initialisms 是生成名称时整体大写的词。
var initialisms = map[string]bool{
	"api": true, "id": true, "ids": true, "url": true, "uri": true, "http": true, "https": true, "json": true,
	"xml": true, "html": true, "uuid": true, "ip": true, "sql": true, "sse": true, "ui": true, "sms": true,
	"otp": true, "totp": true, "pdf": true, "csv": true, "mcp": true, "ai": true, "oidc": true, "jwt": true,
}

// pascal 把 kebab-case 的各段转换为 PascalCase，例如 [export progress] 为 ExportProgress。
func pascal(parts []string) string {
	var b strings.Builder
	for _, p := range parts {
		switch {
		case p == "ids":
			b.WriteString("IDs")
		case initialisms[p]:
			b.WriteString(strings.ToUpper(p))
		default:
			r, size := utf8.DecodeRuneInString(p)
			b.WriteRune(unicode.ToUpper(r))
			b.WriteString(p[size:])
		}
	}
	return b.String()
}

// argName 把字段名转换为参数名，例如 ID 为 id、ArticleID 为 articleID；与 taken 或关键字冲突时加 Param 后缀。
func argName(field string, taken map[string]bool) string {
	runes := []rune(field)
	n := 0
	for n < len(runes) && unicode.IsUpper(runes[n]) {
		n++
	}
	switch {
	case n == len(runes):
		n = len(runes)
	case n > 1:
		n--
	}
	for i := range n {
		runes[i] = unicode.ToLower(runes[i])
	}
	name := string(runes)
	if token.IsKeyword(name) || taken[name] {
		name += "Param"
	}
	return name
}
