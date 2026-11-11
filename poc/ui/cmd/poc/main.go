// poc 是演示应用的命令行：
//
//	go run ./cmd/poc serve                                          # http://localhost:8080
//	go run ./cmd/poc dev --config config/app.yaml,config/local.yaml # http://localhost:7331，自动刷新
package main

import (
	"context"
	"errors"
	"log"
	"net/http"

	ui "github.com/ryan053857/mygoravel/poc/ui"
	"github.com/ryan053857/mygoravel/poc/ui/app"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/command/devcmd"
)

type settings struct {
	Addr      string `yaml:"addr"`
	StaticDir string `yaml:"static_dir"` // 只在 config/local.yaml 中设置
}

func serve(ctx context.Context, cfg settings) error {
	h, err := app.New(ui.Assets, cfg.StaticDir != "")
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: cfg.Addr, Handler: h}
	go func() { <-ctx.Done(); srv.Close() }()
	log.Printf("listening on %s (static_dir=%q)", cfg.Addr, cfg.StaticDir)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func main() {
	root := command.Root("poc", "shadcn-templ PoC")
	root.AddCommand(
		command.Run("serve", "启动演示应用", serve),
		devcmd.New(devcmd.Options{
			Main:   "./cmd/poc",
			Static: "assets",
			Tools: []devcmd.Tool{
				devcmd.Tailwind("assets/css/globals.css", "assets/css/app.css"),
				{Watch: []string{"shadcn-templ", "bundle", "--watch"}, Build: []string{"shadcn-templ", "bundle"}},
			},
		}, func(s settings) string { return s.Addr }),
	)
	command.Execute(root)
}
