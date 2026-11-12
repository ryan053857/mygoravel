// Package redistest 为框架的测试连接 REDIS_TEST_URL 指向的 Redis。
package redistest

import (
	"os"
	"testing"

	"github.com/redis/rueidis"
)

// Open 返回连接测试 Redis 的客户端，测试结束时关闭。REDIS_TEST_URL 未设置时使测试失败。
// 测试应给自己的键加随机前缀，多个测试可以共用同一个 Redis。
func Open(t testing.TB) rueidis.Client {
	t.Helper()
	return OpenNamed(t, "")
}

// OpenNamed 与 Open 相同，但客户端的每条连接都以 name 作为 CLIENT SETNAME，
// 测试据此只断开自己的连接，不影响并行运行的其他测试。
func OpenNamed(t testing.TB, name string) rueidis.Client {
	t.Helper()
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Fatal("REDIS_TEST_URL is required for this test")
	}
	opt, err := rueidis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	opt.ClientName = name
	rdb, err := rueidis.NewClient(opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rdb.Close)
	return rdb
}
