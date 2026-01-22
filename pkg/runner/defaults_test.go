package runner

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_NoDefaultExclusion verifies that flag toggles exclusion of fmt.Println.
func TestRun_Defaults(t *testing.T) {
	// 1. Setup
	tmpDir := t.TempDir()
	goMod := []byte("module example.com/defaults\n\ngo 1.22\n")
	_ = os.WriteFile(filepath.Join(tmpDir, "go.mod"), goMod, 0644)

	// main.go has fmt.Println which returns error but is usually ignored
	src := `package main
import "fmt"
func run() error {
	fmt.Println("ignored")
	return nil
}
func main() {}
`
	srcPath := filepath.Join(tmpDir, "main.go")
	_ = os.WriteFile(srcPath, []byte(src), 0644)

	// Change CWD
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	// 2. Run WITH defaults (implicit exclusion) -> Should NOT apply changes
	// We use DryRun to capture output
	optsWithDefaults := Options{
		LocalPreexistingErr: true,
		Paths:               []string{"."},
		DryRun:              true,
		NoDefaultExclusion:  false,
		ThirdPartyErr:       true, // fmt is third party/stdlib
	}

	outDefault := captureRun(t, optsWithDefaults)
	if strings.Contains(outDefault, "err := fmt.Println") {
		t.Error("With defaults, fmt.Println should be ignored, but found changes")
	}

	// 3. Run WITHOUT defaults -> Should apply changes
	optsNoDefaults := Options{
		LocalPreexistingErr: true,
		Paths:               []string{"."},
		DryRun:              true,
		NoDefaultExclusion:  true,
		ThirdPartyErr:       true,
	}

	outNoDefault := captureRun(t, optsNoDefaults)
	if !strings.Contains(outNoDefault, "err := fmt.Println") {
		t.Error("With --no-default-exclusion, fmt.Println should be detected, but found no changes")
	}
}

// captureRun runs the runner and returns stdout
func captureRun(t *testing.T, opts Options) string {
	r, w, _ := os.Pipe()
	oldStdout := os.Stdout
	os.Stdout = w

	err := Run(opts)

	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}
