package rewrite

import (
	"bytes"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/analysis"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/refactor"
	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
	"golang.org/x/tools/go/packages"
)

// Helper to setup everything
func setupInjectorTest(t *testing.T, src string) (*Injector, *dst.File, *ast.File) {
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "main.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parser: %v", err)
	}

	conf := types.Config{Importer: importer.Default()}
	info := &types.Info{
		Types:  make(map[ast.Expr]types.TypeAndValue),
		Defs:   make(map[*ast.Ident]types.Object),
		Uses:   make(map[*ast.Ident]types.Object),
		Scopes: make(map[ast.Node]*types.Scope),
	}
	pkgTypes, err := conf.Check("main", fset, []*ast.File{astFile}, info)
	if err != nil {
		t.Fatalf("checker: %v", err)
	}

	pkg := &packages.Package{
		Fset:      fset,
		Types:     pkgTypes,
		TypesInfo: info,
		Syntax:    []*ast.File{astFile},
	}

	dstFile, err := decorator.NewDecorator(fset).DecorateFile(astFile)
	if err != nil {
		t.Fatalf("decorate: %v", err)
	}

	// Updated to include retainPanics=false
	injector := NewInjector(pkg, "", "", false)
	return injector, dstFile, astFile
}

// findPoint helper
func findPoint(t *testing.T, f *ast.File, substr string, isCond bool) analysis.InjectionPoint {
	var pt analysis.InjectionPoint
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}

		// Condition Logic: Target IfStmt or SwitchStmt
		if isCond {
			if s, ok := n.(*ast.IfStmt); ok {
				if callStmtContains(s.Cond, substr) {
					pt = analysis.InjectionPoint{Stmt: s, File: f, Pos: s.Pos()}
					pt.Call = findCallInNode(s.Cond, substr)
					found = true
					return false
				}
			}
			if s, ok := n.(*ast.SwitchStmt); ok {
				if s.Tag != nil && callStmtContains(s.Tag, substr) {
					pt = analysis.InjectionPoint{Stmt: s, File: f, Pos: s.Pos()}
					pt.Call = findCallInNode(s.Tag, substr)
					found = true
					return false
				}
			}
			// Do not return true here; fall through might check siblings, but for IsCond we handle stmts here.
			return true
		}

		// Std Logic: We specifically want the AssignStmt inside Init, or ExprStmt.
		// If we visit SwitchStmt or IfStmt, we should NOT match it unless we are in cond mode.
		// So we skip them to recurse into children (Init).
		if _, ok := n.(*ast.IfStmt); ok {
			return true
		}
		if _, ok := n.(*ast.SwitchStmt); ok {
			return true
		}

		if s, ok := n.(ast.Stmt); ok {
			if _, isBlock := s.(*ast.BlockStmt); isBlock {
				return true
			}

			if callStmtContains(s, substr) {
				pt = analysis.InjectionPoint{
					Stmt: s,
					File: f,
					Pos:  s.Pos(),
				}
				pt.Call = findCallInNode(s, substr)
				if a, ok := s.(*ast.AssignStmt); ok {
					pt.Assign = a
				}
				found = true
				return false
			}
		}
		return true
	})
	if !found {
		t.Fatalf("point not found for %q", substr)
	}
	return pt
}

func callStmtContains(n ast.Node, sub string) bool {
	match := false
	if n == nil {
		return false
	}
	ast.Inspect(n, func(node ast.Node) bool {
		if id, ok := node.(*ast.Ident); ok {
			if strings.Contains(id.Name, sub) {
				match = true
			}
		}
		return true
	})
	return match
}

func findCallInNode(n ast.Node, sub string) *ast.CallExpr {
	var call *ast.CallExpr
	ast.Inspect(n, func(node ast.Node) bool {
		if c, ok := node.(*ast.CallExpr); ok {
			if callStmtContains(c.Fun, sub) {
				call = c
				return false
			}
		}
		return true
	})
	return call
}

func render(t *testing.T, f *dst.File) string {
	var buf bytes.Buffer
	if err := decorator.NewRestorer().Fprint(&buf, f); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestRewriteFile_Comments(t *testing.T) {
	src := `package main

func fail() error { return nil }

func run() error { 
	// Pre-comment
	fail() // Inline-comment
	// Post-comment
	return nil
} 
`
	injector, dstFile, astFile := setupInjectorTest(t, src)
	pt := findPoint(t, astFile, "fail", false)

	changed, err := injector.RewriteFile(dstFile, astFile, []analysis.InjectionPoint{pt})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("Expected change")
	}

	out := render(t, dstFile)

	// Check Trivia Placement
	if !strings.Contains(out, "// Pre-comment") {
		t.Error("Pre-comment missing")
	}
	if !strings.Contains(out, "if err := fail();") {
		t.Error("Injection failed")
	}
	if !strings.Contains(out, "// Inline-comment") {
		t.Error("Inline comment lost")
	}
	if !strings.Contains(out, "// Post-comment") {
		t.Error("Post comment lost")
	}
}

func TestRewriteFile_GoStmt(t *testing.T) {
	src := `package main
func task() error { return nil } 
func main() { 
	go task() 
} 
`
	injector, dstFile, astFile := setupInjectorTest(t, src)
	pt := findPoint(t, astFile, "task", false)

	changed, err := injector.RewriteFile(dstFile, astFile, []analysis.InjectionPoint{pt})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("Expected change")
	}

	out := render(t, dstFile)
	if !strings.Contains(out, "go func() {") {
		t.Errorf("Go rewrite failed: %s", out)
	}
	if !strings.Contains(out, "log.Fatal") {
		t.Error("Default handler missing")
	}
}

func TestRewriteFile_IfInitLift(t *testing.T) {
	src := `package main
func fail() error { return nil } 
func run() error { 
 if x := fail(); x != nil { 
 	return nil
 } 
 return nil
} 
`
	injector, dstFile, astFile := setupInjectorTest(t, src)
	pt := findPoint(t, astFile, "fail", false)

	changed, err := injector.RewriteFile(dstFile, astFile, []analysis.InjectionPoint{pt})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("Expected change")
	}

	out := render(t, dstFile)
	norm := strings.Join(strings.Fields(out), " ")

	// Since fail() returns 1 error, and x := fail(), x IS the error.
	// The variable resolution logic keeps 'x'.
	// So we expect: x := fail(); if x != nil (ret); if x != nil { ... }
	expected := "{ x := fail() if x != nil { return x } if x != nil {"
	if !strings.Contains(norm, expected) {
		t.Errorf("Control Lift failed. Got:\n%s", out)
	}
}

func TestRewriteFile_IfCondLift(t *testing.T) {
	src := `package main
func fail() error { return nil } 
func run() error { 
 if fail() != nil { 
 	return nil
 } 
 return nil
} 
`
	injector, dstFile, astFile := setupInjectorTest(t, src)
	pt := findPoint(t, astFile, "fail", true)

	changed, err := injector.RewriteFile(dstFile, astFile, []analysis.InjectionPoint{pt})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("Expected change")
	}

	out := render(t, dstFile)
	// fail() return error only.
	// Expected: err := fail(); if err!=nil { return err }; if err != nil { ... }
	if !strings.Contains(out, "err := fail()") {
		t.Errorf("Condition Lift failed. Got:\n%s", out)
	}
	// Replacement logic might use 'err' in place of 'fail()' in condition
	// so 'if err != nil'
	if !strings.Contains(out, "if err != nil {") {
		t.Errorf("If condition not updated. Got:\n%s", out)
	}
}

func TestRewriteFile_SwitchInitLift(t *testing.T) {
	src := `package main
func fail() error { return nil } 
func run() error { 
 switch x := fail(); x { 
 default: 
 } 
 return nil
} 
`
	injector, dstFile, astFile := setupInjectorTest(t, src)
	pt := findPoint(t, astFile, "fail", false)

	changed, err := injector.RewriteFile(dstFile, astFile, []analysis.InjectionPoint{pt})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("Expected change")
	}

	out := render(t, dstFile)
	norm := strings.Join(strings.Fields(out), " ")

	expected := "{ x := fail() if x != nil { return x } switch x {"
	if !strings.Contains(norm, expected) {
		t.Errorf("Switch Lift failed. Got:\n%s", out)
	}
}

func TestLogFallback(t *testing.T) {
	src := `package main
func task() error { return nil } 
func main() { 
	task() 
} 
`
	injector, dstFile, astFile := setupInjectorTest(t, src)
	pt := findPoint(t, astFile, "task", false)

	changed, err := injector.LogFallback(dstFile, astFile, pt)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("Expected change")
	}

	out := render(t, dstFile)
	if !strings.Contains(out, `log.Printf("ignored error in task: %v", err)`) {
		t.Errorf("Log fallback failed: %s", out)
	}
	if !strings.Contains(out, `import "log"`) {
		t.Error("Import log missing")
	}
}

func TestRewriteFile_Passthrough_Unique(t *testing.T) {
	// Source code with VALID syntax (void functions) which we will PATCH during test to have 'error' return.
	// This simulates the behavior of the main runner iterating levels.
	src := `package main
func sub() error { return nil }

func shouldOptimize() {
	sub()
}

func noOptimizeMismatch() (int, error) {
	sub()
	return 0, nil
}

func noOptimizeNotTail() {
	sub()
	_ = 1
}
`
	injector, dstFile, astFile := setupInjectorTest(t, src)

	// Helper to patch the type info for Level 1 simulation
	applyPatch := func(name string) {
		var decl *ast.FuncDecl
		for _, d := range astFile.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name {
				decl = fd
				break
			}
		}
		if decl == nil {
			t.Fatalf("Decl %s not found", name)
		}
		// Update TypeInfo.Defs and Uses to point to a new signature with 'error' appended
		if err := refactor.PatchSignature(injector.Pkg.TypesInfo, decl, injector.Pkg.Types); err != nil {
			t.Fatalf("PatchSignature failed: %v", err)
		}
	}

	// A: Should Optimize
	// 1. Patch 'shouldOptimize' signature to return 'error' (TypesInfo only)
	applyPatch("shouldOptimize")

	// 2. Run Rewrite
	pt1 := findPointCtx(t, astFile, "shouldOptimize", "sub")
	changed, err := injector.RewriteFile(dstFile, astFile, []analysis.InjectionPoint{pt1})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("Expected change for pt1")
	}
	out := render(t, dstFile)
	// Check that body was replaced with 'return sub()'
	if !strings.Contains(out, "return sub()") {
		t.Errorf("Optimization failed. Expected passthrough return. Got:\n%s", out)
	}

	// B: Mismatch - Standard Rewrite
	// noOptimizeMismatch already returns (int, error), sub returns (error). No patch needed.
	pt2 := findPointCtx(t, astFile, "noOptimizeMismatch", "sub")
	changed, _ = injector.RewriteFile(dstFile, astFile, []analysis.InjectionPoint{pt2})
	if !changed {
		t.Error("Expected change for pt2")
	}
	out = render(t, dstFile)
	// Expect standard rewrite because signatures don't match
	if strings.Contains(out, "return sub()\n\treturn 0, nil") {
		t.Error("Mismatch incorrectly optimized")
	}
	if !strings.Contains(out, "if err := sub(); err != nil") {
		t.Error("Standard rewrite missing for Mismatch")
	}

	// C: Not Tail - Standard Rewrite
	// 1. Patch 'noOptimizeNotTail' to return 'error'
	applyPatch("noOptimizeNotTail")

	pt3 := findPointCtx(t, astFile, "noOptimizeNotTail", "sub")
	changed, _ = injector.RewriteFile(dstFile, astFile, []analysis.InjectionPoint{pt3})
	out = render(t, dstFile)
	// Expect standard rewrite because sub() is not the last statement in block
	if strings.Contains(out, "return sub()\n\t_ = 1") {
		t.Error("Non-tail incorrectly optimized")
	}
	if !strings.Contains(out, "if err := sub(); err != nil") {
		t.Error("Standard rewrite missing for NotTail")
	}
}

func findPointCtx(t *testing.T, f *ast.File, funcName, callSub string) analysis.InjectionPoint {
	var pt analysis.InjectionPoint
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		if fn, ok := n.(*ast.FuncDecl); ok && fn.Name.Name == funcName {
			// Search inside this func
			ast.Inspect(fn.Body, func(bn ast.Node) bool {
				if found {
					return false
				}
				if s, ok := bn.(ast.Stmt); ok {
					if _, isBlk := s.(*ast.BlockStmt); !isBlk {
						if callStmtContains(s, callSub) {
							pt = analysis.InjectionPoint{Stmt: s, File: f, Pos: s.Pos()}
							pt.Call = findCallInNode(s, callSub)
							found = true
						}
					}
				}
				return true
			})
			return false
		}
		return true
	})
	if !found {
		t.Fatalf("Point %s -> %s not found", funcName, callSub)
	}
	return pt
}
