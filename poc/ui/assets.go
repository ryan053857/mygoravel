// Package ui 是 shadcn-templ + Tailwind 的 PoC，见 README.md。
package ui

import (
	"embed"
	"io/fs"
)

//go:embed assets/css/app.css assets/js/*.js
var embedded embed.FS

// Assets 是打进二进制的静态资源：Tailwind 的输出和 shadcn-templ 的组件脚本。
var Assets, _ = fs.Sub(embedded, "assets")
