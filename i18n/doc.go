// Package i18n 组合 go-i18n 消息目录，并在 context 中携带请求的语言。
//
// New 加载框架内置消息与应用的翻译文件，返回原生 Bundle；后加载的同名消息覆盖先加载的。
// httpx.Locale 中间件按 Accept-Language 协商语言并调用 WithLocale，
// 之后通过 T、Localizer 与 Locale 在请求范围内翻译。
package i18n
