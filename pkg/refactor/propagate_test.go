package refactor

import (
	"bytes"
	"fmt"
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

type mockPropImporter struct{}

func (m mockPropImporter) Import(path string) (*types.Package, error) {
	if path == "testing" {
		pkg := types.NewPackage("testing", "testing")

		// Create 'T' as a named type (struct)
		tName := types.NewTypeName(token.NoPos, pkg, "T", nil)
		tNamed := types.NewNamed(tName, types.NewStruct(nil, nil), nil)
		pkg.Scope().Insert(tName)

		// Create 'Helper' method on *T
		// Receiver type is *T
		recvType := types.NewPointer(tNamed)
		recvVar := types.NewVar(token.NoPos, pkg, "t", recvType)

		// Method signature: func (t *T) Helper()
		helperSig := types.NewSignature(recvVar, nil, nil, false)
		helperFunc := types.NewFunc(token.NoPos, pkg, "Helper", helperSig)

		// Attach method to T
		tNamed.AddMethod(helperFunc)

		pkg.MarkComplete()
		return pkg, nil
	}
	return nil, fmt.Errorf("package %q not found", path)
}

func setupPropagateEnv(t *testing.T, src, targetName string) (*token.FileSet, *packages.Package, *types.Func) {
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

	conf := types.Config{
		Importer: mockPropImporter{},
		Error:    func(err error) { t.Logf("Type check error: %v", err) },
	}
	pkg, err := conf.Check("main", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}

	obj := pkg.Scope().Lookup(targetName)
	var fn *types.Func
	if obj != nil {
		fn = obj.(*types.Func)
	}

	p := &packages.Package{
		Fset:      fset,
		Syntax:    []*ast.File{f},
		Types:     pkg,
		TypesInfo: info,
	}

	return fset, p, fn
}

func renderWithImports(t *testing.T, fset *token.FileSet, file *ast.File) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		t.Fatalf("format error: %v", err)
	}
	out, err := imports.Process("main.go", buf.Bytes(), nil)
	if err != nil {
		return buf.String()
	}
	return string(out)
}

func TestPropagateCallers_Recursive(t *testing.T) {
	// Chain: Main -> Intermediary() -> Target()
	// Target() returns error (we verify propagation starting from Target).
	// Intermediary() is VOID initially. It should be upgraded to return error.
	// Main() calls Intermediary. Main should get terminal handling.
	src := `package main
func Target() {} 
func Intermediary() { 
  Target() 
} 
func main() { 
  Intermediary() 
} 
`
	// Pass explicit target name "Target"
	fset, pkg, target := setupPropagateEnv(t, src, "Target")

	// Execute propagation
	n, err := PropagateCallers([]*packages.Package{pkg}, target, "log-fatal")
	if err != nil {
		t.Fatalf("PropagateCallers failed: %v", err)
	}

	// We expect 2 updates:
	// 1. Intermediary: updated to handle Target() and upgraded signature.
	// 2. Main: updated to handle Intermediary() (terminal).
	if n != 2 {
		t.Errorf("Expected 2 updates (recursive), got %d", n)
	}

	out := renderWithImports(t, fset, pkg.Syntax[0])

	// Verify Intermediary Upgrade
	// func Intermediary() error
	if !strings.Contains(out, "func Intermediary() error") {
		t.Errorf("Intermediary signature not upgraded. Got:\n%s", out)
	}
	// if err := Target(); err != nil { return err }
	if !strings.Contains(out, "if err := Target(); err != nil") {
		t.Error("Intermediary did not check Target error")
	}
	if !strings.Contains(out, "return err") {
		t.Error("Intermediary did not return error")
	}

	// Verify Main Terminal Handling
	// if err := Intermediary(); err != nil { log.Fatal(err) }
	if !strings.Contains(out, "if err := Intermediary(); err != nil") {
		t.Error("Main did not check Intermediary error")
	}
	if !strings.Contains(out, "log.Fatal(err)") {
		t.Error("Main did not inject fatal handler")
	}
}

func TestPropagateCallers_Simple(t *testing.T) {
	src := `package main
func target() {} 
func caller() error { 
  target() 
  return nil
} 
`
	fset, pkg, target := setupPropagateEnv(t, src, "target")
	n, err := PropagateCallers([]*packages.Package{pkg}, target, "log-fatal")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Expected 1 update, got %d", n)
	}
	out := renderWithImports(t, fset, pkg.Syntax[0])
	if !strings.Contains(out, "if err := target(); err != nil") {
		t.Error("Caller not updated")
	}
}

func TestPropagateCallers_HelperStop(t *testing.T) {
	// Chain: TestFunc -> Helper -> Target
	// Helper is T.Helper(). Should NOT upgrade signature. Should use t.Fatal.
	src := `package main
import "testing" 
func Target() {} 
func Helper(t *testing.T) { 
  t.Helper() 
  Target() 
} 
func TestFoo(t *testing.T) { 
  Helper(t) 
} 
`
	fset, pkg, target := setupPropagateEnv(t, src, "Target")
	n, err := PropagateCallers([]*packages.Package{pkg}, target, "panic")
	if err != nil {
		t.Fatal(err)
	}

	// Target -> Helper (1 update).
	// Helper is TestHelper (terminal). Recursion stops.
	// TestFoo calls Helper. Since Helper signature didn't change, TestFoo is untouched.
	// Total updates: 1.
	if n != 1 {
		t.Errorf("Expected 1 update (Helper only), got %d", n)
	}

	out := renderWithImports(t, fset, pkg.Syntax[0])

	// Helper should have t.Fatal
	if !strings.Contains(out, "t.Fatal(err)") {
		t.Error("Helper missing t.Fatal")
	}
	// Helper signature remains void
	if strings.Contains(out, "func Helper(t *testing.T) error") {
		t.Error("Helper signature incorrectly upgraded")
	}
}
