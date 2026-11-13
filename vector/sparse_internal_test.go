package vector

import (
	"slices"
	"testing"

	"github.com/go-tenon/tenon/search"
)

func TestDocumentSparse(t *testing.T) {
	t.Parallel()
	s := documentSparse(search.Bigram, "保修 保修 保修 期限")
	if !slices.IsSorted(s.indices) || len(s.indices) != 2 || len(s.values) != 2 {
		t.Fatalf("sparse = %+v", s)
	}
	repeated, once := s.values[slices.Index(s.indices, termID("保修"))], s.values[slices.Index(s.indices, termID("期限"))]
	// 词频越高权重越大，但不超过 k1+1
	if repeated <= once || repeated >= bm25K1+1 {
		t.Fatalf("weights: repeated %v, once %v", repeated, once)
	}
	// 同样的词在更长的文本中权重更低
	long := documentSparse(search.Bigram, "期限 一二三四五六七八九十甲乙丙丁戊己庚辛壬癸")
	if w := long.values[slices.Index(long.indices, termID("期限"))]; w >= once {
		t.Fatalf("long document weight %v, short %v", w, once)
	}
	if e := documentSparse(search.Bigram, "！？"); len(e.indices) != 0 {
		t.Fatalf("punctuation produced %+v", e)
	}
}

func TestQuerySparse(t *testing.T) {
	t.Parallel()
	q := querySparse(search.Bigram, "保修 保修 X-2000")
	if len(q.indices) != 3 || !slices.IsSorted(q.indices) {
		t.Fatalf("query sparse = %+v", q)
	}
	for _, v := range q.values {
		if v != 1 {
			t.Fatalf("query weights = %v", q.values)
		}
	}
}
