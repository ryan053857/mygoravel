package ui

import (
	"context"

	"github.com/go-tenon/tenon/i18n"
)

// text 按请求的语言翻译框架内置的文案 id；context 中没有语言（没有经过 httpx.Locale）时返回英文的 fallback。
func text(ctx context.Context, id, fallback string) string {
	if s := i18n.T(ctx, id, nil); s != id {
		return s
	}
	return fallback
}

// lang 返回 <html lang>：PageProps.Lang，其次是请求的语言，都没有时为 zh-CN（i18n 的默认语言）。
func lang(ctx context.Context, p PageProps) string {
	if p.Lang != "" {
		return p.Lang
	}
	return orDefault(i18n.Locale(ctx), "zh-CN")
}
