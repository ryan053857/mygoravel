package docs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSnippet(t *testing.T) {
	render := func(fs []fragment) string {
		var b strings.Builder
		for _, f := range fs {
			if f.mark {
				b.WriteString("[" + f.text + "]")
			} else {
				b.WriteString(f.text)
			}
		}
		return b.String()
	}
	long := strings.Repeat("前", 50) + "Webhook 签名" + strings.Repeat("后", 100)
	e := entry{text: long, lowerText: strings.ToLower(long)}
	got := render(snippet(e, []string{"webhook", "签名"}))
	assert.Equal(t, "…"+strings.Repeat("前", 40)+"[Webhook] [签名]"+strings.Repeat("后", 70)+"…", got)

	short := entry{text: "没有匹配的正文", lowerText: "没有匹配的正文"}
	assert.Equal(t, "没有匹配的正文", render(snippet(short, []string{"标题"})))
}
