package upload_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexedwards/scs/v2/memstore"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/filesystem"
	"github.com/ryan053857/mygoravel/filesystem/local"
	s3disk "github.com/ryan053857/mygoravel/filesystem/s3"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/session"
	"github.com/ryan053857/mygoravel/web"
	"github.com/ryan053857/mygoravel/web/ui"
	"github.com/ryan053857/mygoravel/web/upload"
	"github.com/ryan053857/mygoravel/web/webtest"
)

var alice = identity.Subject{ID: "alice", Type: "user"}

// setup 返回文件服务（local 与 s3 两个磁盘，max_upload_size 1MiB）和注册了 /uploads、/uploads/s3 的页面。
func setup(t *testing.T) (*filesystem.Service, http.Handler, *web.Pages) {
	t.Helper()
	db := dbtesting.Open(t, sqlite.Driver, filesystem.Migrations())
	localDisk, err := local.Open(filesystem.LocalConfig{Root: filepath.Join(t.TempDir(), "files"), PublicURL: "/storage"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = localDisk.Close() })
	server := httptest.NewServer(gofakes3.New(s3mem.New()).Server())
	t.Cleanup(server.Close)
	client := awss3.New(awss3.Options{
		BaseEndpoint: aws.String(server.URL), Region: "us-east-1", UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider("k", "s", ""),
	})
	if _, err := client.CreateBucket(context.Background(), &awss3.CreateBucketInput{Bucket: aws.String("test")}); err != nil {
		t.Fatal(err)
	}
	remote, err := s3disk.New(client, "test", "https://cdn.example.com")
	if err != nil {
		t.Fatal(err)
	}
	var cfg filesystem.Config
	cfg.SetDefaults()
	cfg.MaxUploadSize = filesystem.MiB
	files, err := filesystem.New(db, cfg, map[string]filesystem.Disk{"local": localDisk, "s3": remote})
	if err != nil {
		t.Fatal(err)
	}

	var scfg session.Config
	scfg.SetDefaults()
	pages := web.New(web.Options{Accounts: identity.Accounts{"user": nil},
		Sessions: session.New(scfg, memstore.New()), Login: "/login",
		ErrorPage: ui.ErrorPage,
	})
	r := chi.NewRouter()
	r.Use(httpx.Locale(nil))
	uploads := pages.Routes(r, web.Require())
	uploads.Post("/uploads", upload.Handler(files, upload.Options{}))
	uploads.Post("/uploads/s3", upload.Handler(files, upload.Options{Disk: "s3"}))
	return files, r, pages
}

type reply struct {
	File   *ui.FileInfo              `json:"file"`
	Direct bool                      `json:"direct"`
	Put    *filesystem.SignedRequest `json:"put"`
	Error  string                    `json:"error"`
}

func decode(t *testing.T, res *webtest.Response) reply {
	t.Helper()
	if ct := res.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type %q: %d %s", ct, res.Code, res.Text())
	}
	var r reply
	if err := json.Unmarshal(res.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func postJSON(c *webtest.Client, path, body string) *webtest.Response {
	r := httptest.NewRequest(http.MethodPost, webtest.Origin+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return c.Do(r)
}

func postFile(t *testing.T, c *webtest.Client, path, filename, content string) *webtest.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte(content))
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, webtest.Origin+path, &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return c.Do(r)
}

// 本地磁盘：不支持直传，文件以 multipart 表单上传；上传者之后可以确认它。
func TestServerUpload(t *testing.T) {
	files, h, pages := setup(t)
	c := webtest.New(t, h)

	if res := c.Post("/uploads", nil); res.Code != http.StatusSeeOther {
		t.Fatalf("anonymous: %d", res.Code)
	}
	c.Login(pages, alice)

	if r := decode(t, postJSON(c, "/uploads", `{"filename":"a.txt","content_type":"text/plain","size":5}`)); r.Direct || r.File != nil {
		t.Fatalf("local disk offered direct upload: %+v", r)
	}
	res := postFile(t, c, "/uploads", "notes.txt", "hello")
	r := decode(t, res)
	if res.Code != http.StatusOK || r.File == nil || r.File.Filename != "notes.txt" || r.File.Size != 5 || !strings.HasPrefix(r.File.URL, "/storage/") {
		t.Fatalf("upload: %d %+v", res.Code, r)
	}
	id := uuid.MustParse(r.File.ID)
	if f, err := files.Confirm(context.Background(), alice, id, filesystem.Accept{}); err != nil || f.Status != filesystem.StatusReady {
		t.Fatalf("confirm: %+v %v", f, err)
	}
	// 只有上传者能确认。
	if _, err := files.Confirm(context.Background(), identity.Subject{ID: "bob", Type: "user"}, id, filesystem.Accept{}); err == nil {
		t.Fatal("bob confirmed alice's file")
	}
}

// S3 磁盘：登记并签名，浏览器按签名把文件直接传到存储，之后可以确认。
func TestDirectUpload(t *testing.T) {
	files, h, pages := setup(t)
	c := webtest.New(t, h)
	c.Login(pages, alice)

	content := "%PDF-1.4 fake"
	r := decode(t, postJSON(c, "/uploads/s3", `{"filename":"doc.pdf","content_type":"application/pdf","size":`+itoa(len(content))+`}`))
	if !r.Direct || r.Put == nil || r.File == nil || !strings.HasPrefix(r.File.URL, "https://cdn.example.com/") {
		t.Fatalf("prepare: %+v", r)
	}
	req, _ := http.NewRequest(r.Put.Method, r.Put.URL, strings.NewReader(content))
	for k, vs := range r.Put.Headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put: %d", resp.StatusCode)
	}
	if _, err := files.Confirm(context.Background(), alice, uuid.MustParse(r.File.ID), filesystem.Accept{}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
}

// 错误以 JSON 回应，按请求的语言翻译。
func TestUploadErrors(t *testing.T) {
	_, h, pages := setup(t)
	c := webtest.New(t, h)
	c.Login(pages, alice)

	res := postJSON(c, "/uploads/s3", `{"filename":"big.bin","content_type":"application/octet-stream","size":2097152}`)
	if r := decode(t, res); res.Code != http.StatusUnprocessableEntity || r.Error == "" {
		t.Fatalf("too large: %d %+v", res.Code, r)
	}
	r := httptest.NewRequest(http.MethodPost, webtest.Origin+"/uploads", strings.NewReader("x"))
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("Accept-Language", "en")
	res = c.Do(r)
	if got := decode(t, res); res.Code != http.StatusUnprocessableEntity || got.Error == "" {
		t.Fatalf("bad request: %d %+v", res.Code, got)
	}
}

func TestFiles(t *testing.T) {
	files, h, pages := setup(t)
	c := webtest.New(t, h)
	c.Login(pages, alice)
	r := decode(t, postFile(t, c, "/uploads", "a.png", "x"))

	infos, err := upload.Files(context.Background(), files, r.File.ID, "not-a-uuid", uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != r.File.ID || infos[0].Filename != "a.png" {
		t.Fatalf("infos = %+v", infos)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
