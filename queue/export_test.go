package queue

import (
	"testing"
	"time"
)

// SetRelayRetry 在测试期间缩短待命的 Worker 重试复制槽的间隔。
func SetRelayRetry(t testing.TB, d time.Duration) {
	old := relayRetry
	relayRetry = d
	t.Cleanup(func() { relayRetry = old })
}
