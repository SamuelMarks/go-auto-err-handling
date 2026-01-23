package runner

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_Defaults verifies that implicit defaults enable filtering properly.
// e.g. UseDefaultExclusions=true vs false.
func TestRun_Defaults(t *testing.T) {
	// Setup
	tmpDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module test\ngo 1.22\n"), 0644)

	src := `package main
import "fmt"
func run() error {
  fmt.Println("ignored")
  return nil
}
func main() {}
`
	_ = os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(src), 0644)

	oldWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer interface{}(func() { _ = os.Chdir(oldWd) }).(func())()

	// 1. With Defaults (UseDefaultExclusions=true) -> fmt.Println should be ignored
	optsDefault := Options{
		EnablePreexistingErr: true,
		EnableThirdPartyErr:  true,
		UseDefaultExclusions: true, // Default filters active
		Paths:                []string{"."},
		DryRun:               true,
	}
	outDefault, err := captureRunLocally(t, optsDefault)
	if err != nil {
		t.Fatalf("captureRun failed: %v", err)
	}
	if strings.Contains(outDefault, "err := fmt.Println") {
		t.Error("fmt.Println changed despite default exclusion")
	}

	// 2. Without Defaults (UseDefaultExclusions=false) -> fmt.Println should be checked
	optsNoDefault := Options{
		EnablePreexistingErr: true,
		EnableThirdPartyErr:  true,
		UseDefaultExclusions: false, // No filters
		Paths:                []string{"."},
		DryRun:               true,
	}
	outNoDefault, err := captureRunLocally(t, optsNoDefault)
	if err != nil {
		t.Fatalf("captureRun failed: %v", err)
	}
	if !strings.Contains(outNoDefault, "err := fmt.Println") {
		t.Error("fmt.Println NOT changed despite UseDefaultExclusions=false")
	}
}

// Rename helper to avoid conflict with run_integration_test.go if compiled together
func captureRunLocally(t *testing.T, opts Options) (string, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}

	oldStdout := os.Stdout
	os.Stdout = w

	err = Run(opts)
	if err != nil {
		os.Stdout = oldStdout
		w.Close()
		return "", err
	}

	err = w.Close()
	if err != nil {
		os.Stdout = oldStdout
		return "", err
	}

	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}
