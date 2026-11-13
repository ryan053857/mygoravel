package mdwrite

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-tenon/tenon/document/convert"
)

func TestWriterLimit(t *testing.T) {
	w := New(5)
	w.WriteString("abc")
	w.WriteString("de")
	if w.Err() != nil || w.String() != "abcde" || w.Len() != 5 {
		t.Fatalf("at limit: %q, %v", w.String(), w.Err())
	}
	w.WriteString("f")
	w.WriteString("")
	var limit *convert.LimitError
	if !errors.As(w.Err(), &limit) || limit.Limit != convert.LimitOutput || limit.Max != 5 || w.String() != "abcde" {
		t.Errorf("over limit: %q, %v", w.String(), w.Err())
	}
	unlimited := New(-1)
	unlimited.WriteString(strings.Repeat("x", 1000))
	if unlimited.Err() != nil {
		t.Error(unlimited.Err())
	}
}

func TestFence(t *testing.T) {
	for _, tc := range []struct{ content, want string }{
		{"a", "```x\na\n```"},
		{"a\n", "```x\na\n```"},
		{"``` and ````", "`````x\n``` and ````\n`````"},
	} {
		w := New(-1)
		w.Fence("x", tc.content)
		if w.String() != tc.want {
			t.Errorf("Fence(%q) = %q, want %q", tc.content, w.String(), tc.want)
		}
	}
	exact := New(int64(len("```x\na\n```")))
	exact.Fence("x", "a")
	if exact.Err() != nil {
		t.Errorf("exact limit: %v", exact.Err())
	}
	small := New(16)
	small.Fence("json", strings.Repeat("`", 1<<20))
	if !errors.Is(small.Err(), convert.ErrTooLarge) || small.Len() != 0 {
		t.Errorf("over limit wrote %d bytes, %v", small.Len(), small.Err())
	}
}
