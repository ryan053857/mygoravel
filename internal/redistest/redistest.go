// Package redistest 为框架的测试连接测试用的 Redis：单机的 REDIS_TEST_URL 和集群的 REDIS_CLUSTER_TEST_URL。
//
// 组件的契约测试对两种 Redis 各运行一次：
//
//	for _, srv := range redistest.Servers {
//		t.Run(srv.Name, func(t *testing.T) { testStore(t, redislock.New(srv.Open(t))) })
//	}
//
// 测试应给自己的键加随机前缀，多个测试可以共用同一个 Redis。
package redistest

import (
	"os"
	"testing"

	"github.com/redis/rueidis"
)

// Server 是一种测试用的 Redis 部署，地址来自环境变量。
type Server struct {
	Name string
	env  string
}

var (
	// Standalone 是单机 Redis，地址来自 REDIS_TEST_URL。
	Standalone = Server{Name: "standalone", env: "REDIS_TEST_URL"}
	// Cluster 是 Redis 集群，地址来自 REDIS_CLUSTER_TEST_URL，由同一目录下的 cluster.sh 启动。
	Cluster = Server{Name: "cluster", env: "REDIS_CLUSTER_TEST_URL"}
	// Servers 是组件需要支持的全部部署方式。
	Servers = []Server{Standalone, Cluster}
)

// Open 返回连接 s 的客户端，测试结束时关闭。环境变量未设置时使测试失败。
func (s Server) Open(t testing.TB) rueidis.Client {
	t.Helper()
	return s.OpenNamed(t, "")
}

// OpenNamed 与 Open 相同，但客户端的每条连接都以 name 作为 CLIENT SETNAME，
// 测试据此只断开自己的连接，不影响并行运行的其他测试。
func (s Server) OpenNamed(t testing.TB, name string) rueidis.Client {
	t.Helper()
	url := os.Getenv(s.env)
	if url == "" {
		t.Fatal(s.env + " is required for this test")
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
