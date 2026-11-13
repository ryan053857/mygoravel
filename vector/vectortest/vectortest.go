// Package vectortest 在测试中创建连接测试 Qdrant 的 vector.Store。
//
//	db := dbtest.Open(t, migrations.FS) // 包含已发布的 queue 和 vector 迁移
//	store := vectortest.New(t, db, vector.Model{Name: "test", Dims: 64, Embedder: aitest.NewEmbedder(64)})
//	err := article.Chunks.Refresh(ctx, store, article.LoadChunks, key)
package vectortest

import (
	"context"
	"crypto/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/qdrant/go-client/qdrant"
	"github.com/uptrace/bun"

	"github.com/go-tenon/tenon/vector"
)

// EnvAddr 是测试 Qdrant 的 gRPC 地址所在的环境变量，例如 127.0.0.1:6334。
const EnvAddr = "QDRANT_TEST_ADDR"

// New 返回连接测试 Qdrant 的 Store，环境变量未设置时使测试失败。
// 每次调用使用随机的 collection 前缀，并行的测试和反复运行的测试互不干扰，测试结束时删除这些 collection。
func New(t testing.TB, db *bun.DB, model vector.Model) *vector.Store {
	t.Helper()
	addr := os.Getenv(EnvAddr)
	if addr == "" {
		t.Fatalf("%s is required for this test", EnvAddr)
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("vectortest: %s: %v", EnvAddr, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("vectortest: %s: %v", EnvAddr, err)
	}
	client, err := qdrant.NewClient(&qdrant.Config{Host: host, Port: port})
	if err != nil {
		t.Fatalf("vectortest: %v", err)
	}
	prefix := "t" + strings.ToLower(rand.Text()[:10]) + "_"
	t.Cleanup(func() {
		defer func() { _ = client.Close() }()
		ctx := context.Background()
		names, err := client.ListCollections(ctx)
		if err != nil {
			t.Errorf("vectortest: list collections: %v", err)
			return
		}
		for _, name := range names {
			if strings.HasPrefix(name, prefix) {
				if err := client.DeleteCollection(ctx, name); err != nil {
					t.Errorf("vectortest: delete collection %s: %v", name, err)
				}
			}
		}
	})
	return vector.New(db, client, model, vector.WithPrefix(prefix))
}
