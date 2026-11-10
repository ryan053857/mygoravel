package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestColorfulOnlyForTerminals(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if colorful(&bytes.Buffer{}) || colorful(f) {
		t.Fatal("non-terminal writers must not be colored")
	}
	t.Setenv("NO_COLOR", "1")
	if colorful(os.Stderr) {
		t.Fatal("NO_COLOR must disable colors")
	}
}
