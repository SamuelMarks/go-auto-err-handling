package imports

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// parseSource helper parses a source string into an AST file and FileSet.
func parseSource(t *testing.T, src string) (*token.FileSet, *ast.File) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parser failed: %v", err)
	}
	return fset, file
}

// TestAdd_NoOp verifies that Add is now a no-op.
func TestAdd_NoOp(t *testing.T) {
	src := "package main\n\nfunc main() {}"
	fset, file := parseSource(t, src)

	// Attempt to add import
	added := Add(fset, file, "fmt")

	if added {
		t.Error("Add() returned true, expected false (no-op)")
	}

	// Verify existing imports count hasn't changed (0)
	if len(file.Imports) != 0 {
		t.Errorf("Expected 0 imports, got %d", len(file.Imports))
	}
}

// TestAddNamed_NoOp verifies that AddNamed is now a no-op.
func TestAddNamed_NoOp(t *testing.T) {
	src := "package main\n\nfunc main() {}"
	fset, file := parseSource(t, src)

	added := AddNamed(fset, file, "f", "fmt")

	if added {
		t.Error("AddNamed() returned true, expected false (no-op)")
	}

	if len(file.Imports) != 0 {
		t.Errorf("Expected 0 imports, got %d", len(file.Imports))
	}
}
