package rewrite

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/imports"
)

// setupTestEnv parses source code and returns the Injector and File.
// We mock TypesInfo for standard error return checks.
func setupTestEnv(t *testing.T, src string) (*Injector, *ast.File) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", src, 0)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	// Mock Type checking just enough to recognize "error"
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}

	// Manually populate TypesInfo for calls that look like error returns
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			// Heuristic: if call is to "f()" or "Close()", treat as error definition
			s := callToString(call)
			if strings.Contains(s, "Close") || strings.Contains(s, "Fail") {
				// Mock as error
				errType := types.Universe.Lookup("error").Type()
				info.Types[call] = types.TypeAndValue{
					Type: errType,
				}
			}
		}
		return true
	})

	pkg := &packages.Package{
		Fset:      fset,
		TypesInfo: info,
	}

	return NewInjector(pkg, ""), file
}

func callToString(call *ast.CallExpr) string {
	if id, ok := call.Fun.(*ast.Ident); ok {
		return id.Name
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	return ""
}

// renderWithImports helper simulating runner handling.
func renderWithImports(t *testing.T, fset *token.FileSet, file *ast.File) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		t.Fatal(err)
	}
	out, err := imports.Process("main.go", buf.Bytes(), nil)
	if err != nil {
		return buf.String()
	}
	return string(out)
}

func TestRewriteDefers(t *testing.T) {
	src := `package main

import "fmt"

func Close() error { return nil }

func DoWork() (int, error) {
  defer Close()
  return 1, nil
}

func NoError() {
  defer Close() // enclosing func has no error return, should be skipped
}
`
	injector, file := setupTestEnv(t, src)

	changed, err := injector.RewriteDefers(file)
	if err != nil {
		t.Fatalf("RewriteDefers failed: %v", err)
	}
	if !changed {
		t.Error("Next Expected changes, got none")
	}

	out := renderWithImports(t, injector.Fset, file)

	// Check DoWork
	// Before: func DoWork() (int, error) { defer Close() ... }
	// After: func DoWork() (i int, err error) { defer func() { err = errors.Join(err, Close()) }(); ... }
	// The variable name for 'int' is 'i' based on refactor.NameForType defaults.

	if !strings.Contains(out, "func DoWork() (i int, err error)") {
		t.Errorf("Signature not updated correctly. Got:\n%s", out)
	}

	if !strings.Contains(out, `defer func() {`) && !strings.Contains(out, `errors.Join`) {
		t.Errorf("Defer block not rewritten correctly. Got:\n%s", out)
	}

	// Check import.
	if !strings.Contains(out, `"errors"`) {
		t.Error("errors import not added")
	}

	// Check NoError (Should be untouched)
	if strings.Contains(out, "func NoError() (err error)") {
		t.Error("NoError signature should not be changed by RewriteDefers")
	}
}

func TestRewriteDefers_Closure(t *testing.T) {
	// Scenario: A defer deeply nested in a closure that returns error.
	src := `package main
func Close() error { return nil }

func Top() {
  _ = func() error {
    defer Close()
    return nil
  }
}
`
	injector, file := setupTestEnv(t, src)
	changed, err := injector.RewriteDefers(file)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("Expected deferred closure change")
	}

	out := renderWithImports(t, injector.Fset, file)

	// We expect the closure signature to be converted from unnamed to named
	// func() error -> func() (err error)
	// And the defer to match.

	expectedSig := `func() (err error) {`
	if !strings.Contains(out, expectedSig) {
		t.Errorf("Closure signature not updated. Got:\n%s", out)
	}

	// Defer logic validation
	if !strings.Contains(out, "err = errors.Join(err, Close())") {
		t.Error("Defer inside closure not rewritten")
	}
}

func TestRewriteDefers_MixedErrorName(t *testing.T) {
	// Scenario: Function returns "e" instead of "err".
	src := `package main
func Close() error { return nil }
func CustomName() (e error) {
  defer Close()
  return nil
}
`
	injector, file := setupTestEnv(t, src)
	changed, err := injector.RewriteDefers(file)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("Expected change")
	}

	out := renderWithImports(t, injector.Fset, file)

	// It should bind to "e", not generate "err"
	if !strings.Contains(out, "e = errors.Join(e, Close())") {
		t.Errorf("Expected usage of 'e', got:\n%s", out)
	}
	if strings.Contains(out, "(e error, err error)") {
		t.Error("Should not have added extra error return")
	}
}

func TestRewriteDefers_AlreadyNamed(t *testing.T) {
	src := `package main
func Close() error { return nil }
func Do() (err error) {
  defer Close()
  return nil
}`
	injector, file := setupTestEnv(t, src)
	changed, err := injector.RewriteDefers(file)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("Expected change")
	}

	out := renderWithImports(t, injector.Fset, file)

	if !strings.Contains(out, `defer func() {`) {
		t.Errorf("Defer not rewritten. Got:\n%s", out)
	}
}
