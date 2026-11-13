package clientgen

import (
	_ "embed"
	"encoding/json"
	"go/token"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/cockroachdb/errors"
	"github.com/danielgtaylor/huma/v2"
	"golang.org/x/tools/go/packages"

	"github.com/go-tenon/tenon/httpx/rest"
)

//go:embed ts/runtime.ts
var tsRuntime string

// TSOptions 配置 TypeScript 客户端的生成。
type TSOptions struct {
	// Main 与 Options.Main 相同：在主包依赖的、属于应用模块和 Tenon 的包中查找 errorx.Define 定义的错误码。
	Main string
	// Dir 是加载源码的工作目录，空表示当前目录。
	Dir string
	// Rename 把服务端包的导入路径映射为错误码分组的名字，用于两个包同名的情况。
	Rename map[string]string
}

// GenerateTS 从 api 登记的端点生成 TypeScript 客户端，不依赖任何 npm 包：
//
//   - api.ts：OpenAPI 中的类型，以及 createClient。接口按 operationId 分组，article-publish 生成
//     client.article.publish；路径参数按顺序作为参数，查询参数、请求头和请求体合成最后一个参数；
//   - errors.ts：errorx.Define 定义的错误码，按包分组，例如 ArticleErrors.SlugTaken；
//   - runtime.ts：发送请求、刷新 access token、ApiError、事件流和下载。
//
// 类型来自 Huma 生成的 OpenAPI 文档，与 openapi.json 一致。
func GenerateTS(api *rest.API, opts TSOptions) (*Result, error) {
	g := &generator{
		api: api, opts: Options{Main: opts.Main, Dir: opts.Dir, Rename: opts.Rename},
		fset: token.NewFileSet(), pkgs: map[string]*packages.Package{}, modules: map[string]bool{frameworkModule: true},
		docs: map[string]map[token.Pos]string{},
	}
	if err := g.load(); err != nil {
		return nil, err
	}
	codes, err := g.collectCodes()
	if err != nil {
		return nil, err
	}
	t := &tsGen{api: api, registry: api.OpenAPI().Components.Schemas}
	res := &Result{Files: map[string][]byte{}}
	errorsTS, err := t.errors(codes, opts.Rename)
	if err != nil {
		return nil, err
	}
	apiTS, err := t.client(res)
	if err != nil {
		return nil, err
	}
	res.Files["api.ts"] = []byte(Header + "\n\n" + apiTS)
	res.Files["errors.ts"] = []byte(Header + "\n\n" + errorsTS)
	res.Files["runtime.ts"] = []byte(Header + "\n\n" + tsRuntime)
	return res, nil
}

type tsGen struct {
	api      *rest.API
	registry huma.Registry
}

// errors 生成 errors.ts：每个包一个常量对象，键是去掉 Err 前缀的变量名。
func (t *tsGen) errors(groups []codeGroup, rename map[string]string) (string, error) {
	var b strings.Builder
	b.WriteString("// 业务错误码：与 ApiError.code 比较，或交给 isApiError(err, code)。\n")
	seen := map[string]string{}
	for _, grp := range groups {
		name := grp.pkg.Name()
		if n, ok := rename[grp.pkg.Path()]; ok {
			name = n
		}
		obj := pascal(strings.Split(name, "_")) + "Errors"
		if other, ok := seen[obj]; ok {
			return "", errors.Newf("clientgen: packages %s and %s would both generate %s; rename one with --rename %s=<name>",
				other, grp.pkg.Path(), obj, grp.pkg.Path())
		}
		seen[obj] = grp.pkg.Path()
		b.WriteString("\n/** " + grp.pkg.Path() + " 的错误码。 */\nexport const " + obj + " = {\n")
		for _, c := range grp.codes {
			b.WriteString(tsDoc(c.doc, "  "))
			b.WriteString("  " + strings.TrimPrefix(c.name, "Err") + ": " + strconv.Quote(c.value) + ",\n")
		}
		b.WriteString("} as const\n")
	}
	return b.String(), nil
}

// tsOp 是一个端点在客户端中的样子。
type tsOp struct {
	ep        rest.Endpoint
	op        *huma.Operation
	group     string // 为空时是客户端的直接方法
	method    string
	pathArgs  []tsParam
	params    []tsParam // 查询参数和请求头
	body      string    // JSON 请求体的类型
	bodyReq   bool
	form      string // multipart 表单的类型
	formName  string // 表单的具名类型
	inputName string // 查询参数、请求头和请求体的具名类型
	out       string // 响应体的类型，为空时没有响应体
	headers   []string
	events    string // 事件流的事件类型
	download  bool
}

type tsParam struct {
	name, key, in, typ, doc string
	required                bool
}

func (t *tsGen) client(res *Result) (string, error) {
	eps := t.api.Endpoints()
	slices.SortFunc(eps, func(a, b rest.Endpoint) int { return strings.Compare(a.OperationID, b.OperationID) })
	groups := map[string][]*tsOp{}
	var events strings.Builder
	for _, ep := range eps {
		o, err := t.analyze(ep, &events)
		if err == nil {
			t.inputs(o, &events)
		}
		var s skip
		if errors.As(err, &s) {
			res.Skipped = append(res.Skipped, ep.OperationID+": "+string(s))
			continue
		}
		if err != nil {
			return "", errors.Wrapf(err, "clientgen: %s", ep.OperationID)
		}
		groups[o.group] = append(groups[o.group], o)
	}
	reserved := []string{"setAccessToken", "getAccessToken", "refresh"}
	for _, o := range groups[""] {
		if _, ok := groups[o.method]; ok || slices.Contains(reserved, o.method) {
			return "", errors.Newf("clientgen: operation %s would collide with client.%s", o.ep.OperationID, o.method)
		}
	}
	for name := range groups {
		if slices.Contains(reserved, name) {
			return "", errors.Newf("clientgen: group %s collides with client.%s", name, name)
		}
	}

	var b strings.Builder
	b.WriteString("import { Runtime, type ClientOptions, type Download, type StreamOptions } from \"./runtime\"\n\n")
	b.WriteString("export * from \"./errors\"\nexport { ApiError, isApiError, saveBlob, type ClientOptions, type AuthOptions, type Download, type FieldError, type StreamOptions } from \"./runtime\"\n")
	b.WriteString(t.schemas())
	b.WriteString(events.String())
	b.WriteString("\n/** 创建客户端。 */\nexport function createClient(options: ClientOptions) {\n  const rt = new Runtime(options)\n  return {\n")
	b.WriteString("    /** 设置 access token，例如登录成功后；空字符串表示退出登录。 */\n    setAccessToken: (token: string) => rt.setAccessToken(token),\n")
	b.WriteString("    /** 当前的 access token，例如交给实时频道的 SDK。 */\n    getAccessToken: () => rt.getAccessToken(),\n")
	b.WriteString("    /** 用 auth.refresh 换新的 access token，打开页面时先调用一次，返回是否已经登录。 */\n    refresh: () => rt.refresh(),\n")
	for _, o := range groups[""] {
		b.WriteString(t.method(o, "    "))
	}
	for _, name := range slices.Sorted(maps.Keys(groups)) {
		if name == "" {
			continue
		}
		b.WriteString("    /** operationId 以 " + groupSegment(groups[name][0].ep.OperationID) + "- 开头的接口。 */\n    " + name + ": {\n")
		for _, o := range groups[name] {
			b.WriteString(t.method(o, "      "))
		}
		b.WriteString("    },\n")
	}
	b.WriteString("  }\n}\n\n/** createClient 返回的客户端。 */\nexport type Client = ReturnType<typeof createClient>\n")
	return b.String(), nil
}

func (t *tsGen) analyze(ep rest.Endpoint, events *strings.Builder) (*tsOp, error) {
	item := t.api.OpenAPI().Paths[ep.Path]
	var op *huma.Operation
	if item != nil {
		op = map[string]*huma.Operation{http.MethodGet: item.Get, http.MethodPost: item.Post, http.MethodPut: item.Put,
			http.MethodPatch: item.Patch, http.MethodDelete: item.Delete, http.MethodHead: item.Head}[ep.Method]
	}
	if op == nil {
		return nil, errors.Newf("operation %s %s not in the OpenAPI document", ep.Method, ep.Path)
	}
	parts := strings.Split(ep.OperationID, "-")
	o := &tsOp{ep: ep, op: op}
	if len(parts) == 1 {
		o.method = camel(parts)
	} else {
		o.group, o.method = camel(parts[:1]), camel(parts[1:])
	}
	taken := map[string]bool{}
	for _, name := range placeholders(ep.Path) {
		for _, p := range op.Parameters {
			if p.In == "path" && p.Name == name {
				arg := tsIdent(camel(strings.Split(strings.ReplaceAll(name, "_", "-"), "-")), taken)
				o.pathArgs = append(o.pathArgs, tsParam{name: arg, key: name, in: "path", typ: t.typ(p.Schema, ""), required: true})
			}
		}
	}
	for _, p := range op.Parameters {
		if p.In == "query" || p.In == "header" {
			o.params = append(o.params, tsParam{key: p.Name, in: p.In, typ: t.typ(p.Schema, ""), doc: p.Description, required: p.Required})
		}
	}
	if rb := op.RequestBody; rb != nil {
		switch {
		case rb.Content["application/json"] != nil:
			o.body, o.bodyReq = t.typ(rb.Content["application/json"].Schema, ""), rb.Required
		case rb.Content["multipart/form-data"] != nil:
			o.form = t.typ(rb.Content["multipart/form-data"].Schema, "")
		default:
			return nil, skip("request body " + strings.Join(slices.Sorted(maps.Keys(rb.Content)), ", ") + " is not supported")
		}
	}
	switch {
	case ep.Events != nil:
		name := pascal(strings.Split(ep.OperationID, "-")) + "Event"
		var variants []string
		for _, ev := range slices.Sorted(maps.Keys(ep.Events)) {
			variants = append(variants, "{ event: "+strconv.Quote(ev)+"; data: "+t.typ(t.registry.Schema(ep.Events[ev], true, ""), "")+"; id?: string }")
		}
		events.WriteString("\n/** " + ep.OperationID + " 推送的事件，按 event 区分。 */\nexport type " + name + " =\n  | " + strings.Join(variants, "\n  | ") + "\n")
		o.events = name
	case ep.Output == reflect.TypeFor[huma.StreamResponse]():
		o.download = true
	default:
		resp := success(op)
		if resp == nil {
			break
		}
		if mt := resp.Content["application/json"]; mt != nil {
			o.out = t.typ(mt.Schema, "")
		} else if len(resp.Content) > 0 {
			return nil, skip("response " + strings.Join(slices.Sorted(maps.Keys(resp.Content)), ", ") + " is not supported")
		}
		o.headers = slices.Sorted(maps.Keys(resp.Headers))
	}
	return o, nil
}

// success 返回第一个 2xx 响应。
func success(op *huma.Operation) *huma.Response {
	for _, code := range slices.Sorted(maps.Keys(op.Responses)) {
		if len(code) == 3 && code[0] == '2' {
			return op.Responses[code]
		}
	}
	return nil
}

// inputs 为有查询参数、请求头或 multipart 表单的接口生成具名的输入类型，例如 ArticleListInput、ArticleCoverForm。
func (t *tsGen) inputs(o *tsOp, b *strings.Builder) {
	base := pascal(strings.Split(o.ep.OperationID, "-"))
	if o.form != "" {
		o.formName = base + "Form"
		b.WriteString("\n/** " + o.ep.OperationID + " 的表单。 */\nexport type " + o.formName + " = " + o.form + "\n")
	}
	if len(o.params) == 0 {
		return
	}
	o.inputName = base + "Input"
	b.WriteString("\n/** " + o.ep.OperationID + " 的参数。 */\nexport interface " + o.inputName + " {\n")
	for _, p := range o.params {
		opt := "?"
		if p.required {
			opt = ""
		}
		b.WriteString(tsDoc(p.doc, "  ") + "  " + tsKey(p.field()) + opt + ": " + p.typ + "\n")
	}
	if o.body != "" {
		opt := "?"
		if o.bodyReq {
			opt = ""
		}
		b.WriteString("  body" + opt + ": " + o.body + "\n")
	}
	if o.formName != "" {
		b.WriteString("  form: " + o.formName + "\n")
	}
	b.WriteString("}\n")
}

// field 返回参数在输入类型中的字段名：查询参数用原名，请求头转为 camelCase，例如 Idempotency-Key 得到 idempotencyKey。
func (p tsParam) field() string {
	if p.in == "header" {
		return camel(strings.Split(strings.ToLower(p.key), "-"))
	}
	return p.key
}

// method 生成一个接口方法。
func (t *tsGen) method(o *tsOp, indent string) string {
	var args, req []string
	for _, p := range o.pathArgs {
		args = append(args, p.name+": "+p.typ)
	}
	path := o.ep.Path
	for _, p := range o.pathArgs {
		path = strings.ReplaceAll(path, "{"+p.key+"}", "${encodeURIComponent("+p.name+")}")
	}
	req = append(req, "method: "+strconv.Quote(o.ep.Method), "path: `"+path+"`")
	if o.ep.Public {
		req = append(req, "public: true")
	}
	required := o.bodyReq || o.formName != ""
	query, headers := map[string]string{}, map[string]string{}
	for _, p := range o.params {
		required = required || p.required
		if p.in == "header" {
			headers[p.key] = "input." + p.field()
		} else {
			query[p.key] = "input[" + strconv.Quote(p.key) + "]"
		}
	}
	switch {
	case o.inputName != "":
		arg := "input: " + o.inputName
		if !required {
			arg += " = {}"
		}
		args = append(args, arg)
		if o.body != "" {
			req = append(req, "body: input.body")
		}
		if o.formName != "" {
			req = append(req, "form: input.form")
		}
	case o.body != "":
		opt := ""
		if !o.bodyReq {
			opt = "?"
		}
		args = append(args, "body"+opt+": "+o.body)
		req = append(req, "body")
	case o.formName != "":
		args = append(args, "form: "+o.formName)
		req = append(req, "form")
	}
	if len(query) > 0 {
		req = append(req, "query: { "+kv(query)+" }")
	}
	if len(headers) > 0 {
		req = append(req, "headers: { "+kv(headers)+" }")
	}
	r := "{ " + strings.Join(req, ", ") + " }"
	var ret, call string
	switch {
	case o.events != "":
		args = append(args, "options?: StreamOptions")
		names := make([]string, 0, len(o.ep.Events))
		for _, ev := range slices.Sorted(maps.Keys(o.ep.Events)) {
			names = append(names, strconv.Quote(ev))
		}
		ret, call = "AsyncGenerator<"+o.events+">", "rt.stream<"+o.events+">("+r+", options, ["+strings.Join(names, ", ")+"])"
	case o.download:
		ret, call = "Promise<Download>", "rt.download("+r+")"
	case len(o.headers) > 0:
		names := make([]string, len(o.headers))
		fields := make([]string, len(o.headers))
		for i, h := range o.headers {
			key := tsKey(camel(strings.Split(strings.ToLower(h), "-")))
			names[i] = key + ": " + strconv.Quote(h)
			fields[i] = key + "?: string"
		}
		body := o.out
		if body == "" {
			body = "void"
		}
		ret = "Promise<{ body: " + body + "; headers: { " + strings.Join(fields, "; ") + " } }>"
		call = "rt.withHeaders<" + body + ">(" + r + ", { " + strings.Join(names, ", ") + " })"
	default:
		out := o.out
		if out == "" {
			out = "void"
		}
		ret, call = "Promise<"+out+">", "rt.request<"+out+">("+r+")"
	}
	doc := strings.TrimSpace(o.ep.Summary + "\n\n" + o.ep.Description)
	return tsDoc(doc+"\n\n"+o.ep.Method+" "+o.ep.Path, indent) + indent + o.method + ": (" + strings.Join(args, ", ") + "): " + ret + " =>\n" +
		indent + "  " + call + ",\n"
}

func kv(m map[string]string) string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(m)) {
		parts = append(parts, strconv.Quote(k)+": "+m[k])
	}
	return strings.Join(parts, ", ")
}

// schemas 按名称顺序生成 OpenAPI 中的全部命名类型。
func (t *tsGen) schemas() string {
	var b strings.Builder
	all := t.registry.Map()
	for _, name := range slices.Sorted(maps.Keys(all)) {
		s := all[name]
		b.WriteString("\n" + tsDoc(s.Description, ""))
		if s.Type == huma.TypeObject && len(s.OneOf)+len(s.AnyOf)+len(s.AllOf) == 0 && s.AdditionalProperties != true && !isSchema(s.AdditionalProperties) {
			b.WriteString("export interface " + tsName(name) + " " + t.object(s, "") + "\n")
		} else {
			b.WriteString("export type " + tsName(name) + " = " + t.typ(s, "") + "\n")
		}
	}
	return b.String()
}

func isSchema(v any) bool {
	_, ok := v.(*huma.Schema)
	return ok
}

// typ 返回 schema 的 TypeScript 类型；indent 是对象字段的缩进。
func (t *tsGen) typ(s *huma.Schema, indent string) string {
	if s == nil {
		return "unknown"
	}
	var ts string
	switch {
	case s.Ref != "":
		ts = tsName(strings.TrimPrefix(s.Ref, "#/components/schemas/"))
	case len(s.OneOf) > 0 || len(s.AnyOf) > 0:
		var parts []string
		for _, v := range append(slices.Clone(s.OneOf), s.AnyOf...) {
			parts = append(parts, t.typ(v, indent))
		}
		ts = strings.Join(parts, " | ")
	case len(s.AllOf) > 0:
		var parts []string
		for _, v := range s.AllOf {
			parts = append(parts, t.typ(v, indent))
		}
		ts = strings.Join(parts, " & ")
	case len(s.Enum) > 0:
		var parts []string
		for _, v := range s.Enum {
			b, _ := json.Marshal(v)
			parts = append(parts, string(b))
		}
		ts = strings.Join(parts, " | ")
	default:
		switch s.Type {
		case huma.TypeString:
			ts = "string"
			if s.Format == "binary" || s.ContentEncoding == "binary" {
				ts = "Blob"
			}
		case huma.TypeInteger, huma.TypeNumber:
			ts = "number"
		case huma.TypeBoolean:
			ts = "boolean"
		case huma.TypeArray:
			item := t.typ(s.Items, indent)
			if strings.ContainsAny(item, " |&") {
				item = "(" + item + ")"
			}
			ts = item + "[]"
		case huma.TypeObject:
			switch ap := s.AdditionalProperties.(type) {
			case *huma.Schema:
				ts = "Record<string, " + t.typ(ap, indent) + ">"
			case bool:
				if ap && len(s.Properties) == 0 {
					ts = "Record<string, unknown>"
				}
			}
			if ts == "" {
				ts = t.object(s, indent)
			}
		default:
			ts = "unknown"
		}
	}
	if s.Nullable && ts != "unknown" {
		ts += " | null"
	}
	return ts
}

// object 生成对象类型的字面量，字段按 OpenAPI 中的名称排序。
func (t *tsGen) object(s *huma.Schema, indent string) string {
	if len(s.Properties) == 0 {
		return "Record<string, never>"
	}
	var b strings.Builder
	b.WriteString("{\n")
	for _, name := range slices.Sorted(maps.Keys(s.Properties)) {
		p := s.Properties[name]
		opt := "?"
		if slices.Contains(s.Required, name) {
			opt = ""
		}
		b.WriteString(tsDoc(p.Description, indent+"  "))
		b.WriteString(indent + "  " + tsKey(name) + opt + ": " + t.typ(p, indent+"  ") + "\n")
	}
	b.WriteString(indent + "}")
	return b.String()
}

// camel 把 operationId 的各段连成 camelCase，例如 get-by-slug 得到 getBySlug。
func camel(parts []string) string {
	p := pascal(parts)
	if p == "" {
		return p
	}
	// 开头的缩写整体小写，例如 IDs 得到 ids，URL 得到 url。
	r := []rune(p)
	i := 0
	for i < len(r) && unicode.IsUpper(r[i]) && (i+1 == len(r) || unicode.IsUpper(r[i+1]) || i == 0) {
		r[i] = unicode.ToLower(r[i])
		i++
	}
	return string(r)
}

// tsName 把 schema 名转换为合法的 TypeScript 类型名。
func tsName(name string) string {
	var b strings.Builder
	for i, r := range name {
		if r == '_' || r == '$' || unicode.IsLetter(r) || i > 0 && unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// tsKey 在 name 不是合法标识符时加上引号。
func tsKey(name string) string {
	for i, r := range name {
		if !(r == '_' || r == '$' || unicode.IsLetter(r) || i > 0 && unicode.IsDigit(r)) {
			return strconv.Quote(name)
		}
	}
	return name
}

// tsIdent 返回不与 taken 和保留字冲突的参数名。
func tsIdent(name string, taken map[string]bool) string {
	for name == "" || taken[name] || tsReserved[name] {
		name += "_"
	}
	taken[name] = true
	return name
}

var tsReserved = map[string]bool{"body": true, "form": true, "input": true, "options": true, "default": true,
	"delete": true, "new": true, "class": true, "function": true, "var": true, "in": true, "for": true}

// tsDoc 把 doc 写成 JSDoc 注释。
func tsDoc(doc, indent string) string {
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return ""
	}
	doc = strings.ReplaceAll(doc, "*/", "*\\/")
	lines := strings.Split(doc, "\n")
	if len(lines) == 1 {
		return indent + "/** " + lines[0] + " */\n"
	}
	var b strings.Builder
	b.WriteString(indent + "/**\n")
	for _, l := range lines {
		b.WriteString(strings.TrimRight(indent+" * "+l, " ") + "\n")
	}
	b.WriteString(indent + " */\n")
	return b.String()
}
