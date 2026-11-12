package schedule

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 截断错误信息时不切断字符。
func TestTruncate(t *testing.T) {
	s := strings.Repeat("a", maxErrorLength-1) + "超时"
	got := truncate(s)
	if !utf8.ValidString(got) || len(got) != maxErrorLength-1 {
		t.Fatalf("truncated to %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
	if truncate("短") != "短" {
		t.Fatal("short string changed")
	}
}
