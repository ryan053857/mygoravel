package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/a-h/templ"
)

// FileInfo 是 FileField 中显示的一个文件，通常由 upload.Info、upload.Files 从 filesystem.File 转换而来。
type FileInfo struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	// URL 是文件的公开地址，图片据此显示缩略图。
	URL string `json:"url"`
}

// FileFieldProps 配置 FileField。
type FileFieldProps struct {
	// Upload 是上传接口的地址，即注册了 upload.Handler 的页面路由。必填。
	Upload string
	// Accept 是文件选择框的 accept 属性，例如 image/*、.pdf，只用于筛选；文件的真实类型由 Confirm 检查。
	Accept string
	// MaxSize 是允许的最大字节数，超过时在浏览器中直接提示，不上传；0 表示不限制（服务端仍受 max_upload_size 限制）。
	MaxSize int64
	// Multiple 允许选择多个文件，字段对应表单结构体中的字符串切片。
	Multiple bool
	// Files 是要显示的文件：编辑页已保存的文件，或整页表单验证失败后已上传的文件，见 upload.Files。
	// htmx 表单验证失败重新渲染时，字段保留浏览器中已上传的文件，不需要传入。
	Files []FileInfo
	// ID 是字段的 id，默认等于字段名。
	ID          string
	Description string
	Class       string
}

// fileItems 返回要显示的文件：表单值中的每个 ID，按表单值的顺序，文件名、地址等取自 files；
// files 中没有的 ID 只显示 ID。用户移除了文件时表单值中没有它，即使 files 中有也不显示。
func fileItems(f Form, name string, files []FileInfo) []FileInfo {
	byID := map[string]FileInfo{}
	for _, fi := range files {
		byID[fi.ID] = fi
	}
	var out []FileInfo
	for _, id := range f.Values(name) {
		if id == "" {
			continue
		}
		fi, ok := byID[id]
		if !ok {
			fi = FileInfo{ID: id, Filename: id}
		}
		out = append(out, fi)
	}
	return out
}

func isImage(fi FileInfo) bool { return strings.HasPrefix(fi.ContentType, "image/") && fi.URL != "" }

// humanSize 把字节数格式化为 1.5 MB 这样的文字。
func humanSize(n int64) string {
	if n <= 0 {
		return ""
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v, i := float64(n), 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return strconv.FormatInt(n, 10) + " B"
	}
	return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.1f", v), "0"), ".") + " " + units[i]
}

func multipleAttr(multiple bool) templ.Attributes {
	if !multiple {
		return nil
	}
	return templ.Attributes{"multiple": true}
}
