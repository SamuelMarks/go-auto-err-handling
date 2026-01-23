package runner

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_DryRun verifies dry-run prints diffs and doesn't verify files.
func TestRun_DryRun(t *testing.T) {
	tmpDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module test\ngo 1.22\n"), 0644)

	src := `package main
func fail() error { return nil }
func main() {
  fail()
}
`
	srcPath := filepath.Join(tmpDir, "main.go")
	_ = os.WriteFile(srcPath, []byte(src), 0644)

	oldWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer interface{}(func() { _ = os.Chdir(oldWd) }).(func())()

	// Options: Enable NonExisting (Level 1) to force signature check on main
	opts := Options{
		EnableNonExistingErr: true,
		EnablePreexistingErr: true,
		Paths:                []string{"."},
		DryRun:               true,
	}

	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w

	err := Run(opts)

	w.Close()
	os.Stdout = old
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	out := buf.String()

	if !strings.Contains(out, "err := fail()") {
		t.Errorf("Expected diff in stdout. Got:\n%s", out)
	}

	content, _ := os.ReadFile(srcPath)
	if string(content) != src {
		t.Error("File modified on disk")
	}
}
