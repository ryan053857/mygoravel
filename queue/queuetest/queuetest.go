// Package queuetest 在测试中运行连接测试 nsqd 的 queue.Worker。
//
//	db := dbtest.Open(t, migrations.FS) // 包含已发布的 queue 迁移
//	w := queue.NewWorker(db, queuetest.Config(t), nil)
//	article.PublishJob.Handle(w, publish)
//	queuetest.Run(t, w)
package queuetest

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/queue"
)

// 测试 nsqd 的地址所在的环境变量：EnvAddr 是 TCP 地址，例如 127.0.0.1:4150；EnvHTTP 是 HTTP 地址，
// 例如 http://127.0.0.1:4151，测试结束时用它删除测试建的 topic。
const (
	EnvAddr = "NSQD_TEST_ADDR"
	EnvHTTP = "NSQD_TEST_HTTP"
)

// Config 返回连接测试 nsqd 的 Worker 配置，环境变量未设置时使测试失败。
// 每次调用使用随机的 topic 前缀，并行的测试和反复运行的测试互不干扰，测试结束时删除这些 topic；
// 租约和停止等待较短，任务很快重试，测试结束时很快停下。
func Config(t testing.TB) queue.Config {
	t.Helper()
	addr, api := os.Getenv(EnvAddr), os.Getenv(EnvHTTP)
	if addr == "" || api == "" {
		t.Fatalf("%s and %s are required for this test", EnvAddr, EnvHTTP)
	}
	cfg := queue.DefaultConfig()
	cfg.NSQD = addr
	cfg.Prefix = "t" + strings.ToLower(rand.Text()[:10]) + "."
	cfg.Lease = 5 * time.Second
	cfg.ShutdownTimeout = time.Second
	// nsqd 定期随机抽查一部分 channel 的延迟消息，channel 越多，延迟和重试越不准时：测试建的 topic 用完即删。
	t.Cleanup(func() { deleteTopics(t, api, cfg.Prefix) })
	return cfg
}

// Run 在后台运行 w，测试结束时停止它并等待 Run 返回。Run 返回错误时使测试失败。
func Run(t testing.TB, w *queue.Worker) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("queuetest: worker: %v", err)
		}
	})
}

// deleteTopics 删除 nsqd 中以 prefix 开头的 topic。
func deleteTopics(t testing.TB, api, prefix string) {
	resp, err := http.Get(api + "/stats?format=json")
	if err != nil {
		t.Errorf("queuetest: nsqd stats: %v", err)
		return
	}
	defer resp.Body.Close()
	var stats struct {
		Topics []struct {
			Name string `json:"topic_name"`
		} `json:"topics"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Errorf("queuetest: nsqd stats: %v", err)
		return
	}
	for _, topic := range stats.Topics {
		if !strings.HasPrefix(topic.Name, prefix) {
			continue
		}
		resp, err := http.Post(api+"/topic/delete?topic="+url.QueryEscape(topic.Name), "", nil)
		if err != nil {
			t.Errorf("queuetest: delete topic %s: %v", topic.Name, err)
			continue
		}
		_ = resp.Body.Close()
	}
}
