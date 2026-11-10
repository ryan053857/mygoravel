package mail_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ryan053857/mygoravel/i18n"
	"github.com/ryan053857/mygoravel/mail"
)

func templatesFS() fstest.MapFS {
	return fstest.MapFS{
		"_layout.html":           {Data: []byte(`<html><body>{{template "content" .}}</body></html>`)},
		"welcome.subject.txt":    {Data: []byte("  {{t \"mail.welcome.subject\" .}}\n")},
		"welcome.html":           {Data: []byte(`{{define "content"}}<p>{{.Name}}</p>{{end}}{{template "_layout.html" .}}`)},
		"welcome.txt":            {Data: []byte(`Hi {{.Name}}`)},
		"auth/reset.subject.txt": {Data: []byte(`Reset`)},
		"auth/reset.txt":         {Data: []byte(`{{.URL}}`)},
		"notes.md":               {Data: []byte(`ignored`)},
	}
}

func TestTemplatesRender(t *testing.T) {
	tpl, err := mail.NewTemplates(templatesFS())
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := i18n.New("en", fstest.MapFS{
		"en.yaml":    {Data: []byte("mail.welcome.subject: \"Welcome, {{.Name}}\"\n")},
		"zh-CN.yaml": {Data: []byte("mail.welcome.subject: \"欢迎，{{.Name}}\"\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]string{"Name": "<Ada>"}

	msg, err := tpl.Render(i18n.WithLocale(context.Background(), bundle, "zh-CN"), "welcome", data)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Subject != "欢迎，<Ada>" {
		t.Errorf("subject = %q", msg.Subject)
	}
	if msg.HTML != "<html><body><p>&lt;Ada&gt;</p></body></html>" {
		t.Errorf("html = %q", msg.HTML)
	}
	if msg.Text != "Hi <Ada>" {
		t.Errorf("text = %q", msg.Text)
	}

	msg, err = tpl.Render(i18n.WithLocale(context.Background(), bundle, "en"), "welcome", data)
	if err != nil || msg.Subject != "Welcome, <Ada>" {
		t.Errorf("en subject = %q, %v", msg.Subject, err)
	}

	msg, err = tpl.Render(context.Background(), "auth/reset", map[string]string{"URL": "https://x/?a=1&b=2"})
	if err != nil || msg.Text != "https://x/?a=1&b=2" || msg.HTML != "" || msg.Subject != "Reset" {
		t.Errorf("reset = %+v, %v", msg, err)
	}

	if _, err := tpl.Render(context.Background(), "missing", nil); err == nil {
		t.Error("missing template rendered")
	}
}

func TestTemplatesConcurrentRender(t *testing.T) {
	tpl, err := mail.NewTemplates(templatesFS())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 8)
	for range 8 {
		go func() {
			_, err := tpl.Render(context.Background(), "welcome", map[string]string{"Name": "Ada"})
			done <- err
		}()
	}
	for range 8 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestTemplatesErrors(t *testing.T) {
	for name, fsys := range map[string]fstest.MapFS{
		"missing subject": {"a.html": {Data: []byte("x")}},
		"missing body":    {"a.subject.txt": {Data: []byte("x")}},
		"parse error":     {"a.subject.txt": {Data: []byte("x")}, "a.txt": {Data: []byte("{{")}},
		"bad partial":     {"_p.html": {Data: []byte("{{end}}")}},
	} {
		if _, err := mail.NewTemplates(fsys); err == nil {
			t.Errorf("%s: no error", name)
		}
	}

	tpl, err := mail.NewTemplates(fstest.MapFS{
		"a.subject.txt": {Data: []byte("{{.}}")}, "a.txt": {Data: []byte("x")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tpl.Render(context.Background(), "a", "line1\nline2"); err == nil || !strings.Contains(err.Error(), "single line") {
		t.Errorf("multiline subject err = %v", err)
	}
}
