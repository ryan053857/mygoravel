package xlsx

import (
	"archive/zip"
	"errors"
	"math"
	"testing"

	"github.com/ryan053857/mygoravel/document/convert"
)

func TestCheckExpandedSaturates(t *testing.T) {
	entry := func(size uint64) *zip.File { return &zip.File{FileHeader: zip.FileHeader{UncompressedSize64: size}} }
	for name, tc := range map[string]struct {
		entries []*zip.File
		max     int64
		over    bool
	}{
		"wrapping sum": {[]*zip.File{entry(16 << 10), entry(math.MaxUint64 - 10000)}, 128 << 20, true},
		"at limit":     {[]*zip.File{entry(60), entry(40)}, 100, false},
		"one over":     {[]*zip.File{entry(60), entry(41)}, 100, true},
		"unlimited":    {[]*zip.File{entry(math.MaxUint64)}, -1, false},
	} {
		err := checkExpanded(tc.entries, tc.max)
		if got := errors.Is(err, convert.ErrTooLarge); got != tc.over {
			t.Errorf("%s: %v", name, err)
		}
	}
}
