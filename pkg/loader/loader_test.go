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
