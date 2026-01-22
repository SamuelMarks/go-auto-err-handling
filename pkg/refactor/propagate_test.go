package refactor

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// Helper to manually construct a package with a target function reference
func setupPropagateEnv(t *testing.T, src string) (*token.FileSet, *packages.Package, *types.Func) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", src, 0)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}

	// We start with a dummy configuration
	// We need to "fake" the type checking such that the 'target' function is identified.
	conf := types.Config{Importer: nil}
	pkg, err := conf.Check("main", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}

	// Find the object for 'target'
	obj := pkg.Scope().Lookup("target")
	if obj == nil {
		t.Fatalf("could not find target function in scope")
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		t.Fatalf("target is not a function")
	}

	p := &packages.Package{
		Fset:      fset,
		Syntax:    []*ast.File{f},
		Types:     pkg,
		TypesInfo: info,
	}

	return fset, p, fn
}

func TestPropagateCallers(t *testing.T) {
	// Scenario:
	// target() is called.
	// enclosing() returns error.
	// We expect target() call to be updated to assignments and checks added.
	// Note: In the source, target() is defined as `func target() {}` (void) initially for the TypeChecker.
	// But our logic assumes we just changed it to return error.
	src := `package main

func target() {} 

func enclosing() error { 
  target()      // Case 1: Bare call
  return nil
}
`
	// To test assignment modification, we need valid Go for the parser.
	srcAssign := `package main
func target() int { return 0 } 
func enclosing() error { 
  x := target() 
  _ = x
  return nil
} 
`

	// Case 1: Void -> Error
	t.Run("VoidToError", func(t *testing.T) {
		fset, pkg, target := setupPropagateEnv(t, src)

		// Run Propagation
		n, err := PropagateCallers([]*packages.Package{pkg}, target)
		if err != nil {
			t.Fatalf("PropagateCallers failed: %v", err)
		}
		if n != 1 {
			t.Errorf("Expected 1 update, got %d", n)
		}

		// Check Output
		var buf bytes.Buffer
		printer.Fprint(&buf, fset, pkg.Syntax[0])
		out := buf.String()

		// Expectation:
		// err := target()
		// if err != nil { return err }
		if !strings.Contains(out, "err := target()") {
			t.Error("Did not find 'err := target()'")
		}
		if !strings.Contains(out, "if err != nil") {
			t.Error("Did not find error check")
		}
	})

	// Case 2: Int -> Int, Error
	t.Run("IntToIntError", func(t *testing.T) {
		fset, pkg, target := setupPropagateEnv(t, srcAssign)

		n, err := PropagateCallers([]*packages.Package{pkg}, target)
		if err != nil {
			t.Fatalf("PropagateCallers failed: %v", err)
		}
		if n != 1 {
			t.Errorf("Expected 1 update, got %d", n)
		}

		var buf bytes.Buffer
		printer.Fprint(&buf, fset, pkg.Syntax[0])
		out := buf.String()

		// Expectation:
		// x, err := target()
		if !strings.Contains(out, "x, err := target()") {
			t.Errorf("Did not find updated assignment. Got:\n%s", out)
		}
	})
}

// TestPropagateCallers_NoEnclosingError verifies handling when caller cannot return error.
func TestPropagateCallers_NoEnclosingError(t *testing.T) {
	src := `package main
func target() {} 
func main() { 
  target() // call
} 
`
	fset, pkg, target := setupPropagateEnv(t, src)

	n, err := PropagateCallers([]*packages.Package{pkg}, target)
	if err != nil {
		t.Fatalf("PropagateCallers failed: %v", err)
	}
	if n != 1 {
		t.Errorf("Expected 1 update")
	}

	var buf bytes.Buffer
	printer.Fprint(&buf, fset, pkg.Syntax[0])
	out := buf.String()

	// We expect the bare call to be converted to specific handling or silence.
	// In current implementation: `err := target()`.
	// Since main doesn't return error, we assume usage of `_`.
	// Check that we modified the line.
	if !strings.Contains(out, "err := target()") && !strings.Contains(out, "_") {
		t.Errorf("Expected modification to assignment or blank, got:\n%s", out)
	}
}
