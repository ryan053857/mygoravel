// poc 是演示应用的命令行：go run ./cmd/poc serve，访问 http://localhost:8080/ops/articles 与 /app/。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/poc/uibuiltin/app"
)

type settings struct {
	Addr       string `yaml:"addr"`
	app.Config `yaml:",inline"`
}

func serve(ctx context.Context, cfg settings) error {
	h, err := app.New(cfg.Config)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: cfg.Addr, Handler: h}
	go func() { <-ctx.Done(); srv.Close() }()
	log.Printf("listening on %s", cfg.Addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func main() {
	root := command.Root("poc", "webui PoC")
	root.AddCommand(command.Run("serve", "启动演示应用", serve))
	command.Execute(root)
}
