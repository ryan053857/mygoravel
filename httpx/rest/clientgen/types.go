package clientgen

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/cockroachdb/errors"
)

// errorxDefine 是错误码的定义函数。
var errorxDefine = frameworkModule + "/errorx"

// mirror 是服务端一个包在客户端的镜像：接口用到的类型和这个包的错误码。
type mirror struct {
	src   string // 服务端包路径
	name  string
	path  string // 镜像包的导入路径
	pkg   *types.Package
	decls map[string]string // 类型名 → 声明
	// overrides 是输入、输出类型去掉服务端字段后的副本，以及匿名请求体、响应体的命名类型，
	// 优先于复制的原类型。
	overrides map[string]string
	bodies    map[string]bool // overrides 中为匿名请求体、响应体生成的类型
	codes     []string
	file      *file
}

// file 记录一个生成文件的包和导入。
type file struct {
	path    string            // 文件所在包的导入路径
	imports map[string]string // 导入路径 → 名称
	used    map[string]string // 名称 → 导入路径
}

func newFile(path string) *file {
	return &file{path: path, imports: map[string]string{}, used: map[string]string{}}
}

// use 导入 p 并返回在文件中引用它的名称，名称冲突时加数字后缀。
func (f *file) use(p, name string) string {
	if n, ok := f.imports[p]; ok {
		return n
	}
	n := name
	for i := 2; f.used[n] != ""; i++ {
		n = name + strconv.Itoa(i)
	}
	f.imports[p], f.used[n] = n, p
	return n
}

func (f *file) importBlock() string {
	if len(f.imports) == 0 {
		return ""
	}
	var std, other []string
	for p := range f.imports {
		if isStd(p) {
			std = append(std, p)
		} else {
			other = append(other, p)
		}
	}
	sort.Strings(std)
	sort.Strings(other)
	var b strings.Builder
	b.WriteString("import (\n")
	for i, group := range [][]string{std, other} {
		if i > 0 && len(std) > 0 && len(other) > 0 {
			b.WriteString("\n")
		}
		for _, p := range group {
			if n := f.imports[p]; n != lastElem(p) {
				b.WriteString(n + " ")
			}
			b.WriteString(strconv.Quote(p) + "\n")
		}
	}
	b.WriteString(")\n\n")
	return b.String()
}

// lastElem 返回导入路径的最后一段，用于判断是否需要别名。
func lastElem(p string) string {
	return p[strings.LastIndex(p, "/")+1:]
}

// mirrorOf 返回服务端包的镜像，第一次用到时创建；镜像包名冲突时返回错误。
func (g *generator) mirrorOf(pkg *types.Package) (*mirror, error) {
	if m, ok := g.mirrors[pkg.Path()]; ok {
		return m, nil
	}
	name := pkg.Name()
	if n, ok := g.opts.Rename[pkg.Path()]; ok {
		name = n
	}
	if other, ok := g.names[name]; ok {
		return nil, errors.Newf("clientgen: packages %s and %s would both be mirrored as %q; rename one with --rename %s=<name>",
			other, pkg.Path(), name, pkg.Path())
	}
	if !token.IsIdentifier(name) {
		return nil, errors.Newf("clientgen: %q is not a valid package name", name)
	}
	m := &mirror{src: pkg.Path(), name: name, path: g.outPath + "/" + name, pkg: pkg,
		decls: map[string]string{}, overrides: map[string]string{}, bodies: map[string]bool{}}
	m.file = newFile(m.path)
	g.mirrors[pkg.Path()], g.names[name] = m, pkg.Path()
	return m, nil
}

// expr 返回类型 t 在文件 f 中的写法；用到的应用和框架类型加入复制队列。
func (g *generator) expr(t types.Type, f *file) (string, error) {
	switch t := t.(type) {
	case *types.Alias:
		if t.Obj().Pkg() == nil {
			return t.Obj().Name(), nil // any
		}
		return g.expr(types.Unalias(t), f)
	case *types.Basic:
		return t.Name(), nil
	case *types.Pointer:
		e, err := g.expr(t.Elem(), f)
		return "*" + e, err
	case *types.Slice:
		e, err := g.expr(t.Elem(), f)
		return "[]" + e, err
	case *types.Array:
		e, err := g.expr(t.Elem(), f)
		return "[" + strconv.FormatInt(t.Len(), 10) + "]" + e, err
	case *types.Map:
		k, err := g.expr(t.Key(), f)
		if err != nil {
			return "", err
		}
		v, err := g.expr(t.Elem(), f)
		return "map[" + k + "]" + v, err
	case *types.Struct:
		return g.structExpr(t, f, nil)
	case *types.Interface:
		if t.Empty() {
			return "any", nil
		}
		return "", errors.Newf("clientgen: interface type %s cannot be decoded from JSON", t)
	case *types.TypeParam:
		return t.Obj().Name(), nil
	case *types.Named:
		return g.named(t, f)
	}
	return "", errors.Newf("clientgen: type %s cannot be sent as JSON", t)
}

func (g *generator) named(t *types.Named, f *file) (string, error) {
	obj := t.Obj()
	if obj.Pkg() == nil {
		return obj.Name(), nil // error、comparable
	}
	p := obj.Pkg().Path()
	if p == humaPath && obj.Name() == "FormFile" {
		return f.use(g.outPath+"/wire", "wire") + ".File", nil
	}
	var name string
	switch {
	case isStd(p) || g.importable(p):
		name = f.use(p, obj.Pkg().Name()) + "." + obj.Name()
	case g.copyable(p):
		m, err := g.mirrorOf(obj.Pkg())
		if err != nil {
			return "", err
		}
		g.enqueue(t.Origin().Obj())
		name = obj.Name()
		if m.path != f.path {
			name = f.use(m.path, m.name) + "." + name
		}
	default:
		return "", errors.Newf("clientgen: %s.%s comes from %s, which the client cannot copy; "+
			"if that package is safe for clients, allow it with --import %s", obj.Pkg().Name(), obj.Name(), p, p)
	}
	if args := t.TypeArgs(); args.Len() > 0 {
		list := make([]string, args.Len())
		for i := range list {
			e, err := g.expr(args.At(i), f)
			if err != nil {
				return "", err
			}
			list[i] = e
		}
		name += "[" + strings.Join(list, ", ") + "]"
	}
	return name, nil
}

// importable 报告客户端能否直接导入包 p：它或它的上级路径在允许导入的列表中。
func (g *generator) importable(p string) bool {
	for allowed := range g.imports {
		if p == allowed || strings.HasPrefix(p, allowed+"/") {
			return true
		}
	}
	return false
}

// copyable 报告包是否属于应用模块或 mygoravel，它的类型复制到客户端。
func (g *generator) copyable(pkgPath string) bool {
	p := g.pkgs[pkgPath]
	return p != nil && p.Module != nil && g.modules[p.Module.Path]
}

func (g *generator) enqueue(obj *types.TypeName) {
	if !g.seen[obj] {
		g.seen[obj] = true
		g.queue = append(g.queue, obj)
	}
}

// fieldFilter 决定结构体的字段是否复制；nil 表示复制全部导出字段。
type fieldFilter func(i int, v *types.Var, tag reflect.StructTag) bool

// structExpr 返回结构体的写法：导出字段和嵌入字段连同 tag 与注释复制，json:"-" 的字段去掉。
// replace 中的字段改用给定的类型写法。
func (g *generator) structExpr(s *types.Struct, f *file, keep fieldFilter, replace ...map[int]string) (string, error) {
	var b strings.Builder
	b.WriteString("struct {\n")
	for i := range s.NumFields() {
		v := s.Field(i)
		tag := reflect.StructTag(s.Tag(i))
		if (!v.Exported() && !v.Embedded()) || tag.Get("json") == "-" {
			continue
		}
		if keep != nil && !keep(i, v, tag) {
			continue
		}
		b.WriteString(comment(g.doc(v.Pkg(), v.Pos())))
		e, ok := "", false
		if len(replace) > 0 {
			e, ok = replace[0][i]
		}
		if !ok {
			var err error
			if e, err = g.expr(v.Type(), f); err != nil {
				return "", errors.Wrapf(err, "field %s", v.Name())
			}
		}
		if !v.Embedded() {
			b.WriteString(v.Name() + " ")
		}
		b.WriteString(e)
		if s.Tag(i) != "" {
			b.WriteString(" " + quoteTag(s.Tag(i)))
		}
		b.WriteString("\n")
	}
	b.WriteString("}")
	return b.String(), nil
}

func quoteTag(tag string) string {
	if strings.Contains(tag, "`") {
		return strconv.Quote(tag)
	}
	return "`" + tag + "`"
}

// marshalers 是让 JSON 编码不再由字段决定的方法，带这些方法的类型不能复制。
var marshalers = []string{"MarshalJSON", "UnmarshalJSON", "MarshalText", "UnmarshalText", "Schema", "TransformSchema"}

// drain 复制队列中的全部类型。
func (g *generator) drain() error {
	for len(g.queue) > 0 {
		obj := g.queue[0]
		g.queue = g.queue[1:]
		if err := g.copyType(obj); err != nil {
			return err
		}
	}
	return nil
}

// copyType 把类型声明和同一个包中这个类型的常量写入镜像包。
func (g *generator) copyType(obj *types.TypeName) error {
	m, err := g.mirrorOf(obj.Pkg())
	if err != nil {
		return err
	}
	named, ok := obj.Type().(*types.Named)
	if !ok {
		return errors.Newf("clientgen: %s.%s is not a named type", obj.Pkg().Name(), obj.Name())
	}
	for i := range named.NumMethods() {
		if name := named.Method(i).Name(); slices.Contains(marshalers, name) {
			return errors.Newf("clientgen: %s.%s has method %s, so copying its fields would not produce the same JSON; "+
				"move it to a package the client can import (--import) or use plain fields", obj.Pkg().Name(), obj.Name(), name)
		}
	}
	if _, ok := m.overrides[obj.Name()]; ok {
		return nil
	}
	var b strings.Builder
	b.WriteString(comment(g.doc(obj.Pkg(), obj.Pos())))
	b.WriteString("type " + obj.Name())
	if tps := named.TypeParams(); tps.Len() > 0 {
		list := make([]string, tps.Len())
		for i := range list {
			tp := tps.At(i)
			c, err := g.expr(tp.Constraint(), m.file)
			if err != nil {
				return errors.Wrapf(err, "%s.%s", obj.Pkg().Name(), obj.Name())
			}
			list[i] = tp.Obj().Name() + " " + c
		}
		b.WriteString("[" + strings.Join(list, ", ") + "]")
	}
	e, err := g.expr(named.Underlying(), m.file)
	if err != nil {
		return errors.Wrapf(err, "%s.%s", obj.Pkg().Name(), obj.Name())
	}
	b.WriteString(" " + e + "\n")
	b.WriteString(g.consts(obj, named))
	m.decls[obj.Name()] = b.String()
	return nil
}

// consts 返回同一个包中类型为 named 的导出常量，按源码顺序。
func (g *generator) consts(obj *types.TypeName, named *types.Named) string {
	if _, ok := named.Underlying().(*types.Basic); !ok {
		return ""
	}
	var list []*types.Const
	scope := obj.Pkg().Scope()
	for _, name := range scope.Names() {
		if c, ok := scope.Lookup(name).(*types.Const); ok && c.Exported() && types.Identical(c.Type(), named) {
			list = append(list, c)
		}
	}
	if len(list) == 0 {
		return ""
	}
	slices.SortFunc(list, func(a, b *types.Const) int { return int(a.Pos() - b.Pos()) })
	var b strings.Builder
	b.WriteString("\nconst (\n")
	for _, c := range list {
		b.WriteString(comment(g.doc(c.Pkg(), c.Pos())))
		v := c.Val()
		lit := v.String()
		if v.Kind() == constant.String || v.Kind() == constant.Int {
			lit = v.ExactString()
		}
		b.WriteString(c.Name() + " " + obj.Name() + " = " + lit + "\n")
	}
	b.WriteString(")\n")
	return b.String()
}

// scanCodes 在主包依赖的应用和框架包中查找 var ErrX = errorx.Define(kind, "code")，写入镜像包。
func (g *generator) scanCodes() error {
	paths := make([]string, 0, len(g.pkgs))
	for p := range g.pkgs {
		if g.copyable(p) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		pkg := g.pkgs[p]
		var codes []string
		for _, f := range pkg.Syntax {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue
				}
				for _, spec := range gd.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if i >= len(vs.Values) || !name.IsExported() {
							continue
						}
						code, ok := g.defined(pkg.TypesInfo, vs.Values[i])
						if !ok {
							continue
						}
						codes = append(codes, comment(g.doc(pkg.Types, name.Pos()))+
							name.Name+" wire.Code = "+strconv.Quote(code)+"\n")
					}
				}
			}
		}
		if len(codes) == 0 {
			continue
		}
		m, err := g.mirrorOf(pkg.Types)
		if err != nil {
			return err
		}
		m.codes = codes
	}
	return nil
}

// defined 报告表达式是否是以字符串常量为错误码的 errorx.Define 调用。
func (g *generator) defined(info *types.Info, e ast.Expr) (string, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 2 || info == nil {
		return "", false
	}
	var id *ast.Ident
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		id = fn.Sel
	case *ast.Ident:
		id = fn
	default:
		return "", false
	}
	obj, ok := info.Uses[id].(*types.Func)
	if !ok || obj.Pkg() == nil || obj.Pkg().Path() != errorxDefine || obj.Name() != "Define" {
		return "", false
	}
	tv, ok := info.Types[call.Args[1]]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(tv.Value), true
}

// doc 返回声明在 pos 处的注释。
func (g *generator) doc(pkg *types.Package, pos token.Pos) string {
	if pkg == nil {
		return ""
	}
	index, ok := g.docs[pkg.Path()]
	if !ok {
		index = map[token.Pos]string{}
		if p := g.pkgs[pkg.Path()]; p != nil {
			for _, f := range p.Syntax {
				indexDocs(f, index)
			}
		}
		g.docs[pkg.Path()] = index
	}
	return index[pos]
}

func indexDocs(f *ast.File, index map[token.Pos]string) {
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		single := len(gd.Specs) == 1
		for _, spec := range gd.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				index[s.Name.Pos()] = text(s.Doc, s.Comment, gd.Doc, single)
				ast.Inspect(s.Type, func(n ast.Node) bool {
					st, ok := n.(*ast.StructType)
					if !ok {
						return true
					}
					for _, field := range st.Fields.List {
						t := text(field.Doc, field.Comment, nil, false)
						for _, name := range field.Names {
							index[name.Pos()] = t
						}
						if len(field.Names) == 0 {
							index[embedded(field.Type)] = t
						}
					}
					return true
				})
			case *ast.ValueSpec:
				t := text(s.Doc, s.Comment, gd.Doc, single)
				for _, name := range s.Names {
					index[name.Pos()] = t
				}
			}
		}
	}
}

func text(doc, trailing, outer *ast.CommentGroup, single bool) string {
	switch {
	case doc != nil:
		return doc.Text()
	case single && outer != nil:
		return outer.Text()
	case trailing != nil:
		return trailing.Text()
	}
	return ""
}

// embedded 返回嵌入字段的类型名的位置，与 types.Var.Pos() 一致。
func embedded(e ast.Expr) token.Pos {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.SelectorExpr:
			return x.Sel.Pos()
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		default:
			return e.Pos()
		}
	}
}

// comment 把注释文本转换为 // 注释行。
func comment(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	var b strings.Builder
	for line := range strings.SplitSeq(s, "\n") {
		if line == "" {
			b.WriteString("//\n")
		} else {
			b.WriteString("// " + line + "\n")
		}
	}
	return b.String()
}
