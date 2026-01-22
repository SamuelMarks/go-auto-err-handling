package runner

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_DryRun verifies that changes are printed to stdout and not saved to disk.
func TestRun_DryRun(t *testing.T) {
	// 1. Setup Source
	tmpDir := t.TempDir()
	goMod := []byte("module example.com/dryrun\n\ngo 1.22\n")
	_ = os.WriteFile(filepath.Join(tmpDir, "go.mod"), goMod, 0644)

	src := `package main
func fail() error { return nil }
func main() {
	fail()
}
`
	srcPath := filepath.Join(tmpDir, "main.go")
	_ = os.WriteFile(srcPath, []byte(src), 0644)

	// Change CWD for loader (same workaround as integration test)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	// 2. Configure Options with DryRun
	opts := Options{
		LocalPreexistingErr: false, // main() is void, so Preexisting won't trigger if strict
		LocalNonExistingErr: true,  // Use NonExisting to force mutation of main() or handler
		Paths:               []string{"."},
		DryRun:              true,
	}

	// 3. Capture Stdout
	// Since Run() prints via log and fmt, we need to capture stdout.
	// Note: Run() uses log.Printf (stderr by default usually, but main.go redirected to stdout).
	// But Run logic here imports standard log. Standard log writes to stderr by default unless set.
	// The provided `main.go` in the original context set `log.SetOutput(stdout)`.
	// Since we are calling `runner.Run` directly, we depend on global log package state.
	// We will capture stdout/stderr via pipe.

	r, w, _ := os.Pipe()
	oldStdout := os.Stdout
	os.Stdout = w

	// Run
	err := Run(opts)

	// Restore
	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	output := buf.String()

	// 4. Verify Diff in Output
	// We look for unified diff header or content
	if !strings.Contains(output, "--- main.go") && !strings.Contains(output, "+++ main.go") {
		// gotextdiff prints paths.
		// If main.go matches, we expect diff signs
		if !strings.Contains(output, "err := fail()") {
			t.Errorf("Expected diff content 'err := fail()' in output. Got:\n%s", output)
		}
	} else {
		// Check for diff hunk headers
		if !strings.Contains(output, "@@") {
			t.Errorf("Expected diff format in output. Got:\n%s", output)
		}
	}

	// 5. Verify File on Disk Unchanged
	content, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != src {
		t.Error("DryRun modified the file on disk!")
	}
}
