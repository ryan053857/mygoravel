// Package ooxmltest 为测试构造 OOXML 包。
package ooxmltest

import (
	"archive/zip"
	"bytes"
	"testing"
)

// Build 把以部件名为键的 files 打包为一个 zip。
func Build(t testing.TB, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Encrypted 返回一个含有 EncryptionInfo 流的复合文档二进制格式的开头。
func Encrypted() []byte {
	data := []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}
	data = append(data, make([]byte, 64)...)
	return append(data, []byte("E\x00n\x00c\x00r\x00y\x00p\x00t\x00i\x00o\x00n\x00I\x00n\x00f\x00o\x00")...)
}

// Legacy 返回一个未加密的复合文档二进制格式的开头，与旧的 .doc 文件一样。
func Legacy() []byte {
	return append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 64)...)
}
