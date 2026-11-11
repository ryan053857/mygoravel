package mdwrite

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"unsafe"

	"github.com/ryan053857/mygoravel/document/convert"
)

func TestTableSizeMatchesRendering(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		table := NewTable(-1, random.IntN(2) == 0)
		if random.IntN(3) == 0 {
			header := make([]string, random.IntN(4))
			_ = table.SetHeader(header)
		}
		for range random.IntN(6) {
			row := make([]string, random.IntN(5))
			for i := range row {
				row[i] = strings.Repeat("x|\\", random.IntN(3))
			}
			_ = table.Add(row)
		}
		if got, want := table.size(), int64(len(table.Markdown())); got != want {
			t.Fatalf("size %d, rendered %d:\n%s", got, want, table.Markdown())
		}
	}
}

func TestTableLimit(t *testing.T) {
	exact := NewTable(19, true)
	if err := errors.Join(exact.Add([]string{"A"}), exact.Add([]string{"B"})); err != nil || exact.Markdown() != "| A |\n| --- |\n| B |" {
		t.Fatalf("exact limit: %q, %v", exact.Markdown(), err)
	}
	over := NewTable(18, true)
	if err := errors.Join(over.Add([]string{"A"}), over.Add([]string{"B"})); !errors.Is(err, convert.ErrTooLarge) {
		t.Errorf("one byte over: %v", err)
	}
	blank := NewTable(1, true)
	for range 100 {
		if err := blank.Add([]string{"", " "}); err != nil {
			t.Fatalf("blank rows counted: %v", err)
		}
	}
	if blank.Markdown() != "" {
		t.Errorf("blank table = %q", blank.Markdown())
	}
}

func TestTableWithin(t *testing.T) {
	table := NewTableWithin(18, 100, true)
	err := errors.Join(table.Add([]string{"A"}), table.Add([]string{"B"}))
	var limit *convert.LimitError
	if !errors.As(err, &limit) || limit.Max != 100 {
		t.Errorf("err = %v, want a limit of 100", err)
	}
	// 重复的单元格共用转义结果。
	cell := strings.Repeat("|", 1000)
	shared := NewTable(-1, true)
	_ = shared.Add([]string{cell, cell})
	if a, b := shared.header[0], shared.header[1]; a != Cell(cell) || unsafe.StringData(a) != unsafe.StringData(b) {
		t.Error("repeated cell escaped twice")
	}
}

func TestTableHeaders(t *testing.T) {
	empty := NewTable(-1, true)
	_ = empty.SetHeader([]string{"", "2025"})
	_ = empty.Add([]string{"Jan", "10"})
	if want := "|  | 2025 |\n| --- | --- |\n| Jan | 10 |"; empty.Markdown() != want {
		t.Errorf("explicit header = %q", empty.Markdown())
	}
	twice := NewTable(-1, true)
	_ = twice.SetHeader([]string{"long header", "x"})
	_ = twice.SetHeader([]string{"a"})
	_ = twice.Add([]string{"1"})
	if twice.size() != int64(len(twice.Markdown())) {
		t.Errorf("SetHeader twice: size %d, rendered %d", twice.size(), len(twice.Markdown()))
	}
	none := NewTable(-1, false)
	_ = none.Add([]string{"1", "2"})
	if want := "|  |  |\n| --- | --- |\n| 1 | 2 |"; none.Markdown() != want {
		t.Errorf("no header = %q", none.Markdown())
	}
}

func TestEscaping(t *testing.T) {
	if got := Cell(" a\\|b \n c "); got != `a\\\|b c` {
		t.Errorf("Cell = %q", got)
	}
	if got := Paragraph("intro\r# injected\r\n  - item\n1. one\n10) ten"); got != "intro\n\\# injected\n\\- item\n1\\. one\n10\\) ten" {
		t.Errorf("Paragraph = %q", got)
	}
}
