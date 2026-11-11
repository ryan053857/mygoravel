package ui_test

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ryan053857/mygoravel/web"
	"github.com/ryan053857/mygoravel/web/ui"
)

type coverForm struct {
	CoverID string   `json:"cover_id" minLength:"1"`
	Images  []string `json:"images,omitempty"`
}

var cover = ui.FileInfo{ID: "0193a3c2-0000-7000-8000-000000000001", Filename: "封面.png", ContentType: "image/png", Size: 1536, URL: "https://cdn.example.com/a.png"}

func TestFileFieldWithFiles(t *testing.T) {
	f := web.NewForm(coverForm{CoverID: cover.ID})
	html := renderIn(t, "zh-CN", ui.FileField(f, "cover_id", "封面", ui.FileFieldProps{Upload: "/uploads", Accept: "image/*", MaxSize: 5 << 20, Files: []ui.FileInfo{cover}}))
	contains(t, html,
		`id="cover_id"`, `hx-preserve="true"`, `data-web-ui-file="/uploads"`, `data-max-size="5242880"`,
		`data-msg-too-large="文件不能超过 5 MB"`, `data-msg-failed="上传失败"`,
		`<input type="hidden" name="cover_id" value="`+cover.ID+`">`, // 已有文件的隐藏字段
		`src="https://cdn.example.com/a.png"`, ">封面.png<", ">1.5 KB<", `aria-label="移除"`,
		`id="cover_id-input"`, `type="file"`, `accept="image/*"`, `for="cover_id-input"`,
		"data-web-ui-file-template",
	)
	lacks(t, html, " multiple")
	// 文件选择框没有 name，不随表单提交；模板中的隐藏字段在 <template> 中，也不提交。
	if strings.Contains(tag(t, html, "cover_id-input"), "name=") {
		t.Error("file input has a name")
	}
}

// 用户移除了文件：表单值中没有它，即使 Files 中有也不显示；验证错误显示在字段下方。
func TestFileFieldRemovedAndInvalid(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(url.Values{"cover_id": {""}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	f, err := web.ParseForm[coverForm](r)
	if err != nil {
		t.Fatal(err)
	}
	html := render(t, ui.FileField(f, "cover_id", "Cover", ui.FileFieldProps{Upload: "/uploads", Files: []ui.FileInfo{cover}}))
	lacks(t, html, `value="`+cover.ID+`"`, "a.png")
	contains(t, html, `data-invalid="true"`, `aria-invalid="true"`, "data-web-ui-file-error")
	if strings.Contains(html, `data-web-ui-file-error="true" hidden`) {
		t.Error("error hidden while the field is invalid")
	}
}

// 多个文件：保持表单值的顺序；Files 中没有信息的 ID 只显示 ID。
func TestFileFieldMultiple(t *testing.T) {
	other := "0193a3c2-0000-7000-8000-000000000002"
	f := web.NewForm(coverForm{Images: []string{other, cover.ID}})
	html := render(t, ui.FileField(f, "images", "Images", ui.FileFieldProps{Upload: "/uploads", Multiple: true, Files: []ui.FileInfo{cover}, Description: "最多 9 张"}))
	contains(t, html, `data-multiple="true"`, ` multiple`, "最多 9 张")
	if i, j := strings.Index(html, `value="`+other+`"`), strings.Index(html, `value="`+cover.ID+`"`); i < 0 || j < 0 || i > j {
		t.Errorf("order: %d %d", i, j)
	}
	contains(t, html, ">"+other+"<", `data-web-ui-file-error="true" hidden`)
}
