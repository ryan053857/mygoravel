// 运行演示应用：go run ./cmd/demo，然后访问 http://localhost:8080（密码 secret）。
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/poc/htmx4/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cfg := httpx.DefaultConfig()
	cfg.Addr = ":8080"
	a := app.New(app.Options{HTTP: cfg})
	if err := httpx.ListenAndServe(ctx, cfg, a.Handler, httpx.ServeOptions{OnDraining: a.Drain.Start}); err != nil {
		panic(err)
	}
}
