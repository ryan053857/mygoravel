package page_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/go-tenon/tenon/errorx"
	"github.com/go-tenon/tenon/page"
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

func TestOffsetLinks(t *testing.T) {
	cases := []struct {
		page  int
		total int64
		want  []int
	}{
		{1, 0, []int{1}},
		{1, 10, []int{1}},
		{1, 20, []int{1, 2}},
		{1, 60, []int{1, 2, 0, 6}},
		{10, 200, []int{1, 0, 9, 10, 11, 0, 20}},
		{3, 200, []int{1, 2, 3, 4, 0, 20}},
		{4, 200, []int{1, 2, 3, 4, 5, 0, 20}},
		{5, 200, []int{1, 0, 4, 5, 6, 0, 20}},
		{17, 200, []int{1, 0, 16, 17, 18, 19, 20}},
		{20, 200, []int{1, 0, 19, 20}},
	}
	for _, c := range cases {
		o := page.NewOffset([]int{}, page.OffsetParams{Page: c.page, PerPage: 10}, c.total)
		if got := o.Links(1); !slices.Equal(got, c.want) {
			t.Errorf("page %d of %d: %v, want %v", c.page, o.LastPage(), got, c.want)
		}
	}
	if last := page.NewOffset([]int{}, page.OffsetParams{Page: 1, PerPage: 20}, 41).LastPage(); last != 3 {
		t.Errorf("LastPage = %d", last)
	}
}
