// Package textdecode 把原文解码为 UTF-8，并为 Markdown 输出做规范化。
package textdecode

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/encoding/unicode/utf32"
	"golang.org/x/text/transform"
)

// Decode 把 data 解码为 UTF-8。优先使用 BOM（UTF-8、UTF-16 或 UTF-32），其次是声明的已知字符集，
// 最后是 UTF-8。没有声明编码、又不是合法 UTF-8 的输入，设置了 fallback 时用它解码。
// 无法解码的字节变为 U+FFFD。
func Decode(data []byte, declared string, fallback encoding.Encoding) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return strings.ToValidUTF8(string(data[3:]), "\uFFFD")
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE, 0x00, 0x00}), bytes.HasPrefix(data, []byte{0x00, 0x00, 0xFE, 0xFF}):
		return decodeWith(utf32.UTF32(utf32.BigEndian, utf32.ExpectBOM), data)
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}), bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return decodeWith(unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM), data)
	}
	if e := Lookup(declared); e != nil {
		if e == unicode.UTF8 {
			return strings.ToValidUTF8(string(data), "\uFFFD")
		}
		return decodeWith(e, data)
	}
	if utf8.Valid(data) {
		return string(data)
	}
	if fallback != nil {
		return decodeWith(fallback, data)
	}
	return strings.ToValidUTF8(string(data), "\uFFFD")
}

// Lookup 返回字符集标签对应的编码，忽略两侧的引号；标签为空、未知或映射到 replacement 编码时返回 nil。
func Lookup(label string) encoding.Encoding {
	label = strings.Trim(strings.TrimSpace(label), `"'`)
	if label == "" {
		return nil
	}
	e, err := htmlindex.Get(label)
	if err != nil || e == encoding.Replacement {
		return nil
	}
	return e
}

// decodeWith 用 e 解码 data，无法解码的字节变为 U+FFFD。
func decodeWith(e encoding.Encoding, data []byte) string {
	decoded, _, _ := transform.Bytes(e.NewDecoder(), data)
	return strings.ToValidUTF8(string(decoded), "\uFFFD")
}

// Normalize 把 CRLF 和单独的 CR 转为 LF，并去掉开头的 BOM 和 NUL 字节。
func Normalize(s string) string {
	s = strings.TrimPrefix(s, "\uFEFF")
	if strings.ContainsAny(s, "\r\x00") {
		s = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\x00", "").Replace(s)
	}
	return s
}
