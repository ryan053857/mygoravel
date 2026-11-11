// Package upload 是页面中上传文件的接口，与 web/ui 的 FileField 配合：
// 用户选择文件后立即上传为临时文件，表单只提交文件 ID，业务保存时用 filesystem.Service.Confirm 确认。
//
//	pages.Routes(r, web.Require()).Post("/uploads", upload.Handler(files, upload.Options{}))
//
// 磁盘支持客户端直传（S3）时，浏览器拿到签名后把文件直接传到存储，正文不经过应用服务器；
// 否则（本地磁盘）文件通过这个接口上传。
package upload

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/filesystem"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/web"
	"github.com/ryan053857/mygoravel/web/ui"
)

// ErrBadRequest 表示上传请求的格式不正确。
var ErrBadRequest = errorx.Define(errorx.InvalidArgument, "upload.bad_request")

// Options 配置 Handler。
type Options struct {
	// Disk 是保存文件的磁盘，为空时是默认磁盘。
	Disk string
}

// meta 是浏览器在上传前发送的文件信息。
type meta struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

// response 是上传接口的回应。Direct 为 false 时浏览器改为以 multipart 表单上传到同一个地址。
type response struct {
	File   *ui.FileInfo              `json:"file,omitempty"`
	Direct bool                      `json:"direct"`
	Put    *filesystem.SignedRequest `json:"put,omitempty"`
	Error  string                    `json:"error,omitempty"`
}

// Handler 返回上传接口的处理器，注册为需要登录的页面路由（POST）。上传者是当前主体，
// 只有本人能确认这些临时文件。它接受两种请求：
//
//   - JSON（filename、content_type、size）：磁盘支持直传时登记文件并签名，回应 file 和 put，
//     浏览器按 put 把文件传到存储；不支持时回应 direct 为 false；
//   - multipart 表单的 file 字段：通过应用服务器上传，回应 file。请求体受 httpx.BodyLimit 限制。
//
// 出错时回应 JSON 的 error（按请求的语言翻译）和对应的状态码，FileField 把它显示在字段下方。
func Handler(files *filesystem.Service, opts Options) web.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		res, err := upload(r, files, opts)
		if err != nil {
			status := httpx.StatusOf(err)
			if status >= http.StatusInternalServerError {
				return err // 交给页面的错误处理记录日志
			}
			return reply(w, status, response{Error: httpx.ErrorDetail(r.Context(), err)})
		}
		return reply(w, http.StatusOK, res)
	}
}

func upload(r *http.Request, files *filesystem.Service, opts Options) (response, error) {
	ctx := r.Context()
	owner, ok := identity.FromContext(ctx)
	if !ok {
		return response{}, web.ErrForbidden.New()
	}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch mt {
	case "application/json":
		var m meta
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4096)).Decode(&m); err != nil {
			return response{}, ErrBadRequest.Wrap(err)
		}
		if !files.SupportsDirect(opts.Disk) {
			return response{Direct: false}, nil
		}
		f, err := files.Prepare(ctx, owner, filesystem.FileInput{Disk: opts.Disk, Filename: m.Filename, ContentType: m.ContentType, Size: m.Size})
		if err != nil {
			return response{}, err
		}
		put, err := files.SignPut(ctx, owner, f.ID)
		if err != nil {
			return response{}, err
		}
		info := Info(f)
		return response{File: &info, Direct: true, Put: &put}, nil
	case "multipart/form-data":
		body, header, err := r.FormFile("file")
		if err != nil {
			return response{}, ErrBadRequest.Wrap(err)
		}
		defer body.Close()
		f, err := files.Upload(ctx, owner, filesystem.FileInput{
			Disk: opts.Disk, Filename: header.Filename, ContentType: header.Header.Get("Content-Type"), Size: header.Size,
		}, body)
		if err != nil {
			return response{}, err
		}
		info := Info(f)
		return response{File: &info}, nil
	}
	return response{}, ErrBadRequest.New()
}

func reply(w http.ResponseWriter, status int, res response) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(res)
}

// Info 把文件转换为 FileField 显示的信息。
func Info(f filesystem.File) ui.FileInfo {
	return ui.FileInfo{ID: f.ID.String(), Filename: f.Filename, ContentType: f.ContentType, Size: f.Size, URL: f.URL}
}

// Files 查出 ids 对应的文件，供 FileField 显示：编辑页显示已保存的文件，整页表单验证失败后显示已上传的文件。
// 不存在或已删除的 ID 被忽略。
func Files(ctx context.Context, files *filesystem.Service, ids ...string) ([]ui.FileInfo, error) {
	var out []ui.FileInfo
	for _, s := range ids {
		id, err := uuid.Parse(s)
		if err != nil {
			continue
		}
		f, err := files.Get(ctx, id)
		if errors.Is(err, filesystem.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, Info(f))
	}
	return out, nil
}
