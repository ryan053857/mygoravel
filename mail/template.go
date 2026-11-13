package mail

import (
	"bytes"
	"context"
	htmltemplate "html/template"
	"io/fs"
	"path"
	"sort"
	"strings"
	texttemplate "text/template"

	"github.com/cockroachdb/errors"

	"github.com/go-tenon/tenon/i18n"
)

// 模板文件后缀。
const (
	subjectSuffix = ".subject.txt"
	htmlSuffix    = ".html"
	textSuffix    = ".txt"
)

// Templates 是启动时一次性解析好的邮件模板，可并发使用。
//
// 一封名为 welcome 的邮件由 welcome.subject.txt（必需）、welcome.html 和 welcome.txt
// （至少一个）组成，可以放在子目录中，名称为相对路径，如 auth/welcome。
// 以 _ 开头的 .html 或 .txt 文件是公共片段，会解析进每封邮件的同类模板，
// 典型用法是布局：_layout.html 中写 {{template "content" .}}，
// 邮件中写 {{define "content"}}...{{end}}{{template "_layout.html" .}}。
//
// 所有模板都可以调用 t 翻译消息：{{t "mail.welcome.greeting" .}}，语言取自 Render 的 ctx。
type Templates struct {
	mails map[string]*mailTemplate
}

type mailTemplate struct {
	subject *texttemplate.Template
	html    *htmltemplate.Template
	text    *texttemplate.Template
}

// 解析时的占位函数，渲染时替换为绑定 ctx 的版本。
var placeholderFuncs = map[string]any{"t": func(string, ...any) string { return "" }}

func translate(ctx context.Context) map[string]any {
	return map[string]any{"t": func(id string, data ...any) string {
		var d any
		if len(data) > 0 {
			d = data[0]
		}
		return i18n.T(ctx, id, d)
	}}
}

// NewTemplates 从 fsys 加载全部邮件模板。
func NewTemplates(fsys fs.FS) (*Templates, error) {
	files := map[string]string{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if ext := path.Ext(p); ext != htmlSuffix && ext != textSuffix {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		files[p] = string(data)
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "mail: load templates")
	}

	names := make([]string, 0, len(files))
	for p := range files {
		names = append(names, p)
	}
	sort.Strings(names)

	htmlBase := htmltemplate.New("").Funcs(placeholderFuncs)
	textBase := texttemplate.New("").Funcs(placeholderFuncs)
	mails := map[string]*mailTemplate{}
	for _, p := range names {
		if strings.HasPrefix(path.Base(p), "_") {
			var err error
			if path.Ext(p) == htmlSuffix {
				_, err = htmlBase.New(p).Parse(files[p])
			} else {
				_, err = textBase.New(p).Parse(files[p])
			}
			if err != nil {
				return nil, errors.Wrapf(err, "mail: parse %s", p)
			}
			continue
		}
		name, _ := strings.CutSuffix(p, subjectSuffix)
		if name == p {
			name = strings.TrimSuffix(p, path.Ext(p))
		}
		if mails[name] == nil {
			mails[name] = &mailTemplate{}
		}
	}

	for name, mt := range mails {
		var err error
		if src, ok := files[name+subjectSuffix]; ok {
			mt.subject, err = texttemplate.New(name + subjectSuffix).Funcs(placeholderFuncs).Parse(src)
		} else {
			err = errors.New("missing " + name + subjectSuffix)
		}
		if err == nil {
			if src, ok := files[name+htmlSuffix]; ok {
				mt.html, err = parseWith(htmlBase, name+htmlSuffix, src)
			}
		}
		if err == nil {
			if src, ok := files[name+textSuffix]; ok {
				mt.text, err = parseWithText(textBase, name+textSuffix, src)
			}
		}
		if err == nil && mt.html == nil && mt.text == nil {
			err = errors.New("missing " + name + htmlSuffix + " or " + name + textSuffix)
		}
		if err != nil {
			return nil, errors.Wrapf(err, "mail: template %q", name)
		}
	}
	return &Templates{mails: mails}, nil
}

func parseWith(base *htmltemplate.Template, name, src string) (*htmltemplate.Template, error) {
	t, err := base.Clone()
	if err != nil {
		return nil, err
	}
	return t.New(name).Parse(src)
}

func parseWithText(base *texttemplate.Template, name, src string) (*texttemplate.Template, error) {
	t, err := base.Clone()
	if err != nil {
		return nil, err
	}
	return t.New(name).Parse(src)
}

// Render 渲染名为 name 的邮件，返回只填好 Subject、HTML 和 Text 的 Message。
// 主题去掉首尾空白后必须是单行。
func (t *Templates) Render(ctx context.Context, name string, data any) (Message, error) {
	mt := t.mails[name]
	if mt == nil {
		return Message{}, errors.Newf("mail: template %q not found", name)
	}
	funcs := translate(ctx)
	var msg Message
	var buf bytes.Buffer

	subject, err := mt.subject.Clone()
	if err == nil {
		err = subject.Funcs(funcs).Execute(&buf, data)
	}
	if err != nil {
		return Message{}, errors.Wrapf(err, "mail: render %q subject", name)
	}
	msg.Subject = strings.TrimSpace(buf.String())
	if msg.Subject == "" || strings.ContainsAny(msg.Subject, "\r\n") {
		return Message{}, errors.Wrapf(ErrInvalid, "template %q: subject must be a non-empty single line", name)
	}

	if mt.html != nil {
		buf.Reset()
		html, err := mt.html.Clone()
		if err == nil {
			err = html.Funcs(funcs).Execute(&buf, data)
		}
		if err != nil {
			return Message{}, errors.Wrapf(err, "mail: render %q html", name)
		}
		msg.HTML = buf.String()
	}
	if mt.text != nil {
		buf.Reset()
		text, err := mt.text.Clone()
		if err == nil {
			err = text.Funcs(funcs).Execute(&buf, data)
		}
		if err != nil {
			return Message{}, errors.Wrapf(err, "mail: render %q text", name)
		}
		msg.Text = buf.String()
	}
	return msg, nil
}
