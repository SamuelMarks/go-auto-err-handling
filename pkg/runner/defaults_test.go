package runner

import (
	"bytes"
	"io"
	"log"
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

// captureRunLocally helper to capture stdout and log output
func captureRunLocally(t *testing.T, opts Options) (string, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}

	// Capture Standard Output
	oldStdout := os.Stdout
	os.Stdout = w

	// Capture Log Output
	// The runner uses the global log package (log.Printf), which writes to standard logger output.
	// We redirect standard log output to our pipe.
	// Note: We don't restore old flags/prefix, assuming tests don't depend on them critically or run in isolation.
	log.SetOutput(w)

	err = Run(opts)

	// Restore
	os.Stdout = oldStdout
	// Restore log to stderr (standard default)
	log.SetOutput(os.Stderr)

	if err != nil {
		if err := w.Close(); err != nil {
			return "", err
		}
		return "", err
	}

	if err := w.Close(); err != nil {
		return "", err
	}

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}
