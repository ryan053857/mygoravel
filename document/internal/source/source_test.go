package source

import (
	"context"
	"errors"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/go-tenon/tenon/document/convert"
)

// countingReader 提供 size 字节的数据，并记录读取了多少。
type countingReader struct {
	size, read int
	failAfter  int // 读取量到达它时同时返回数据和错误；0 表示不出错
	cancel     func()
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.read >= r.size {
		return 0, io.EOF
	}
	n := min(len(p), r.size-r.read)
	r.read += n
	if r.cancel != nil {
		r.cancel()
	}
	if r.failAfter > 0 && r.read >= r.failAfter {
		return n, io.ErrUnexpectedEOF
	}
	return n, nil
}

func TestReadStopsAfterOneExtraByte(t *testing.T) {
	for _, max := range []int64{0, 1, 10, chunkSize - 1, chunkSize, chunkSize + 1, 3 * chunkSize} {
		r := &countingReader{size: 5 * chunkSize}
		_, err := Read(context.Background(), convert.Input{Reader: r}, max)
		var limit *convert.LimitError
		if !errors.As(err, &limit) || limit.Limit != convert.LimitSource || limit.Max != max {
			t.Fatalf("max %d: %v", max, err)
		}
		if int64(r.read) != max+1 {
			t.Errorf("max %d: read %d bytes, want %d", max, r.read, max+1)
		}
	}
}

func TestReadAtLimitAndUnlimited(t *testing.T) {
	r := &countingReader{size: 2*chunkSize + 7}
	data, err := Read(context.Background(), convert.Input{Reader: r}, int64(r.size))
	if err != nil || len(data) != r.size {
		t.Fatalf("at limit: %d bytes, %v", len(data), err)
	}
	for _, max := range []int64{-1, math.MaxInt64} {
		r = &countingReader{size: 3 * chunkSize}
		if data, err := Read(context.Background(), convert.Input{Reader: r}, max); err != nil || len(data) != r.size {
			t.Fatalf("max %d: %d bytes, %v", max, len(data), err)
		}
	}
}

func TestReadErrors(t *testing.T) {
	r := &countingReader{size: 10, failAfter: 10}
	if _, err := Read(context.Background(), convert.Input{Reader: r, Name: "x.txt"}, -1); !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), "x.txt") {
		t.Errorf("reader error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r = &countingReader{size: 3 * chunkSize, cancel: cancel}
	if _, err := Read(ctx, convert.Input{Reader: r}, -1); !errors.Is(err, context.Canceled) || r.read != chunkSize {
		t.Errorf("cancel between reads: %v after %d bytes", err, r.read)
	}
	r = &countingReader{size: 10, failAfter: 10}
	if _, err := Read(context.Background(), convert.Input{Reader: r}, -1); !errors.Is(err, io.ErrUnexpectedEOF) || !strings.HasPrefix(err.Error(), "convert: read: ") {
		t.Errorf("unnamed reader error: %v", err)
	}
	if _, err := Read(context.Background(), convert.Input{}, -1); err == nil {
		t.Error("nil reader accepted")
	}
}
