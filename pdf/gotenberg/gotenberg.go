// Package gotenberg 通过 Gotenberg（https://gotenberg.dev）中的 Chromium 生成 PDF。
//
// 每次生成把 HTML 作为 index.html、Assets 中的文件一起上传到 /forms/chromium/convert/html，
// 并固定带上这些参数：A4 作为默认纸张，preferCssPageSize 让 CSS @page 生效，printBackground
// 打印背景色，failOnResourceLoadingFailed 让引用的文件加载失败时报错而不是生成没有样式的 PDF。
package gotenberg

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/pdf"
)

// Config 是 Gotenberg 的连接配置。
type Config struct {
	// URL 是 Gotenberg 的地址，例如 http://gotenberg:3000。
	URL string `yaml:"url"`
	// Username 和 Password 可选，对应 Gotenberg 的 --api-enable-basic-auth。
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// Validate 检查 url 是 http 或 https 地址。
func (c Config) Validate() error {
	if c.URL == "" {
		return errors.New("url is required")
	}
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.Newf("url %q must be an http or https address", c.URL)
	}
	return nil
}

// Renderer 调用 Gotenberg 生成 PDF，可以并发使用。
type Renderer struct {
	endpoint string
	cfg      Config
	client   *http.Client
}

var _ pdf.Renderer = (*Renderer)(nil)

// New 创建 Renderer，不访问网络。
func New(cfg Config) (*Renderer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "gotenberg")
	}
	return &Renderer{
		endpoint: strings.TrimRight(cfg.URL, "/") + "/forms/chromium/convert/html",
		cfg:      cfg,
		client:   &http.Client{},
	}, nil
}

// fields 是每次请求都带上的表单参数。
var fields = [][2]string{
	{"paperWidth", "210mm"},
	{"paperHeight", "297mm"},
	{"preferCssPageSize", "true"},
	{"printBackground", "true"},
	{"failOnResourceLoadingFailed", "true"},
}

// Render 生成 PDF。连不上 Gotenberg、排队已满（429）或超时（503）时返回 pdf.ErrUnavailable；
// 其他失败（例如引用的文件加载失败）返回带有 Gotenberg 说明的普通错误。
func (r *Renderer) Render(ctx context.Context, doc pdf.Document) ([]byte, error) {
	body, contentType, err := form(ctx, doc)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, body)
	if err != nil {
		return nil, errors.Wrap(err, "gotenberg: create request")
	}
	req.Header.Set("Content-Type", contentType)
	if r.cfg.Username != "" || r.cfg.Password != "" {
		req.SetBasicAuth(r.cfg.Username, r.cfg.Password)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, pdf.ErrUnavailable.Wrap(errors.Wrap(err, "gotenberg"))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, errors.Wrap(err, "gotenberg: read pdf")
		}
		return data, nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	err = errors.Newf("gotenberg: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		return nil, pdf.ErrUnavailable.Wrap(err)
	}
	return nil, err
}

// form 编码请求体：index.html、Assets 的顶层文件和固定参数。
func form(ctx context.Context, doc pdf.Document) (*bytes.Buffer, string, error) {
	if doc.HTML == nil {
		return nil, "", errors.New("gotenberg: document has no HTML")
	}
	var html bytes.Buffer
	if err := doc.HTML.Render(ctx, &html); err != nil {
		return nil, "", errors.Wrap(err, "gotenberg: render html")
	}
	body := new(bytes.Buffer)
	w := multipart.NewWriter(body)
	if err := addFile(w, "index.html", html.Bytes()); err != nil {
		return nil, "", err
	}
	if doc.Assets != nil {
		entries, err := fs.ReadDir(doc.Assets, ".")
		if err != nil {
			return nil, "", errors.Wrap(err, "gotenberg: read assets")
		}
		for _, e := range entries {
			name := e.Name()
			switch {
			case e.IsDir():
				return nil, "", errors.Newf("gotenberg: assets: %q is a directory; assets must be top-level files referenced by name", name)
			case name == "index.html":
				return nil, "", errors.New("gotenberg: assets: index.html is reserved for the document")
			}
			data, err := fs.ReadFile(doc.Assets, name)
			if err != nil {
				return nil, "", errors.Wrapf(err, "gotenberg: read asset %q", name)
			}
			if err := addFile(w, name, data); err != nil {
				return nil, "", err
			}
		}
	}
	for _, f := range fields {
		if err := w.WriteField(f[0], f[1]); err != nil {
			return nil, "", errors.Wrap(err, "gotenberg: encode form")
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", errors.Wrap(err, "gotenberg: encode form")
	}
	return body, w.FormDataContentType(), nil
}

func addFile(w *multipart.Writer, name string, data []byte) error {
	part, err := w.CreateFormFile("files", name)
	if err == nil {
		_, err = part.Write(data)
	}
	return errors.Wrap(err, "gotenberg: encode form")
}
