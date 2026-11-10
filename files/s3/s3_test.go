package s3_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/ryan053857/mygoravel/files"
	s3disk "github.com/ryan053857/mygoravel/files/s3"
)

// newFakeDisk 通过真实 SDK 连接内存 S3 服务。
func newFakeDisk(t *testing.T) *s3disk.Disk {
	t.Helper()
	server := httptest.NewServer(gofakes3.New(s3mem.New()).Server())
	t.Cleanup(server.Close)
	client := awss3.New(awss3.Options{
		BaseEndpoint: aws.String(server.URL), Region: "us-east-1", UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider("k", "s", ""), RetryMaxAttempts: 1,
	})
	if _, err := client.CreateBucket(context.Background(), &awss3.CreateBucketInput{Bucket: aws.String("test")}); err != nil {
		t.Fatal(err)
	}
	disk, err := s3disk.Open(context.Background(), s3disk.Config{
		Bucket: "test", Region: "us-east-1", Endpoint: server.URL, PathStyle: true,
		AccessKey: "k", SecretKey: "s", PublicURL: "https://cdn.example.com/",
	})
	if err != nil {
		t.Fatal(err)
	}
	return disk
}

func read(t *testing.T, disk *s3disk.Disk, key string) []byte {
	t.Helper()
	body, err := disk.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestConfigValidate(t *testing.T) {
	ok := s3disk.Config{Bucket: "b", PublicURL: "https://cdn.example.com"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []s3disk.Config{
		{PublicURL: "https://cdn.example.com"},
		{Bucket: "b"},
		{Bucket: "b", PublicURL: "/storage"},
		{Bucket: "b", PublicURL: "https://x", Endpoint: "minio:9000"},
		{Bucket: "b", PublicURL: "https://x", AccessKey: "k"},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestPutSmallAndLarge(t *testing.T) {
	ctx := context.Background()
	disk := newFakeDisk(t)
	if err := disk.Put(ctx, "a/small.txt", "text/plain", strings.NewReader("hello"), 5); err != nil {
		t.Fatal(err)
	}
	if got := read(t, disk, "a/small.txt"); string(got) != "hello" {
		t.Fatalf("small = %q", got)
	}
	// 超过一片缓冲，走分片上传；用 HalfReader 模拟不可 seek 的慢速正文。
	large := bytes.Repeat([]byte("0123456789abcdef"), (40<<20)/16+3)
	if err := disk.Put(ctx, "a/large.bin", "", iotest.HalfReader(bytes.NewReader(large)), int64(len(large))); err != nil {
		t.Fatal(err)
	}
	if got := read(t, disk, "a/large.bin"); !bytes.Equal(got, large) {
		t.Fatalf("large mismatch: %d bytes", len(got))
	}
	if info, err := disk.Stat(ctx, "a/large.bin"); err != nil || info.Size != int64(len(large)) {
		t.Fatalf("stat = %+v %v", info, err)
	}
	if err := disk.Put(ctx, "a/broken.bin", "", io.MultiReader(bytes.NewReader(large[:20<<20]), iotest.ErrReader(errors.New("boom"))), 40<<20); err == nil {
		t.Fatal("broken body accepted")
	}
	if _, err := disk.Stat(ctx, "a/broken.bin"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("broken object published: %v", err)
	}
}

func TestStatDeleteURL(t *testing.T) {
	ctx := context.Background()
	disk := newFakeDisk(t)
	if _, err := disk.Stat(ctx, "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat missing = %v", err)
	}
	if err := disk.Delete(ctx, "missing"); err != nil {
		t.Fatal(err)
	}
	if got := disk.URL("2026/11/a b.png"); got != "https://cdn.example.com/2026/11/a%20b.png" {
		t.Fatalf("URL = %s", got)
	}
}

func TestDirectUpload(t *testing.T) {
	ctx := context.Background()
	disk := newFakeDisk(t)
	send := func(req files.SignedRequest, body []byte) string {
		t.Helper()
		if req.Headers.Get("Host") != "" || req.Headers.Get("Content-Length") != "" {
			t.Fatalf("restricted headers returned: %v", req.Headers)
		}
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
			t.Fatalf("upload status %d: %s", resp.StatusCode, msg)
		}
		return resp.Header.Get("ETag")
	}

	req, err := disk.SignPut(ctx, "put.txt", "text/plain", 3, 60e9)
	if err != nil {
		t.Fatal(err)
	}
	send(req, []byte("abc"))
	if got := read(t, disk, "put.txt"); string(got) != "abc" {
		t.Fatalf("put = %q", got)
	}

	id, err := disk.CreateMultipart(ctx, "multi.bin", "")
	if err != nil {
		t.Fatal(err)
	}
	first, second := bytes.Repeat([]byte{'a'}, 5<<20), []byte("tail")
	var parts []files.Part
	for i, body := range [][]byte{first, second} {
		req, err := disk.SignPart(ctx, "multi.bin", id, int32(i+1), int64(len(body)), 60e9)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, files.Part{Number: int32(i + 1), ETag: send(req, body)})
	}
	if err := disk.CompleteMultipart(ctx, "multi.bin", id, parts); err != nil {
		t.Fatal(err)
	}
	if got := read(t, disk, "multi.bin"); !bytes.Equal(got, append(first, second...)) {
		t.Fatal("multipart content mismatch")
	}

	stale, err := disk.CreateMultipart(ctx, "stale.bin", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := disk.AbortAllMultipart(ctx, "stale.bin"); err != nil {
		t.Fatal(err)
	}
	if err := disk.CompleteMultipart(ctx, "stale.bin", stale, parts); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("aborted session still usable: %v", err)
	}
	if err := disk.AbortMultipart(ctx, "stale.bin", stale); err != nil {
		t.Fatalf("abort is not idempotent: %v", err)
	}
}
