//go:build integration

package files_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/files"
	"github.com/ryan053857/mygoravel/files/local"
	s3disk "github.com/ryan053857/mygoravel/files/s3"
	"github.com/ryan053857/mygoravel/identity"
)

var (
	alice = identity.Subject{ID: "alice", Type: identity.SubjectUser}
	bob   = identity.Subject{ID: "bob", Type: identity.SubjectUser}
)

type env struct {
	db    *bun.DB
	svc   *files.Service
	local *local.Disk
	s3    *s3disk.Disk
}

func newEnv(t *testing.T) env {
	t.Helper()
	db := dbtest.Open(t, files.Migrations())
	localDisk, err := local.Open(local.Config{Root: filepath.Join(t.TempDir(), "files"), PublicURL: "/storage"})
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
	var cfg files.Config
	cfg.SetDefaults()
	svc, err := files.New(db, cfg, map[string]files.Disk{"local": localDisk, "s3": remote})
	if err != nil {
		t.Fatal(err)
	}
	return env{db: db, svc: svc, local: localDisk, s3: remote}
}

func (e env) expire(t *testing.T, id uuid.UUID) {
	t.Helper()
	if _, err := e.db.NewUpdate().Model((*files.Record)(nil)).Set("expires_at = ?", time.Now().Add(-time.Second)).
		Where("id = ?", id).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (e env) rows(t *testing.T) int {
	t.Helper()
	n, err := e.db.NewSelect().Model((*files.Record)(nil)).Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func upload(t *testing.T, svc *files.Service, owner identity.Subject, body string) files.Record {
	t.Helper()
	r, err := svc.Upload(context.Background(), owner, files.FileInput{Filename: "Photo.JPG", ContentType: "image/jpeg", Size: int64(len(body))}, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestUploadConfirmGetOpen(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := upload(t, e.svc, alice, "hello")
	if r.Status != files.StatusTemporary || r.ExpiresAt == nil || !strings.HasSuffix(r.Key, ".jpg") {
		t.Fatalf("uploaded = %+v", r)
	}
	if r.URL != "/storage/"+r.Key {
		t.Fatalf("URL = %s", r.URL)
	}
	if _, err := e.svc.Confirm(ctx, bob, r.ID); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("other owner confirmed: %v", err)
	}
	confirmed, err := e.svc.Confirm(ctx, alice, r.ID)
	if err != nil || confirmed.Status != files.StatusReady || confirmed.ExpiresAt != nil || confirmed.URL != r.URL {
		t.Fatalf("confirm = %+v %v", confirmed, err)
	}
	if again, err := e.svc.Confirm(ctx, alice, r.ID); err != nil || again.Status != files.StatusReady {
		t.Fatalf("repeat confirm = %+v %v", again, err)
	}
	got, err := e.svc.Get(ctx, r.ID)
	if err != nil || got.Status != files.StatusReady || got.URL != r.URL {
		t.Fatalf("get = %+v %v", got, err)
	}
	body, _, err := e.svc.Open(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(body)
	_ = body.Close()
	if string(data) != "hello" {
		t.Fatalf("open = %q", data)
	}
	if _, err := e.svc.Get(ctx, uuid.New()); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("missing get = %v", err)
	}
}

func TestUploadValidation(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	cases := map[string]struct {
		in   files.FileInput
		body string
		want error
	}{
		"short":    {files.FileInput{Filename: "a.txt", Size: 10}, "abc", files.ErrInvalid},
		"long":     {files.FileInput{Filename: "a.txt", Size: 2}, "abc", files.ErrInvalid},
		"too big":  {files.FileInput{Filename: "a.txt", Size: int64(2 * files.GiB)}, "", files.ErrTooLarge},
		"no name":  {files.FileInput{Filename: " ", Size: 0}, "", files.ErrInvalid},
		"bad disk": {files.FileInput{Disk: "nope", Filename: "a", Size: 0}, "", files.ErrInvalid},
	}
	for name, c := range cases {
		if _, err := e.svc.Upload(ctx, alice, c.in, strings.NewReader(c.body)); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
	if n := e.rows(t); n != 0 {
		t.Fatalf("failed uploads left %d rows", n)
	}
	empty := upload(t, e.svc, alice, "")
	if _, err := e.svc.Confirm(ctx, alice, empty.ID); err != nil {
		t.Fatalf("empty file: %v", err)
	}
}

func TestConfirmRequiresUploadedObject(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r, err := e.svc.Prepare(ctx, alice, files.FileInput{Filename: "a.txt", Size: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Confirm(ctx, alice, r.ID); !errors.Is(err, files.ErrNotUploaded) {
		t.Fatalf("confirm without object = %v", err)
	}
	if err := e.local.Put(ctx, r.Key, "", strings.NewReader("ab"), 2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Confirm(ctx, alice, r.ID); !errors.Is(err, files.ErrNotUploaded) {
		t.Fatalf("confirm with wrong size = %v", err)
	}
	if _, err := e.svc.SignPut(ctx, alice, r.ID); !errors.Is(err, files.ErrUnsupported) {
		t.Fatalf("local SignPut = %v", err)
	}
	e.expire(t, r.ID)
	if _, err := e.svc.Confirm(ctx, alice, r.ID); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("expired confirm = %v", err)
	}
}

func TestConcurrentConfirm(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := upload(t, e.svc, alice, "data")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			got, err := e.svc.Confirm(ctx, alice, r.ID)
			if err == nil && got.Status != files.StatusReady {
				err = errors.New("not ready")
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeleteAndCleanup(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	ready := upload(t, e.svc, alice, "keep")
	if _, err := e.svc.Confirm(ctx, alice, ready.ID); err != nil {
		t.Fatal(err)
	}
	stale := upload(t, e.svc, alice, "stale")
	e.expire(t, stale.ID)
	fresh := upload(t, e.svc, alice, "fresh")

	n, err := e.svc.Cleanup(ctx, 10)
	if err != nil || n != 1 {
		t.Fatalf("cleanup = %d %v", n, err)
	}
	if _, err := e.local.Stat(ctx, stale.Key); err == nil {
		t.Fatal("stale object kept")
	}
	if _, err := e.svc.Get(ctx, stale.ID); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("stale record = %v", err)
	}
	for _, r := range []files.Record{ready, fresh} {
		if _, err := e.svc.Get(ctx, r.ID); err != nil {
			t.Fatalf("%s removed: %v", r.Status, err)
		}
	}

	if err := e.svc.Delete(ctx, ready.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(ctx, ready.ID); err != nil {
		t.Fatalf("repeat delete = %v", err)
	}
	if _, err := e.local.Stat(ctx, ready.Key); err == nil {
		t.Fatal("deleted object kept")
	}
	if n := e.rows(t); n != 1 {
		t.Fatalf("rows = %d, want only fresh", n)
	}
}

func TestDeleteInTransaction(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	kept := upload(t, e.svc, alice, "kept")
	gone := upload(t, e.svc, alice, "gone")

	rollback := errors.New("rollback")
	err := e.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := e.svc.WithDB(tx).Delete(ctx, kept.ID); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if _, err := e.svc.Get(ctx, kept.ID); err != nil {
		t.Fatalf("rolled back delete removed file: %v", err)
	}
	if _, err := e.local.Stat(ctx, kept.Key); err != nil {
		t.Fatalf("rolled back delete removed object: %v", err)
	}

	if err := e.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return e.svc.WithDB(tx).Delete(ctx, gone.ID)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Get(ctx, gone.ID); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("deleted file visible: %v", err)
	}
	if _, err := e.local.Stat(ctx, gone.Key); err != nil {
		t.Fatal("object should wait for cleanup after commit")
	}
	if n, err := e.svc.Cleanup(ctx, 10); err != nil || n != 1 {
		t.Fatalf("cleanup = %d %v", n, err)
	}
	if _, err := e.local.Stat(ctx, gone.Key); err == nil {
		t.Fatal("object kept after cleanup")
	}
}

func sendSigned(t *testing.T, req files.SignedRequest, body []byte) string {
	t.Helper()
	r, err := http.NewRequest(req.Method, req.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range req.Headers {
		r.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, msg)
	}
	return resp.Header.Get("ETag")
}

func TestS3DirectPut(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r, err := e.svc.Prepare(ctx, alice, files.FileInput{Disk: "s3", Filename: "a.txt", ContentType: "text/plain", Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	if r.URL != "https://cdn.example.com/"+r.Key {
		t.Fatalf("URL = %s", r.URL)
	}
	if _, err := e.svc.SignPut(ctx, bob, r.ID); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("other owner signed: %v", err)
	}
	req, err := e.svc.SignPut(ctx, alice, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	sendSigned(t, req, []byte("hello"))
	if _, err := e.svc.Confirm(ctx, alice, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SignPut(ctx, alice, r.ID); !errors.Is(err, files.ErrInvalid) {
		t.Fatalf("signing confirmed file = %v", err)
	}
}

func TestS3Multipart(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	data := bytes.Repeat([]byte("x"), 16<<20+100)
	r, err := e.svc.Prepare(ctx, alice, files.FileInput{Disk: "s3", Filename: "big.bin", Size: int64(len(data))})
	if err != nil {
		t.Fatal(err)
	}
	up, err := e.svc.CreateMultipart(ctx, alice, r.ID)
	if err != nil || up.PartSize != 16<<20 || up.PartCount != 2 {
		t.Fatalf("multipart = %+v %v", up, err)
	}
	if _, err := e.svc.SignPart(ctx, alice, r.ID, up.UploadID, 3); !errors.Is(err, files.ErrInvalid) {
		t.Fatalf("part 3 = %v", err)
	}
	var parts []files.Part
	for n := 1; n <= up.PartCount; n++ {
		req, err := e.svc.SignPart(ctx, alice, r.ID, up.UploadID, int32(n))
		if err != nil {
			t.Fatal(err)
		}
		chunk := data[int64(n-1)*up.PartSize : min(int64(n)*up.PartSize, int64(len(data)))]
		parts = append(parts, files.Part{Number: int32(n), ETag: sendSigned(t, req, chunk)})
	}
	if _, err := e.svc.CompleteMultipart(ctx, alice, r.ID, up.UploadID, parts[:1]); !errors.Is(err, files.ErrInvalid) {
		t.Fatalf("missing part = %v", err)
	}
	if _, err := e.svc.Confirm(ctx, alice, r.ID); !errors.Is(err, files.ErrNotUploaded) {
		t.Fatalf("confirm before complete = %v", err)
	}
	for range 2 { // 第二次模拟响应丢失后的重试
		if _, err := e.svc.CompleteMultipart(ctx, alice, r.ID, up.UploadID, []files.Part{parts[1], parts[0]}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.Confirm(ctx, alice, r.ID); err != nil {
		t.Fatal(err)
	}
	info, err := e.s3.Stat(ctx, r.Key)
	if err != nil || info.Size != int64(len(data)) {
		t.Fatalf("stat = %+v %v", info, err)
	}
}

func TestS3CleanupAbortsMultipart(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r, err := e.svc.Prepare(ctx, alice, files.FileInput{Disk: "s3", Filename: "big.bin", Size: 20 << 20})
	if err != nil {
		t.Fatal(err)
	}
	up, err := e.svc.CreateMultipart(ctx, alice, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.expire(t, r.ID)
	if n, err := e.svc.Cleanup(ctx, 10); err != nil || n != 1 {
		t.Fatalf("cleanup = %d %v", n, err)
	}
	if err := e.s3.CompleteMultipart(ctx, r.Key, up.UploadID, []files.Part{{Number: 1, ETag: "x"}}); err == nil {
		t.Fatal("session survived cleanup")
	}
	if n := e.rows(t); n != 0 {
		t.Fatalf("rows = %d", n)
	}
}
