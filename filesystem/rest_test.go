package filesystem_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/filesystem"
	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
)

// 以下输入、输出和处理器与 docs/filesystem.md 中 REST 一节的示例保持一致。

type UploadFileInput struct {
	RawBody huma.MultipartFormFiles[struct {
		File huma.FormFile `form:"file" required:"true"`
	}]
}

type FileOutput struct {
	Body filesystem.File
}

type PrepareFileInput struct {
	Body struct {
		Filename    string `json:"filename" minLength:"1"`
		ContentType string `json:"content_type,omitempty"`
		Size        int64  `json:"size" minimum:"0"`
	}
}

type FileIDInput struct {
	ID uuid.UUID `path:"id"`
}

type SignedOutput struct {
	Body filesystem.SignedRequest
}

type MultipartOutput struct {
	Body filesystem.Multipart
}

type SignPartInput struct {
	ID       uuid.UUID `path:"id"`
	UploadID string    `path:"uploadID"`
	Number   int32     `path:"number" minimum:"1" maximum:"10000"`
}

type CompleteMultipartInput struct {
	ID       uuid.UUID `path:"id"`
	UploadID string    `path:"uploadID"`
	Body     struct {
		Parts []filesystem.Part `json:"parts" minItems:"1"`
	}
}

type UploadIDInput struct {
	ID       uuid.UUID `path:"id"`
	UploadID string    `path:"uploadID"`
}

type NoContentOutput struct{}

type CreateArticleInput struct {
	Body struct {
		Title   string    `json:"title"`
		CoverID uuid.UUID `json:"cover_id"`
	}
}

type ArticleOutput struct {
	Body struct {
		Title    string `json:"title"`
		CoverURL string `json:"cover_url"`
	}
}

func registerFileRoutes(api *rest.API, auth func(*huma.Operation), files *filesystem.Service) {
	upload := huma.Operation{OperationID: "file-upload", Method: http.MethodPost, Path: "/files", DefaultStatus: http.StatusCreated}
	auth(&upload)
	rest.Register(api, upload, func(ctx context.Context, in *UploadFileInput) (*FileOutput, error) {
		owner, _ := identity.FromContext(ctx)
		f := in.RawBody.Data().File
		file, err := files.Upload(ctx, owner, filesystem.FileInput{
			Filename: f.Filename, ContentType: f.ContentType, Size: f.Size,
		}, f.File)
		if err != nil {
			return nil, err
		}
		return &FileOutput{Body: file}, nil
	})

	prepare := huma.Operation{OperationID: "file-prepare", Method: http.MethodPost, Path: "/files/prepare", DefaultStatus: http.StatusCreated}
	auth(&prepare)
	rest.Register(api, prepare, func(ctx context.Context, in *PrepareFileInput) (*FileOutput, error) {
		owner, _ := identity.FromContext(ctx)
		file, err := files.Prepare(ctx, owner, filesystem.FileInput{
			Disk: "s3", Filename: in.Body.Filename, ContentType: in.Body.ContentType, Size: in.Body.Size,
		})
		if err != nil {
			return nil, err
		}
		return &FileOutput{Body: file}, nil
	})

	sign := huma.Operation{OperationID: "file-sign-put", Method: http.MethodPost, Path: "/files/{id}/sign"}
	auth(&sign)
	rest.Register(api, sign, func(ctx context.Context, in *FileIDInput) (*SignedOutput, error) {
		owner, _ := identity.FromContext(ctx)
		signed, err := files.SignPut(ctx, owner, in.ID)
		if err != nil {
			return nil, err
		}
		return &SignedOutput{Body: signed}, nil
	})

	createMultipart := huma.Operation{OperationID: "file-multipart-create", Method: http.MethodPost, Path: "/files/{id}/multipart", DefaultStatus: http.StatusCreated}
	auth(&createMultipart)
	rest.Register(api, createMultipart, func(ctx context.Context, in *FileIDInput) (*MultipartOutput, error) {
		owner, _ := identity.FromContext(ctx)
		upload, err := files.CreateMultipart(ctx, owner, in.ID)
		if err != nil {
			return nil, err
		}
		return &MultipartOutput{Body: upload}, nil
	})

	signPart := huma.Operation{OperationID: "file-multipart-sign-part", Method: http.MethodPost, Path: "/files/{id}/multipart/{uploadID}/parts/{number}/sign"}
	auth(&signPart)
	rest.Register(api, signPart, func(ctx context.Context, in *SignPartInput) (*SignedOutput, error) {
		owner, _ := identity.FromContext(ctx)
		signed, err := files.SignPart(ctx, owner, in.ID, in.UploadID, in.Number)
		if err != nil {
			return nil, err
		}
		return &SignedOutput{Body: signed}, nil
	})

	complete := huma.Operation{OperationID: "file-multipart-complete", Method: http.MethodPost, Path: "/files/{id}/multipart/{uploadID}/complete"}
	auth(&complete)
	rest.Register(api, complete, func(ctx context.Context, in *CompleteMultipartInput) (*FileOutput, error) {
		owner, _ := identity.FromContext(ctx)
		file, err := files.CompleteMultipart(ctx, owner, in.ID, in.UploadID, in.Body.Parts)
		if err != nil {
			return nil, err
		}
		return &FileOutput{Body: file}, nil
	})

	abort := huma.Operation{OperationID: "file-multipart-abort", Method: http.MethodDelete, Path: "/files/{id}/multipart/{uploadID}", DefaultStatus: http.StatusNoContent}
	auth(&abort)
	rest.Register(api, abort, func(ctx context.Context, in *UploadIDInput) (*NoContentOutput, error) {
		owner, _ := identity.FromContext(ctx)
		if err := files.AbortMultipart(ctx, owner, in.ID, in.UploadID); err != nil {
			return nil, err
		}
		return &NoContentOutput{}, nil
	})

	create := huma.Operation{OperationID: "article-create", Method: http.MethodPost, Path: "/articles", DefaultStatus: http.StatusCreated}
	auth(&create)
	rest.Register(api, create, func(ctx context.Context, in *CreateArticleInput) (*ArticleOutput, error) {
		owner, _ := identity.FromContext(ctx)
		cover, err := files.Confirm(ctx, owner, in.Body.CoverID, filesystem.Accept{})
		if err != nil {
			return nil, err
		}
		out := &ArticleOutput{}
		out.Body.Title, out.Body.CoverURL = in.Body.Title, cover.URL
		return out, nil
	})
}

func TestRESTUploadAndConfirm(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		e := newEnv(t, driver)
		api := rest.New(rest.Options{Title: "test"})
		users := map[string]identity.Subject{"alice": alice, "bob": bob}
		auth := rest.RequireBearer(api, func(_ context.Context, token string) (identity.Subject, error) {
			return users[token], nil
		})
		registerFileRoutes(api, auth, e.svc)
		server := httptest.NewServer(api.Handler())
		t.Cleanup(server.Close)

		call := func(token, method, path, contentType string, body []byte, out any) (int, map[string]any) {
			t.Helper()
			req, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", contentType)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var raw json.RawMessage
			_ = json.NewDecoder(resp.Body).Decode(&raw)
			var problem map[string]any
			_ = json.Unmarshal(raw, &problem)
			if out != nil {
				_ = json.Unmarshal(raw, out)
			}
			return resp.StatusCode, problem
		}
		form := func(name, content string) (string, []byte) {
			var buf bytes.Buffer
			w := multipart.NewWriter(&buf)
			part, _ := w.CreateFormFile("file", name)
			_, _ = part.Write([]byte(content))
			_ = w.Close()
			return w.FormDataContentType(), buf.Bytes()
		}
		jsonBody := func(v any) []byte {
			b, _ := json.Marshal(v)
			return b
		}

		ct, body := form("cover.png", "png-bytes")
		var file filesystem.File
		if code, p := call("alice", http.MethodPost, "/files", ct, body, &file); code != http.StatusCreated {
			t.Fatalf("upload = %d %v", code, p)
		}
		if file.Status != filesystem.StatusTemporary || file.Size != 9 || !strings.HasSuffix(file.URL, ".png") {
			t.Fatalf("file = %+v", file)
		}

		// 他人引用临时文件得到 404 和业务错误码。
		code, p := call("bob", http.MethodPost, "/articles", "application/json", jsonBody(map[string]any{"title": "x", "cover_id": file.ID}), nil)
		if code != http.StatusNotFound || p["code"] != "filesystem.not_found" {
			t.Fatalf("bob confirm = %d %v", code, p)
		}
		var article struct {
			CoverURL string `json:"cover_url"`
		}
		if code, p := call("alice", http.MethodPost, "/articles", "application/json", jsonBody(map[string]any{"title": "x", "cover_id": file.ID}), &article); code != http.StatusCreated || article.CoverURL != file.URL {
			t.Fatalf("create article = %d %v %+v", code, p, article)
		}

		// S3 单次直传：未上传就确认得到 409，签名上传后确认成功。
		var prepared filesystem.File
		if code, p := call("alice", http.MethodPost, "/files/prepare", "application/json", jsonBody(map[string]any{"filename": "a.txt", "size": 3}), &prepared); code != http.StatusCreated {
			t.Fatalf("prepare = %d %v", code, p)
		}
		code, p = call("alice", http.MethodPost, "/articles", "application/json", jsonBody(map[string]any{"title": "x", "cover_id": prepared.ID}), nil)
		if code != http.StatusConflict || p["code"] != "filesystem.not_uploaded" {
			t.Fatalf("confirm before upload = %d %v", code, p)
		}
		var signed filesystem.SignedRequest
		if code, p := call("alice", http.MethodPost, "/files/"+prepared.ID.String()+"/sign", "application/json", nil, &signed); code != http.StatusOK || signed.Method != http.MethodPut {
			t.Fatalf("sign = %d %v", code, p)
		}
		sendSigned(t, signed, []byte("abc"))
		if code, p := call("alice", http.MethodPost, "/articles", "application/json", jsonBody(map[string]any{"title": "x", "cover_id": prepared.ID}), nil); code != http.StatusCreated {
			t.Fatalf("confirm direct upload = %d %v", code, p)
		}

		// S3 分片直传：创建会话、逐片签名上传、合并，再由业务接口确认。
		data := bytes.Repeat([]byte("z"), 16<<20+10)
		var big filesystem.File
		if code, p := call("alice", http.MethodPost, "/files/prepare", "application/json", jsonBody(map[string]any{"filename": "big.bin", "size": len(data)}), &big); code != http.StatusCreated {
			t.Fatalf("prepare big = %d %v", code, p)
		}
		var up filesystem.Multipart
		if code, p := call("alice", http.MethodPost, "/files/"+big.ID.String()+"/multipart", "application/json", nil, &up); code != http.StatusCreated || up.PartCount != 2 {
			t.Fatalf("create multipart = %d %v %+v", code, p, up)
		}
		var parts []filesystem.Part
		for n := 1; n <= up.PartCount; n++ {
			var req filesystem.SignedRequest
			path := "/files/" + big.ID.String() + "/multipart/" + up.UploadID + "/parts/" + strconv.Itoa(n) + "/sign"
			if code, p := call("alice", http.MethodPost, path, "application/json", nil, &req); code != http.StatusOK {
				t.Fatalf("sign part %d = %d %v", n, code, p)
			}
			chunk := data[int64(n-1)*up.PartSize : min(int64(n)*up.PartSize, int64(len(data)))]
			parts = append(parts, filesystem.Part{Number: int32(n), ETag: sendSigned(t, req, chunk)})
		}
		completePath := "/files/" + big.ID.String() + "/multipart/" + up.UploadID + "/complete"
		if code, p := call("alice", http.MethodPost, completePath, "application/json", jsonBody(map[string]any{"parts": parts}), nil); code != http.StatusOK {
			t.Fatalf("complete = %d %v", code, p)
		}
		if code, p := call("alice", http.MethodPost, "/articles", "application/json", jsonBody(map[string]any{"title": "x", "cover_id": big.ID}), nil); code != http.StatusCreated {
			t.Fatalf("confirm multipart = %d %v", code, p)
		}

		// 放弃分片：204，之后会话不可再用于签名合并。
		var abandoned filesystem.File
		call("alice", http.MethodPost, "/files/prepare", "application/json", jsonBody(map[string]any{"filename": "x.bin", "size": len(data)}), &abandoned)
		var session filesystem.Multipart
		call("alice", http.MethodPost, "/files/"+abandoned.ID.String()+"/multipart", "application/json", nil, &session)
		if code, p := call("alice", http.MethodDelete, "/files/"+abandoned.ID.String()+"/multipart/"+session.UploadID, "application/json", nil, nil); code != http.StatusNoContent {
			t.Fatalf("abort = %d %v", code, p)
		}

		tooBig, _ := json.Marshal(map[string]any{"filename": "a.bin", "size": int64(2 * filesystem.GiB)})
		code, p = call("alice", http.MethodPost, "/files/prepare", "application/json", tooBig, nil)
		if code != http.StatusUnprocessableEntity || p["code"] != "filesystem.too_large" {
			t.Fatalf("too large = %d %v", code, p)
		}
	})
}
