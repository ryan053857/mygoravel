// Package pdftest 为测试构造小型 PDF 文档。
package pdftest

import (
	"bytes"
	"crypto/md5"
	"crypto/rc4"
	"fmt"
	"strings"
	"testing"
)

// Text 是在 (X, Y) 处以字号 Size 绘制的一行。S 写作 PDF 字符串字面量的内容，所以 "("、")" 和 "\\"
// 需要转义，"\\n" 这类转义由阅读器解释。X 为 0 表示左边距 72。
type Text struct {
	Size float64
	Y    float64
	S    string
	X    float64
}

// L 返回在左边距、y 处以字号 size 绘制的一行文字 s。
func L(size, y float64, s string) Text {
	return Text{Size: size, Y: y, S: s}
}

// LX 返回在 (x, y) 处以字号 size 绘制的一行文字 s。
func LX(size, x, y float64, s string) Text {
	return Text{Size: size, Y: y, S: s, X: x}
}

// Build 用 Helvetica 写出一个 PDF，每一项一页。userPassword 非空时加入需要它的 RC4 标准安全处理器。
func Build(t testing.TB, pages [][]Text, userPassword string) []byte {
	t.Helper()
	return buildPDF(pages, userPassword, false)
}

// BuildOwnerOnly 写出一个打开时不需要密码、但带有所有者密码的 PDF，内容流用 RC4 加密。
func BuildOwnerOnly(pages [][]Text) []byte {
	return buildPDF(pages, "", true)
}

// buildPDF 写出 Build 描述的 PDF。encryptStreams 时即使没有用户密码也带安全处理器，
// 内容流用各自对象的密钥做 RC4 加密，与只有所有者密码的文档一样。
func buildPDF(pages [][]Text, userPassword string, encryptStreams bool) []byte {
	id := "0123456789abcdef"
	encrypted := userPassword != "" || encryptStreams
	o, u, fileKey := securityValues(userPassword, "owner", id, -4)
	var objects []string
	add := func(body string) int {
		objects = append(objects, body)
		return len(objects)
	}
	catalog := add("") // pages 对象建好后填写
	pagesID := add("")
	font := add("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	var kids []string
	for _, lines := range pages {
		var content strings.Builder
		for _, l := range lines {
			x := l.X
			if x == 0 {
				x = 72
			}
			fmt.Fprintf(&content, "BT /F1 %g Tf %g %g Td (%s) Tj ET\n", l.Size, x, l.Y, l.S)
		}
		body := content.String()
		if encryptStreams {
			number := len(objects) + 1
			objectKey := md5.Sum(append(append([]byte{}, fileKey...), byte(number), byte(number>>8), byte(number>>16), 0, 0))
			body = string(rc4Crypt(objectKey[:10], []byte(body)))
		}
		stream := add(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(body), body))
		page := add(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", pagesID, font, stream))
		kids = append(kids, fmt.Sprintf("%d 0 R", page))
	}
	objects[catalog-1] = fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesID)
	objects[pagesID-1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(kids))
	trailer := fmt.Sprintf("/Root %d 0 R", catalog)
	if encrypted {
		encrypt := add(fmt.Sprintf("<< /Filter /Standard /V 1 /R 2 /O <%x> /U <%x> /P -4 >>", o, u))
		trailer += fmt.Sprintf(" /Encrypt %d 0 R /ID [<%x> <%x>]", encrypt, id, id)
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, body := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d %s >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, trailer, xref)
	return out.Bytes()
}

// padding 是标准安全处理器的密码填充。
var padding = []byte{0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A}

// rc4Crypt 用 key 加密或解密 data。
func rc4Crypt(key, data []byte) []byte {
	cipher, _ := rc4.NewCipher(key)
	out := make([]byte, len(data))
	cipher.XORKeyStream(out, data)
	return out
}

// securityValues 为第 2 版 RC4 加密计算 /O 和 /U 项以及文件密钥。
func securityValues(user, owner, id string, permissions int32) ([]byte, []byte, []byte) {
	pad := func(password string) []byte {
		return append([]byte(password), padding...)[:32]
	}
	ownerKey := md5.Sum(pad(owner))
	o := rc4Crypt(ownerKey[:5], pad(user))
	p := uint32(permissions)
	seed := append(append(pad(user), o...), byte(p), byte(p>>8), byte(p>>16), byte(p>>24))
	key := md5.Sum(append(seed, id...))
	return o, rc4Crypt(key[:5], padding), key[:5]
}
