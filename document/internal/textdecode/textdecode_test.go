package textdecode

import (
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode/utf32"
)

func TestDecode(t *testing.T) {
	gb, _ := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte("中文"))
	le32, _ := utf32.UTF32(utf32.LittleEndian, utf32.UseBOM).NewEncoder().Bytes([]byte("中文😀"))
	be32, _ := utf32.UTF32(utf32.BigEndian, utf32.UseBOM).NewEncoder().Bytes([]byte("中文😀"))
	for name, tc := range map[string]struct {
		data     []byte
		declared string
		fallback bool
		want     string
	}{
		"declared utf-8 keeps text":       {[]byte("中文\xff"), "utf-8", true, "中文\uFFFD"},
		"replacement label is unknown":    {[]byte("plain ASCII"), "iso-2022-kr", false, "plain ASCII"},
		"replacement label uses fallback": {gb, "hz-gb-2312", true, "中文"},
		"quoted label":                    {gb, `"gbk"`, false, "中文"},
		"blank label":                     {gb, "  ", true, "中文"},
		"utf-32 le":                       {le32, "", false, "中文😀"},
		"utf-32 be":                       {be32, "", false, "中文😀"},
		"truncated gbk":                   {gb[:3], "gbk", false, "中\uFFFD"},
	} {
		var fallback = simplifiedchinese.GB18030
		got := ""
		if tc.fallback {
			got = Decode(tc.data, tc.declared, fallback)
		} else {
			got = Decode(tc.data, tc.declared, nil)
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	if got := Normalize("\uFEFFa\r\nb\rc\x00d"); got != "a\nb\ncd" {
		t.Errorf("got %q", got)
	}
}
