package loader

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadPackages verifies that the loader correctly identifies and parses a temporary Go module.
func TestLoadPackages(t *testing.T) {
	// 1. Setup a temporary directory for a dummy module
	tmpDir := t.TempDir()

	// Write go.mod
	goModParams := []byte("module example.com/testmod\n\ngo 1.22\n")
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), goModParams, 0644); err != nil {
		t.Fatalf("failed to write go.mod: %v", err)
	}

	// Write a valid Go file
	mainGoParams := []byte(`package main

import "fmt" 

func main() { 
  fmt.Println("Hello, World!") 
} 
`)
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), mainGoParams, 0644); err != nil {
		t.Fatalf("failed to write main.go: %v", err)
	}

	// 2. Execute LoadPackages
	patterns := []string{"."}
	pkgs, err := LoadPackages(patterns, tmpDir)
	if err != nil {
		t.Fatalf("LoadPackages returned unexpected error: %v", err)
	}

	// 3. Validation
	if len(pkgs) == 0 {
		t.Fatal("expected at least one package, got 0")
	}

	pkg := pkgs[0]

	// Check if ID matches expected module name
	if pkg.ID != "example.com/testmod" && pkg.Name != "main" {
		t.Errorf("expected package ID 'example.com/testmod' or Name 'main', got ID '%s' Name '%s'", pkg.ID, pkg.Name)
	}

	// Check if Syntax (AST) was loaded
	if len(pkg.Syntax) == 0 {
		t.Error("expected Syntax trees to be loaded, got 0 files")
	}

	// Check if Types (TypeInfo) were loaded
	if pkg.Types == nil {
		t.Error("expected Types to be loaded, got nil")
	}

	// Check if TypesInfo is populated
	if pkg.TypesInfo == nil {
		t.Error("expected TypesInfo to be loaded, got nil")
	}
}

// TestLoadPackages_SmartRecursion verifies that the loader switches to recursive mode
// when the root directory has no files but contains a go.mod.
func TestLoadPackages_SmartRecursion(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Setup Module Root with go.mod but NO go files
	goMod := []byte("module example.com/recursive\n\ngo 1.22\n")
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), goMod, 0644); err != nil {
		t.Fatalf("failed to write go.mod: %v", err)
	}

	// 2. Create a subdirectory with a package
	subDir := filepath.Join(tmpDir, "cmd")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("failed to create sub dir: %v", err)
	}

	src := []byte(`package main
func main() {} 
`)
	if err := os.WriteFile(filepath.Join(subDir, "main.go"), src, 0644); err != nil {
		t.Fatalf("failed to write sub/main.go: %v", err)
	}

	// 3. Load with "." targeting the empty root
	pkgs, err := LoadPackages([]string{"."}, tmpDir)
	if err != nil {
		t.Fatalf("LoadPackages failed: %v", err)
	}

	// 4. Verify it found the nested package via recursion
	if len(pkgs) == 0 {
		t.Fatal("Expected recursion to find packages, got 0")
	}

	found := false
	for _, p := range pkgs {
		if p.Name == "main" { // example.com/recursive/cmd
			found = true
			break
		}
	}
	if !found {
		t.Error("Did not find sub-package 'main' after smart recursion")
	}
}

// TestLoadPackages_WithSyntaxError verifies that the loader captures syntax errors
// found in the target package, enabling the logging path in LoadPackages.
func TestLoadPackages_WithSyntaxError(t *testing.T) {
	tmpDir := t.TempDir()

	// Write go.mod
	_ = os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example.com/bad\n\ngo 1.22\n"), 0644)

	// Write a Go file with a syntax error (incomplete statement)
	badSrc := []byte(`package main
func main() { 
  return 1 + 
} 
`)
	if err := os.WriteFile(filepath.Join(tmpDir, "bad.go"), badSrc, 0644); err != nil {
		t.Fatalf("failed to write bad.go: %v", err)
	}

	pkgs, err := LoadPackages([]string{"."}, tmpDir)
	if err != nil {
		t.Fatalf("LoadPackages failed: %v", err)
	}

	if len(pkgs) == 0 {
		t.Fatal("expected package despite syntax errors")
	}

	pkg := pkgs[0]
	if len(pkg.Errors) == 0 {
		t.Error("expected package errors to be populated for syntax error")
	}
}

// TestLoadPackages_ErrorHandling checks behavior when loading non-existent locations.
// Note: packages.Load usually doesn't return a pure error for bad patterns, it returns packages with errors attached.
// This test ensures the function doesn't panic and returns the underlying tool's response.
func TestLoadPackages_ErrorHandling(t *testing.T) {
	tmpDir := t.TempDir() // Empty dir, no go.mod

	patterns := []string{"."}
	pkgs, err := LoadPackages(patterns, tmpDir)

	// In standard go/packages behavior, running on an empty folder without modules might return an error
	// or a package struct containing an error, depending on the environment.
	// We just want to ensure our wrapper handles the call gracefully.
	// Our smart recursion logic checks for go.mod, so it should NOT recurse here, keeping the error.

	if err != nil {
		// Valid outcome (e.g. no go.mod found)
		return
	}

	// If it didn't error, it might have returned a package with errors
	if len(pkgs) > 0 {
		// Just ensure we can access the list without panic
		_ = pkgs[0].Errors
	}
}

// TestShouldRetryRecursive_Logic verifies the helper predicate logic via placeholder.
func TestShouldRetryRecursive_Logic(t *testing.T) {
	tmpDir := t.TempDir()
	// Explicitly ignore unused var to pass lint check until we mock packages.Package type internally.
	_ = tmpDir
}
