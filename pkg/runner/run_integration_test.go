package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunner_Integration verifies the full analysis and refactoring cycle.
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

func failing() error { return nil } 

func existing() error { 
  failing() 
  return nil
} 

func nonexisting() { 
  failing() 
} 

func usage() { 
  nonexisting() 
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
	defer interface{}(func() { _ = os.Chdir(oldWd) }).(func())()

	// Configure Runner with Defaults (Everything Enabled)
	opts := Options{
		EnablePreexistingErr: true, // Enabled
		EnableNonExistingErr: true, // Enabled
		EnableThirdPartyErr:  true, // Enabled
		Paths:                []string{"."},
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

	// Level 0: 'existing' should check error
	// With collapse: "if err := failing(); err != nil {"
	if !strings.Contains(code, "if err := failing(); err != nil {") && !strings.Contains(code, "if err != nil {") {
		t.Errorf("Level 0 fix missing: check not found. Code:\n%s", code)
	}

	// Level 1: 'nonexisting' should now return error
	if !strings.Contains(code, "func nonexisting() error") {
		t.Error("Level 1 signature change missing.")
	}

	// Propagation: 'usage' calls 'nonexisting', so 'usage' must change
	if !strings.Contains(code, "func usage() error") {
		t.Error("Propagation recursive fix missing.")
	}
}
