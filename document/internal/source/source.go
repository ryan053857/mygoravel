// Package source 在原件上限内读取转换器的输入。
package source

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/go-tenon/tenon/document/convert"
)

// chunkSize 是两次检查取消之间读取的字节数。
const chunkSize = 64 << 10

// Read 读取 in.Reader 的全部内容，每次读取之间检查 ctx。maxBytes < 0 表示不限制；否则最多读取
// maxBytes+1 字节，原件超过 maxBytes 时返回 *convert.LimitError。
func Read(ctx context.Context, in convert.Input, maxBytes int64) ([]byte, error) {
	if in.Reader == nil {
		return nil, errors.New("convert: Input.Reader is nil")
	}
	var data []byte
	buf := make([]byte, chunkSize)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// 最多读到上限之后的一个字节；此处 len(data) <= maxBytes 成立。
		next := buf
		if maxBytes >= 0 {
			if remaining := maxBytes - int64(len(data)); remaining < int64(len(next))-1 {
				next = next[:remaining+1]
			}
		}
		n, err := in.Reader.Read(next)
		data = append(data, next[:n]...)
		if maxBytes >= 0 && int64(len(data)) > maxBytes {
			return nil, &convert.LimitError{Limit: convert.LimitSource, Max: maxBytes}
		}
		switch {
		case errors.Is(err, io.EOF):
			return data, nil
		case err != nil:
			if in.Name != "" {
				return nil, fmt.Errorf("convert: read %s: %w", in.Name, err)
			}
			return nil, fmt.Errorf("convert: read: %w", err)
		}
	}
}
