package split

import (
	"math"
	"runtime"
	"strings"
	"testing"
	"time"
)

// pathological 是没有 defuse 时让 goldmark 超线性的输入，参数是大致的字节数。
var pathological = map[string]func(size int) string{
	"nested list":   func(size int) string { return strings.Repeat("- ", size/2) + "x\n" },
	"nested quote":  func(size int) string { return strings.Repeat("> ", size/2) + "x\n" },
	"mixed nesting": func(size int) string { return strings.Repeat("> - 1. ", size/7) + "x\n" },
	"indentation": func(size int) string {
		var b strings.Builder
		for depth := 0; b.Len() < size; depth++ {
			b.WriteString(strings.Repeat("  ", depth) + "- a\n")
		}
		return b.String()
	},
	"link references":  func(size int) string { return strings.Repeat("[a]: b\n", size/7) },
	"long blank line":  func(size int) string { return strings.Repeat(" ", size) + "\n# H\nx" },
	"long indentation": func(size int) string { return strings.Repeat(" ", size) + "x\n" },
	"long empty item":  func(size int) string { return "-" + strings.Repeat(" ", size) + "\n" },
	"backticks": func(size int) string {
		var b strings.Builder
		for k := 1; b.Len() < size; k++ {
			b.WriteString(strings.Repeat("`", k) + "a ")
		}
		return b.String()
	},
	"padded table": func(size int) string {
		columns := size / 6
		return strings.Repeat("|a", columns) + "|\n" + strings.Repeat("|-", columns) + "|\n" + strings.Repeat("x\n", columns)
	},
	"padded table in quote": func(size int) string {
		columns := size / 8
		return "> " + strings.Repeat("|a", columns) + "|\n> " + strings.Repeat("|-", columns) + "|\n" + strings.Repeat("> x\n", columns)
	},
}

// cost 返回扫描 text 每字节的耗时（三次中最少的）和内存分配。
func cost(text string) (perByte time.Duration, allocated float64) {
	perByte = time.Duration(math.MaxInt64)
	for range 3 {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		start := time.Now()
		scan(text)
		elapsed := time.Since(start)
		runtime.ReadMemStats(&after)
		perByte = min(perByte, elapsed/time.Duration(len(text)))
		allocated = float64(after.TotalAlloc-before.TotalAlloc) / float64(len(text))
	}
	return max(perByte, 1), allocated
}

// TestPathologicalInputsStayLinear 确保已知的病态输入在扩大 16 倍时，每字节的耗时和内存分配基本不变；
// n^1.5 的增长会让它们变为 4 倍，平方级的增长变为 16 倍。
func TestPathologicalInputsStayLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("timing")
	}
	for name, generate := range pathological {
		smallTime, smallAlloc := cost(generate(64 << 10))
		largeTime, largeAlloc := cost(generate(1 << 20))
		if ratio := float64(largeTime) / float64(smallTime); ratio > 2.5 {
			t.Errorf("%s: time per byte grew %.1fx", name, ratio)
		}
		if ratio := largeAlloc / smallAlloc; ratio > 2 || largeAlloc > 512 {
			t.Errorf("%s: allocation per byte %.0f, grew %.1fx", name, largeAlloc, ratio)
		}
	}
}

func TestDefuseKeepsLength(t *testing.T) {
	for name, generate := range pathological {
		text := generate(4 << 10)
		source := []byte(text)
		defuse(source)
		if len(source) != len(text) || strings.Count(string(source), "\n") != strings.Count(text, "\n") {
			t.Errorf("%s: defuse changed the length or the lines", name)
		}
	}
}

func TestDefuseLeavesOrdinaryMarkdown(t *testing.T) {
	for _, text := range []string{
		"# 标题\n\n> 引用\n> - 列表\n>   1. 编号\n\n" + strings.Repeat("- - - ", 40) + "\n",
		"| a | b |\n| - | - |\n| 1 |\n| 1 | 2 |\n",
		strings.Repeat("[ref]: /url\n", maxReferences),
		"```\n" + strings.Repeat(" ", 200) + "\n" + strings.Repeat(" ", 100) + "code\n```\n",
	} {
		source := []byte(text)
		if defuse(source); string(source) != text {
			t.Errorf("defuse changed %q to %q", text, source)
		}
	}
}
