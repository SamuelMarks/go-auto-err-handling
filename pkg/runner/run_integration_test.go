package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunner_Integration simulates a full run on a temporary module.
// It verifies:
// 1. Detection of ignored errors.
// 2. Level 0 Rewrite (LocalPreexisting).
// 3. Level 1 Propagate (LocalNonExisting).
func TestRunner_Integration(t *testing.T) {
	// 1. Setup Environment
	tmpDir := t.TempDir()

	// go.mod
	goMod := []byte("module example.com/runner\n\ngo 1.22\n")
	err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), goMod, 0644)
	if err != nil {
		t.Fatalf("failed to create go.mod: %v", err)
	}

	// main.go
	// Contains:
	// - failing() returning error.
	// - existing() returning error, calls failing() (Level 0 fix).
	// - nonexisting() returning void, calls failing() (Level 1 fix).
	// - usage() calls nonexisting() (Propagation).
	//
	// NOTE: Comments removed to avoid AST formatter placing them in positions
	// that break simple string matching assertions (e.g. inside if statements).
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

	// 2. Configure Runner
	opts := Options{
		LocalPreexistingErr: true,
		LocalNonExistingErr: true,
		ThirdPartyErr:       false,
		Paths:               []string{"."},
	}

	// We must change working directory to tmpDir so "." path works.
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	// 3. Run
	if err := Run(opts); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// 4. Verification
	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("failed to read back main.go: %v", err)
	}
	code := string(content)

	// Check Level 0 Fix in 'existing'
	// Should see: "err := failing(); if err != nil { return err }"
	if !strings.Contains(code, "if err != nil {") {
		t.Logf("Full Code:\n%s", code)
		t.Error("Level 0 fix missing: 'if err != nil' checks not found.")
	}

	// Check Level 1 Fix in 'nonexisting'
	// Signature should be `func nonexisting() error`
	if !strings.Contains(code, "func nonexisting() error") {
		t.Error("Level 1 signature change missing.")
	}

	// Check Level 1 Body fix
	// Should return nil at end of nonexisting likely (refactor logic adds it)
	if !strings.Contains(code, "return nil") {
		t.Error("Return nil missing in nonexisting.")
	}

	// Check Propagation in 'usage'
	// usage calls nonexisting. nonexisting now returns error. usage is void.
	// Propagate should update usage to return error or handle it.
	if !strings.Contains(code, "func usage() error") {
		t.Logf("Full Code:\n%s", code)
		t.Error("Propagation recursive fix missing: usage() should return error.")
	}
}
