package clientgen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-tenon/tenon/httpx/rest"
	"github.com/go-tenon/tenon/httpx/rest/clientgen"
)

const exampleTSOut = "internal/example/ts"

func generateTS(t *testing.T, api *rest.API) *clientgen.Result {
	t.Helper()
	res, err := clientgen.GenerateTS(api, clientgen.TSOptions{Main: exampleMain})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestGeneratedTSExampleIsUpToDate(t *testing.T) {
	res := generateTS(t, exampleAPI(rest.ModeOpenAPI, nil))
	if len(res.Skipped) > 0 {
		t.Fatalf("skipped %q", res.Skipped)
	}
	if *update {
		if err := clientgen.Write(exampleTSOut, res.Files); err != nil {
			t.Fatal(err)
		}
		return
	}
	for name, data := range res.Files {
		disk, err := os.ReadFile(filepath.Join(exampleTSOut, name))
		if err != nil || string(disk) != string(data) {
			t.Errorf("%s is out of date; run go test ./httpx/rest/clientgen -run TestGeneratedTSExampleIsUpToDate -update", name)
		}
	}
}

// TestTSClientCompiles 用严格模式的 tsc 检查生成的代码。需要 bun，没有时跳过。
func TestTSClientCompiles(t *testing.T) {
	bun := requireBun(t)
	cmd := exec.Command(bun, "x", "-p", "typescript@5.9.3", "tsc", "--noEmit", "--strict", "--target", "es2022",
		"--lib", "es2022,dom,dom.iterable", "--module", "esnext", "--moduleResolution", "bundler", "--verbatimModuleSyntax",
		"api.ts", "errors.ts", "runtime.ts")
	cmd.Dir = exampleTSOut
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tsc: %v\n%s", err, out)
	}
}

// TestTSRoundTrip 用 bun 运行 testdata/roundtrip.ts：生成的客户端调用真实的示例服务。
func TestTSRoundTrip(t *testing.T) {
	bun := requireBun(t)
	base := serve(t, tokens{"secret": true})
	client, err := filepath.Abs(filepath.Join(exampleTSOut, "api.ts"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bun, "run", "testdata/roundtrip.ts")
	cmd.Env = append(os.Environ(), "BASE_URL="+base+"/api", "CLIENT="+client)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "roundtrip ok") {
		t.Fatalf("bun: %v\n%s", err, out)
	}
}

func requireBun(t *testing.T) string {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("bun is required in CI")
		}
		t.Skip("bun not installed")
	}
	return bun
}
