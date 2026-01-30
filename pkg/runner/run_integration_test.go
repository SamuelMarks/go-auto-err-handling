package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunner_Integration verifies the full analysis and refactoring cycle using DST.
func TestRunner_Integration(t *testing.T) {
	tmpDir := t.TempDir()

	// go.mod
	goMod := []byte("module example.com/runner\n\ngo 1.22\n")
	err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), goMod, 0644)
	if err != nil {
		t.Fatalf("failed to create go.mod: %v", err)
	}

	// main.go containing unhandled errors in various scenarios
	mainSrc := `package main

import "fmt"

func failing() error { return nil }

// comment attached to existing
func existing() error {
	failing()
	return nil
}

func nonexisting() {
	failing() // inline comment
}

func usage() {
	nonexisting()
}

func main() {
	// Should log fatal
	usage()
}
`
	err = os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(mainSrc), 0644)
	if err != nil {
		t.Fatalf("failed to create main.go: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	// Configure Runner with Defaults (Everything Enabled)
	opts := Options{
		EnablePreexistingErr: true,
		EnableNonExistingErr: true,
		EnableThirdPartyErr:  true,
		Paths:                []string{"."},
		DryRun:               false,
	}

	// Run
	if err := Run(opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Verification
	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("failed to read back main.go: %v", err)
	}
	code := string(content)

	// Check 1: 'existing' should check error using 'err' variable
	if !strings.Contains(code, "if err := failing(); err != nil") {
		t.Errorf("Level 0 fix missing. Code dump:\n%s", code)
	}
	// Check comment preservation
	if !strings.Contains(code, "// comment attached to existing") {
		t.Error("Top level comment lost")
	}

	// Check 2: 'nonexisting' signature upgrade
	if !strings.Contains(code, "func nonexisting() error") {
		t.Error("Level 1 signature change missing.")
	}
	if !strings.Contains(code, "// inline comment") {
		t.Error("Inline comment lost.")
	}

	// Check 3: 'usage' signature upgrade (Propagation)
	if !strings.Contains(code, "func usage() error") {
		t.Error("Propagation recursive fix missing.")
	} else if !strings.Contains(code, "if err := nonexisting(); err != nil") {
		t.Error("Usage body not updated.")
	}

	// Check 4: 'main' terminal handling
	if !strings.Contains(code, "log.Fatal(err)") {
		t.Error("Main terminal handling missing.")
	}
}

func TestRunner_PanicRewrite(t *testing.T) {
	tmpDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module panic\ngo 1.22\n"), 0644)

	src := `package main
import "errors"
func do() {
	panic(errors.New("fail"))
}
`
	_ = os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(src), 0644)

	oldWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer func() { _ = os.Chdir(oldWd) }()

	opts := Options{
		PanicToReturn: true,
		Paths:         []string{"."},
	}

	if err := Run(opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	content, _ := os.ReadFile("main.go")
	code := string(content)

	if !strings.Contains(code, "func do() error") {
		t.Error("Signature not upgraded for panic")
	}
	if !strings.Contains(code, `return errors.New("fail")`) {
		t.Error("Panic not rewritten to return")
	}
}
