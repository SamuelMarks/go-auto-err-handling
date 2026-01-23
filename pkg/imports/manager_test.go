package imports

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// parseSource helper parses a source string into an AST file and FileSet.
//
// t: The testing framework handle.
// src: The source code string to parse.
//
// Returns the FileSet and the parsed AST File.
func parseSource(t *testing.T, src string) (*token.FileSet, *ast.File) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parser failed: %v", err)
	}
	return fset, file
}

// TestAdd verifies that Add correctly injects new imports and deduplicates existing ones.
func TestAdd(t *testing.T) {
	src := `package main

func main() {}`
	fset, file := parseSource(t, src)

	// 1. Add new import "fmt"
	added := Add(fset, file, "fmt")
	if !added {
		t.Error("Add() returned false, expected true for new import")
	}

	// Verify AST modification
	if len(file.Imports) != 1 {
		t.Errorf("Expected 1 import, got %d", len(file.Imports))
	}
	if file.Imports[0].Path.Value != `"fmt"` {
		t.Errorf("Expected import path \"fmt\", got %s", file.Imports[0].Path.Value)
	}

	// 2. Add duplicate import "fmt"
	// Should return false and not modify AST count
	addedDuplicate := Add(fset, file, "fmt")
	if addedDuplicate {
		t.Error("Add() returned true, expected false for existing import")
	}
	if len(file.Imports) != 1 {
		t.Errorf("Expected import count to remain 1, got %d", len(file.Imports))
	}
}

// TestAddNamed verifies that AddNamed correctly injects aliased imports.
func TestAddNamed(t *testing.T) {
	src := `package main

func main() {}`
	fset, file := parseSource(t, src)

	// 1. Add aliased import f "fmt"
	added := AddNamed(fset, file, "f", "fmt")
	if !added {
		t.Error("AddNamed() returned false, expected true for new alias")
	}

	// Verify AST modification
	if len(file.Imports) != 1 {
		t.Errorf("Expected 1 import, got %d", len(file.Imports))
	}
	if file.Imports[0].Name == nil || file.Imports[0].Name.Name != "f" {
		t.Errorf("Expected alias 'f', got %v", file.Imports[0].Name)
	}
	if file.Imports[0].Path.Value != `"fmt"` {
		t.Errorf("Expected import path \"fmt\", got %s", file.Imports[0].Path.Value)
	}

	// 2. Add duplicate alias
	addedDuplicate := AddNamed(fset, file, "f", "fmt")
	if addedDuplicate {
		t.Error("AddNamed() returned true, expected false for duplicate alias")
	}
}
