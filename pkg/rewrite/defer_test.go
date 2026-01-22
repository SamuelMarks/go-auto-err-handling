package rewrite

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

	return NewInjector(pkg), file
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

	var buf bytes.Buffer
	printer.Fprint(&buf, injector.Fset, file)
	out := buf.String()

	// Check DoWork
	// Before: func DoWork() (int, error) { defer Close() ... }
	// After: func DoWork() (ret0 int, err error) { defer func() { err = errors.Join(err, Close()) }(); ... }

	if !strings.Contains(out, "func DoWork() (ret0 int, err error)") {
		t.Errorf("Signature not updated correctly. Got:\n%s", out)
	}

	if !strings.Contains(out, `defer func() {
	err = errors.Join(err, Close())
}()`) && !strings.Contains(out, `err = errors.Join(err, Close())`) {
		t.Errorf("Defer block not rewritten correctly. Got:\n%s", out)
	}

	// Check import. Note: astutil may combine imports into a block (import ( "fmt"; "errors" )).
	// Exact string matching 'import "errors"' might fail if newlines exist.
	if !strings.Contains(out, `"errors"`) {
		t.Error("errors import not added")
	}

	// Check NoError (Should be untouched because signature doesn't return error initially, and logic requires existing error return for Join)
	// (Note: AddErrorToSignature is Level 1 logic, RewriteDefers assumes Level 1 ran or existing signature is compatible)
	if strings.Contains(out, "func NoError() (err error)") {
		t.Error("NoError signature should not be changed by RewriteDefers (it requires existing error path)")
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

	var buf bytes.Buffer
	printer.Fprint(&buf, injector.Fset, file)
	out := buf.String()

	if !strings.Contains(out, `defer func() {
	err = errors.Join(err, Close())
}()`) && !strings.Contains(out, `errors.Join`) {
		t.Errorf("Defer not rewritten. Got:\n%s", out)
	}
}
