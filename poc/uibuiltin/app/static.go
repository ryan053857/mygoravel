package app

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// Static 是租户端的静态文件：Tailwind 生成的 css/app.css。
var Static, _ = fs.Sub(files, "static")
