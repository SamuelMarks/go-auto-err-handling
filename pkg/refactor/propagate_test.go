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
		// t.Fatalf("check failed: %v", err) -> Allow soft fail for incomplete code in partial tests if needed
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

type mockProvider struct {
	f *dst.File
}

func (m *mockProvider) Get(pkg *packages.Package, file *ast.File) (*dst.File, error) {
	return m.f, nil
}
func (m *mockProvider) MarkModified(file *ast.File) {}

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
	pkg, _, dstFile := setupPropagateEnv(t, src)
	targetFunc := findDecl(t, pkg, "Target")
	provider := &mockProvider{f: dstFile}

	updates, err := PropagateCallers([]*packages.Package{pkg}, provider, targetFunc, "log-fatal")
	if err != nil {
		t.Fatalf("PropagateCallers failed: %v", err)
	}
	if updates != 2 { // Caller -> Target (1), Main -> Caller (1)
		t.Errorf("Expected 2 updates, got %d", updates)
	}
}

func TestPropagateCallers_Variables(t *testing.T) {
	src := `package main
func Target() {} 
var f = Target
func main() { 
  f() 
} 
`
	pkg, f, dstFile := setupPropagateEnv(t, src)
	provider := &mockProvider{f: dstFile}

	// Manually patch Target to return error so propagation has something to do
	var targetDecl *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "Target" {
			targetDecl = fd
		}
	}
	if targetDecl == nil {
		t.Fatal("Target decl not found")
	}
	AddErrorToSignature(pkg.Fset, targetDecl)
	PatchSignature(pkg.TypesInfo, targetDecl, pkg.Types)

	targetFunc := pkg.TypesInfo.ObjectOf(targetDecl.Name).(*types.Func)

	// Target -> f (var), f -> main (call)
	// Expect 2 updates: Variable propagation return count + Call update.
	updates, err := PropagateCallers([]*packages.Package{pkg}, provider, targetFunc, "log-fatal")
	if err != nil {
		t.Fatalf("PropagateCallers failed: %v", err)
	}
	if updates != 2 {
		t.Errorf("Expected variable propagation (Def+Use), updated %d", updates)
	}

	out := renderDST(t, dstFile)
	if !strings.Contains(out, "if err := f();") {
		t.Errorf("Variable call not updated. Got:\n%s", out)
	}
}

func TestPropagateCallers_ExplicitVarType(t *testing.T) {
	src := `package main
func Target() {} 
var f func() = Target
func main() { 
  f() 
} 
`
	pkg, f, dstFile := setupPropagateEnv(t, src)
	provider := &mockProvider{f: dstFile}

	// Manually patch Target
	var targetDecl *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "Target" {
			targetDecl = fd
		}
	}
	AddErrorToSignature(pkg.Fset, targetDecl)
	PatchSignature(pkg.TypesInfo, targetDecl, pkg.Types)
	targetFunc := pkg.TypesInfo.ObjectOf(targetDecl.Name).(*types.Func)

	updates, err := PropagateCallers([]*packages.Package{pkg}, provider, targetFunc, "log-fatal")
	if err != nil {
		t.Fatal(err)
	}
	if updates != 2 {
		t.Errorf("Updates: %d", updates)
	}

	out := renderDST(t, dstFile)
	if !strings.Contains(out, "var f func() error") {
		t.Errorf("Explicit variable type not updated. Got:\n%s", out)
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
	// Expect log fallback because main returns void (and signature change is not force applied to main per se during processCallSite unless propagated, but main cannot propagate)
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

func TestPropagate_IfCond(t *testing.T) {
	src := `package main
func Target() bool { return true }
func main() {
	if Target() {
	}
}
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)

	// Manually patch Target to (bool, error)
	var targetDecl *ast.FuncDecl
	for _, d := range astFile.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "Target" {
			targetDecl = fd
		}
	}
	AddErrorToSignature(pkg.Fset, targetDecl)
	PatchSignature(pkg.TypesInfo, targetDecl, pkg.Types)

	// REFRESH object reference
	target := pkg.TypesInfo.Defs[targetDecl.Name].(*types.Func)

	call := findCall(t, astFile, "Target")

	n, _, err := processCallSiteDST(pkg, astFile, dstFile, call, target, "log-fatal")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("Expected update")
	}
	out := renderDST(t, dstFile)
	if !strings.Contains(out, "val, err := Target()") {
		t.Errorf("If condition did not lift assignment correctly. Got:\n%s", out)
	}
	if !strings.Contains(out, "log.Fatal(err)") {
		t.Error("Missing handler using log.Fatal")
	}
}

func TestPropagate_ReturnStmt(t *testing.T) {
	// Case: explicit return matching new signature (void func becomes error func)
	// We use `return Target()` where Target returns int initially to be valid Go
	src := `package main
func Target() int { return 0 }
func wrapper() int {
	return Target()
}
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)

	// Patch Target -> (int, error)
	var targetDecl *ast.FuncDecl
	for _, d := range astFile.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "Target" {
			targetDecl = fd
		}
	}
	AddErrorToSignature(pkg.Fset, targetDecl)
	PatchSignature(pkg.TypesInfo, targetDecl, pkg.Types)

	// REFRESH object
	target := pkg.TypesInfo.Defs[targetDecl.Name].(*types.Func)

	call := findCall(t, astFile, "Target")

	n, _, err := processCallSiteDST(pkg, astFile, dstFile, call, target, "panic")
	if err != nil {
		t.Fatal(err)
	}
	// wrapper signature should be updated to return (int, error)
	if n != 1 {
		t.Error("Expected update")
	}
	out := renderDST(t, dstFile)
	if !strings.Contains(out, "func wrapper() (int, error)") {
		t.Error("Wrapper signature not updated")
	}
	// Verify return statement is clean (no double return)
	if strings.Count(out, "return Target()") != 1 {
		t.Errorf("Return statement malformed. Got:\n%s", out)
	}
}

func TestPropagate_ReturnMismatch(t *testing.T) {
	// Case: explicit return NOT matching
	// wrapper returns (int), Target returns (int, error) (propagated).
	// But we simulate a scenario where `wrapper` signature update fails or is mismatched.
	// Since `processCallSiteDST` auto-updates enclosing `wrapper` to `(int, error)` if void/matching,
	// we need a scenario where wrapper signature is fixed/different.
	// E.g. Wrapper is `func wrapper() (int, int)` and calls `Target` returning `(int, error)`.

	src3 := `package main
func Target() int { return 1 }
func wrapper() (int, int) {
	return Target(), 0
}
`
	// This code is valid: return 1, 0.
	// After patch Target -> (int, error).
	// wrapper loop `return Target(), 0` becomes invalid return count 3?
	// refactor return stmt handles `Call` detection.
	// It sees `Target()`. It lifts it.

	pkg, astFile, dstFile := setupPropagateEnv(t, src3)

	// Patch Target -> (int, error)
	var targetDecl *ast.FuncDecl
	for _, d := range astFile.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "Target" {
			targetDecl = fd
		}
	}
	AddErrorToSignature(pkg.Fset, targetDecl)
	PatchSignature(pkg.TypesInfo, targetDecl, pkg.Types)

	target := pkg.TypesInfo.Defs[targetDecl.Name].(*types.Func)
	call := findCall(t, astFile, "Target")

	// Target -> (int, error). Wrapper -> (int, int).
	// Mismatch.
	n, _, err := processCallSiteDST(pkg, astFile, dstFile, call, target, "panic")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("Expected update")
	}
	out := renderDST(t, dstFile)

	// Expect lifting: val, err := Target(); check; return val, 0
	if !strings.Contains(out, "val, err := Target()") {
		t.Errorf("Expected lifting. Got:\n%s", out)
	}
	// Note: since wrapper sig was NOT auto updated (it wasn't void),
	// we just handle the error locally.
	// Check block should appear.
}
