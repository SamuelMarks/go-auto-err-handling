package refactor

import (
	"bytes"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
	"golang.org/x/tools/go/packages"
)

// --- Test Setup Helpers ---

// setupPropagateEnv creates a comprehensive environment for testing.
// It writes files to a temp dir to ensure 'packages.Load' logic or 'decorator'
// file reading logic works if it falls back to disk.
func setupPropagateEnv(t *testing.T, src string) (*packages.Package, *ast.File, *dst.File) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatalf("failed to write src: %v", err)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	// Type check
	conf := types.Config{
		Importer: importer.Default(),
		Error:    func(err error) { t.Logf("Type check: %v", err) },
	}
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}
	pkgTypes, err := conf.Check("main", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}

	pkg := &packages.Package{
		Fset:      fset,
		Syntax:    []*ast.File{f},
		Types:     pkgTypes,
		TypesInfo: info,
		GoFiles:   []string{path},
	}

	// Decorate
	dec := decorator.NewDecorator(fset)
	dstFile, err := dec.DecorateFile(f)
	if err != nil {
		t.Fatalf("decorate failed: %v", err)
	}

	return pkg, f, dstFile
}

func renderDST(t *testing.T, n *dst.File) string {
	var buf bytes.Buffer
	if err := decorator.NewRestorer().Fprint(&buf, n); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	return buf.String()
}

func findCall(t *testing.T, f *ast.File, funName string) *ast.Ident {
	var target *ast.Ident
	ast.Inspect(f, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == funName {
				target = id
				return false
			}
		}
		return true
	})
	if target == nil {
		t.Fatalf("Call to %s not found", funName)
	}
	return target
}

func findDecl(t *testing.T, pkg *packages.Package, name string) *types.Func {
	obj := pkg.Types.Scope().Lookup(name)
	if obj == nil {
		t.Fatalf("func %s not found", name)
	}
	return obj.(*types.Func)
}

// --- Tests ---

func TestPropagateCallers_E2E(t *testing.T) {
	src := `package main
func Target() {}
func Caller() {
	Target()
}
func main() {
	Caller()
}
`
	pkg, _, _ := setupPropagateEnv(t, src)
	targetFunc := findDecl(t, pkg, "Target")

	updates, err := PropagateCallers([]*packages.Package{pkg}, targetFunc, "log-fatal")
	if err != nil {
		t.Fatalf("PropagateCallers failed: %v", err)
	}
	if updates != 2 { // Caller -> Target (1), Main -> Caller (1)
		t.Errorf("Expected 2 updates, got %d", updates)
	}
}

func TestProcessCallSite_Async_Go(t *testing.T) {
	src := `package main
func Target() {}
func Do() {
	go Target()
}
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	targetFunc := findDecl(t, pkg, "Target")
	callID := findCall(t, astFile, "Target")

	// Run process directly
	n, _, err := processCallSiteDST(pkg, astFile, dstFile, callID, targetFunc, "log-fatal")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Expected 1 update")
	}

	out := renderDST(t, dstFile)
	if !strings.Contains(out, "go func() {") {
		t.Error("Go statement not wrapped in closure")
	}
	if !strings.Contains(out, "log.Fatal") {
		t.Error("Missing terminal handler in go routine")
	}
}

func TestProcessCallSite_Async_Defer_LogFallback(t *testing.T) {
	src := `package main
func Target() {}
func main() {
	defer Target()
}
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	targetFunc := findDecl(t, pkg, "Target")
	callID := findCall(t, astFile, "Target")

	n, _, err := processCallSiteDST(pkg, astFile, dstFile, callID, targetFunc, "log-fatal")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Expected 1 update")
	}

	out := renderDST(t, dstFile)
	// Expect log fallback because Do() returns void
	expected := `defer func() {
		if err := Target(); err != nil {
			log.Printf("deferred error: %v", err)
		}
	}()`
	// Normalize for check
	if !strings.Contains(strings.ReplaceAll(out, "\t", ""), "log.Printf") {
		t.Errorf("Defer log fallback missing.\nGot:\n%s\nExpected:\n%s", out, expected)
	}
}

func TestProcessCallSite_Async_Defer_Capture(t *testing.T) {
	// Function ALREADY returns named error "e", so we should use errors.Join
	src := `package main
func Target() {}
func Do() (e error) {
	defer Target()
	return nil
}
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	targetFunc := findDecl(t, pkg, "Target")
	callID := findCall(t, astFile, "Target")

	n, _, err := processCallSiteDST(pkg, astFile, dstFile, callID, targetFunc, "log-fatal")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Expected 1 update")
	}

	out := renderDST(t, dstFile)
	if !strings.Contains(out, `e = errors.Join(e, Target())`) {
		t.Errorf("Defer capture missing.\nGot:\n%s", out)
	}
	if !strings.Contains(out, `import "errors"`) {
		t.Error("Errors import missing")
	}
}

func TestProcessCallSite_Async_Defer_Capture_Rename(t *testing.T) {
	// Function returns unnamed error. Needs rename to use capture.
	src := `package main
func Target() {}
func Do() error {
	defer Target()
	return nil
}
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	targetFunc := findDecl(t, pkg, "Target")
	callID := findCall(t, astFile, "Target")

	n, _, err := processCallSiteDST(pkg, astFile, dstFile, callID, targetFunc, "log-fatal")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Expected 1 update")
	}

	out := renderDST(t, dstFile)
	if !strings.Contains(out, `func Do() (err error)`) {
		t.Error("Signature naming update missing")
	}
	if !strings.Contains(out, `err = errors.Join(err, Target())`) {
		t.Errorf("Defer capture missing.\nGot:\n%s", out)
	}
}

func TestRefactorGoStmt_Strategies(t *testing.T) {
	tests := []struct {
		strategy string
		expect   string
	}{
		{"panic", "panic(err)"},
		{"os-exit", "os.Exit(1)"},
	}
	src := `package main
func T() {}
func main() { go T() }`

	for _, tt := range tests {
		t.Run(tt.strategy, func(t *testing.T) {
			pkg, astFile, dstFile := setupPropagateEnv(t, src)
			target := findDecl(t, pkg, "T")
			call := findCall(t, astFile, "T")
			_, _, err := processCallSiteDST(pkg, astFile, dstFile, call, target, tt.strategy)
			if err != nil {
				t.Fatal(err)
			}
			out := renderDST(t, dstFile)
			if !strings.Contains(out, tt.expect) {
				t.Errorf("Expected %s, got:\n%s", tt.expect, out)
			}
		})
	}
}

func TestProcessCallSite_TestHandler(t *testing.T) {
	src := `package main
import "testing"
func Target() {}
func TestFoo(t *testing.T) {
	Target()
}
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	target := findDecl(t, pkg, "Target")
	call := findCall(t, astFile, "Target")

	n, _, err := processCallSiteDST(pkg, astFile, dstFile, call, target, "log-fatal")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("Expected update")
	}
	out := renderDST(t, dstFile)
	if !strings.Contains(out, "t.Fatal(err)") {
		t.Errorf("Expected test handling t.Fatal(err), got:\n%s", out)
	}
}

func TestProcessCallSite_TestHelper(t *testing.T) {
	src := `package main
import "testing"
func Target() {}
func MyHelper(t *testing.T) {
	t.Helper()
	Target()
}
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	target := findDecl(t, pkg, "Target")
	call := findCall(t, astFile, "Target")

	n, _, err := processCallSiteDST(pkg, astFile, dstFile, call, target, "log-fatal")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("Expected update")
	}
	out := renderDST(t, dstFile)
	if !strings.Contains(out, "t.Fatal(err)") {
		t.Errorf("Expected helper handling t.Fatal(err), got:\n%s", out)
	}
}

func TestHandleEntryPoint(t *testing.T) {
	src := `package main
func X() {}
func main() {
	X()
}`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	var stmt ast.Stmt
	var call *ast.CallExpr
	ast.Inspect(astFile, func(n ast.Node) bool {
		if s, ok := n.(*ast.ExprStmt); ok {
			stmt = s
			call = s.X.(*ast.CallExpr)
		}
		return true
	})

	err := HandleEntryPoint(pkg, dstFile, call, stmt, "panic")
	if err != nil {
		t.Fatal(err)
	}
	out := renderDST(t, dstFile)
	if !strings.Contains(out, "panic(err)") {
		t.Error("HandleEntryPoint failed")
	}
}

func TestRefactor_AssignStmt(t *testing.T) {
	src := `package main
func Target() int { return 1 }
func main() {
	x := Target()
	_ = x
}
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	target := findDecl(t, pkg, "Target")
	call := findCall(t, astFile, "Target")

	// Target currently returns (int). We pretend it returns (int, error) via logic simulation
	// The helpers inject "err" into LHS and check.

	n, _, err := processCallSiteDST(pkg, astFile, dstFile, call, target, "panic")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("Expected update")
	}
	out := renderDST(t, dstFile)
	if !strings.Contains(out, "x, err := Target()") {
		t.Errorf("Assignment not updated. Got:\n%s", out)
	}
	if !strings.Contains(out, "panic(err)") {
		t.Error("Check not added")
	}
}

// --- Internal Logic & Edge Case Tests ---

func TestMapAstToDst_Advanced(t *testing.T) {
	src := `package main
type S struct { F int }
var G = []S{{F:1}}
func main() {
	if true {
	  _ = G[0]
	}
}
`
	_, astFile, dstFile := setupPropagateEnv(t, src)

	// Target: G[0] (IndexExpr)
	var target ast.Node
	ast.Inspect(astFile, func(n ast.Node) bool {
		if idx, ok := n.(*ast.IndexExpr); ok {
			target = idx
		}
		return true
	})

	dstNode, parent := mapAstToDst(astFile, dstFile, target)
	if dstNode == nil {
		t.Fatal("Failed to map index expr")
	}
	if _, ok := dstNode.(*dst.IndexExpr); !ok {
		t.Errorf("Mapped wrong type: %T", dstNode)
	}
	if parent == nil {
		t.Error("Parent missing")
	}
}

func TestMapAstToDst_Failures(t *testing.T) {
	// 1. Mismatch Root
	src := "package main"
	fset := token.NewFileSet()
	f1, _ := parser.ParseFile(fset, "a.go", src, 0)
	f2, _ := parser.ParseFile(fset, "b.go", src, 0) // Different file
	dec := decorator.NewDecorator(fset)
	d1, _ := dec.DecorateFile(f1)

	_, _ = mapAstToDst(f1, d1, f2) // f2 is not in f1 path

	// 2. Node not found
	// We pass a node that isn't in f1 at all
	orphan := &ast.Ident{Name: "Orphan"}
	node, _ := mapAstToDst(f1, d1, orphan)
	if node != nil {
		t.Error("Expected nil for orphan node")
	}
}

func TestHelpers(t *testing.T) {
	// IsEntryPoint
	pkgMain := types.NewPackage("main", "main")
	pkgOther := types.NewPackage("other", "other")
	mainFunc := types.NewFunc(token.NoPos, pkgMain, "main", nil)
	initFunc := types.NewFunc(token.NoPos, pkgOther, "init", nil)
	otherFunc := types.NewFunc(token.NoPos, pkgMain, "foo", nil)

	if !IsEntryPoint(mainFunc) {
		t.Error("main should be entry point")
	}
	if !IsEntryPoint(initFunc) {
		t.Error("init should be entry point")
	}
	if IsEntryPoint(otherFunc) {
		t.Error("foo should not be entry point")
	}

	// canReturnError
	errType := types.Universe.Lookup("error").Type()
	sigErr := types.NewSignature(nil, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", errType)), false)
	sigVoid := types.NewSignature(nil, nil, nil, false)

	if !canReturnError(sigErr) {
		t.Error("sigErr should return error")
	}
	if canReturnError(sigVoid) {
		t.Error("sigVoid should not return error")
	}
	if canReturnError(nil) {
		t.Error("nil sig should not return error")
	}
}

func TestDetermineTraversalStep_Errors(t *testing.T) {
	// Child not found
	parent := &ast.BlockStmt{} // Just an empty struct
	child := &ast.Ident{}
	_, err := determineTraversalStep(parent, child)
	if err == nil {
		t.Error("Expected error for child not found")
	}
}

func TestApplyTraversalStep_Errors(t *testing.T) {
	node := &dst.BlockStmt{} // Has List []Stmt
	step := tStep{FieldName: "List", Index: 100}

	_, err := applyTraversalStep(node, step)
	if err == nil || !strings.Contains(err.Error(), "out of bounds") {
		t.Errorf("Expected OOB error, got %v", err)
	}

	stepInvalid := tStep{FieldName: "NonExistent", Index: 0}
	_, err = applyTraversalStep(node, stepInvalid)
	if err == nil {
		t.Error("Expected field error")
	}

	// Index on non-slice
	node2 := &dst.ExprStmt{X: &dst.Ident{}} // X is not slice
	stepBadType := tStep{FieldName: "X", Index: 0}
	_, err = applyTraversalStep(node2, stepBadType)
	if err == nil {
		t.Error("Expected error accessing non-slice as slice")
	}
}

func TestProcessCallSiteDST_Errors(t *testing.T) {
	// 1. Inputs nil - Explicit call to verify nil check behavior
	_, _, err := processCallSiteDST(nil, nil, nil, &ast.Ident{}, nil, "")
	if err == nil || !strings.Contains(err.Error(), "nil") {
		t.Error("Expected error for nil file inputs")
	}

	// 2. Call not found in file (orphan ID)
	src := "package p\nfunc f() {}"
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	orphan := &ast.Ident{NamePos: token.NoPos}
	n, _, _ := processCallSiteDST(pkg, astFile, dstFile, orphan, nil, "")
	if n != 0 {
		t.Error("Should return 0 for orphan")
	}
}

func TestEnsureImportDST(t *testing.T) {
	f := &dst.File{Name: dst.NewIdent("p")}
	ensureImportDST(f, "fmt")
	if len(f.Decls) != 1 {
		t.Fatal("Import not added")
	}
	ensureImportDST(f, "fmt")
	if len(f.Decls) != 1 {
		t.Fatal("Duplicate import added")
	}
	imp := f.Decls[0].(*dst.GenDecl).Specs[0].(*dst.ImportSpec)
	if imp.Path.Value != `"fmt"` {
		t.Error("Wrong import value")
	}
}

func TestRefactorUnsupported(t *testing.T) {
	// Try to refactor a call inside an unsupported statement type
	src := `package main
func Target() int { return 1 }
func main() {
    if Target() == 1 {}
}
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	// We rely on manually finding items to force logic
	var targetFunc *types.Func
	if obj := pkg.Types.Scope().Lookup("Target"); obj != nil {
		targetFunc = obj.(*types.Func)
	}
	// Ident
	var callID *ast.Ident
	ast.Inspect(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "Target" {
				callID = id
			}
		}
		return true
	})

	_, _, err := processCallSiteDST(pkg, astFile, dstFile, callID, targetFunc, "")
	if err == nil {
		t.Error("Expected error for unsupported statement type (IfStmt)")
	} else if !strings.Contains(err.Error(), "unsupported statement type") {
		t.Errorf("Unexpected error: %v", err)
	}
}

func TestPropagateCallers_NilTarget(t *testing.T) {
	_, err := PropagateCallers(nil, nil, "")
	if err == nil {
		t.Error("Expected error for nil target")
	}
}
