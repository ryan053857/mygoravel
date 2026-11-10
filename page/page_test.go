package page_test

import (
	"errors"
	"testing"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/page"
)

func TestOffsetParams(t *testing.T) {
	p := page.OffsetParams{Page: 3, PerPage: 20}
	if p.Limit() != 20 || p.Offset() != 40 {
		t.Fatalf("limit=%d offset=%d", p.Limit(), p.Offset())
	}
	got := page.NewOffset[int](nil, p, 41)
	if got.Items == nil || got.Page != 3 || got.PerPage != 20 || got.Total != 41 {
		t.Fatalf("%+v", got)
	}
}

type key struct {
	ID int `json:"id"`
}

func TestCursorRoundTrip(t *testing.T) {
	first := page.CursorParams{PerPage: 2}
	if first.Limit() != 3 {
		t.Fatalf("limit=%d", first.Limit())
	}
	var after key
	if ok, err := first.After(&after); ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}

	got := page.NewCursor([]int{1, 2, 3}, first, func(n int) any { return key{n} })
	if len(got.Items) != 2 || got.NextCursor == "" {
		t.Fatalf("%+v", got)
	}

	next := page.CursorParams{Cursor: got.NextCursor, PerPage: 2}
	if ok, err := next.After(&after); !ok || err != nil || after.ID != 2 {
		t.Fatalf("ok=%v err=%v after=%+v", ok, err, after)
	}

	last := page.NewCursor([]int{3}, next, func(n int) any { return key{n} })
	if len(last.Items) != 1 || last.NextCursor != "" {
		t.Fatalf("%+v", last)
	}
	if empty := page.NewCursor[int](nil, next, nil); empty.Items == nil {
		t.Fatal("nil items should become empty slice")
	}
}

func TestInvalidCursor(t *testing.T) {
	for _, c := range []string{"!!!", "bm90LWpzb24"} {
		var after key
		_, err := page.CursorParams{Cursor: c}.After(&after)
		if !errors.Is(err, page.ErrInvalidCursor) || errorx.KindOf(err) != errorx.InvalidArgument {
			t.Fatalf("%q: %v", c, err)
		}
	}
}
