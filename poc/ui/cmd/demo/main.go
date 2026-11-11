// demo 启动演示应用：go run ./cmd/demo，访问 http://localhost:8080。
// DEV=1 时直接读磁盘上的 assets 目录、不加指纹（cmd/dev 会设置）。
package main

import (
	"io/fs"
	"log"
	"net/http"
	"os"

	ui "github.com/ryan053857/mygoravel/poc/ui"
	"github.com/ryan053857/mygoravel/poc/ui/app"
)

func main() {
	dev := os.Getenv("DEV") != ""
	var assets fs.FS = ui.Assets
	if dev {
		assets = os.DirFS("assets")
	}
	h, err := app.New(assets, dev)
	if err != nil {
		log.Fatal(err)
	}
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("listening on %s (dev=%v)", addr, dev)
	log.Fatal(http.ListenAndServe(addr, h))
}
