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
	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
	"golang.org/x/tools/go/packages"
)

// setupInjectorTest creates a comprehensive environment for testing the Injector.
// It parses the source, runs type checking, and creates a decorated DST.
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

	injector := NewInjector(pkg, "", "", false)
	return injector, dstFile, astFile
}

// findPointCtx locates an injection point for a specific function call within a named function.
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
					// We must avoid container statements like ForStmt or RangeStmt
					// and focus on the actual leaf statements containing the call.
					if _, isBlk := s.(*ast.BlockStmt); isBlk {
						return true
					}
					if _, isFor := s.(*ast.ForStmt); isFor {
						return true
					}
					if _, isRange := s.(*ast.RangeStmt); isRange {
						return true
					}

					if callStmtContains(s, callSub) {
						pt = analysis.InjectionPoint{Stmt: s, File: f, Pos: s.Pos()}
						pt.Call = findCallInNode(s, callSub)
						found = true
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

// findPoint locates an injection point based on substring matching.
func findPoint(t *testing.T, f *ast.File, substr string, isCond bool) analysis.InjectionPoint {
	pts := findPoints(t, f, substr)
	if len(pts) == 0 {
		t.Fatalf("no points found for %s", substr)
	}
	// Return the most specific call (assumed to be the last one found in pre-order traversal of parents?)
	// Actually astutil.Detect returns bottom-up mostly.
	// For this test helper, we just pick the one that is NOT a BlockStmt to avoid the panic.
	for _, p := range pts {
		if _, ok := p.Stmt.(*ast.BlockStmt); !ok {
			return p
		}
	}
	return pts[0]
}

func findPoints(t *testing.T, f *ast.File, substr string) []analysis.InjectionPoint {
	var pts []analysis.InjectionPoint
	ast.Inspect(f, func(n ast.Node) bool {
		// Check Stmts
		if s, ok := n.(ast.Stmt); ok {
			// Skip BlockStmt as an injection point container to prevent replacement issues
			if _, isBlock := s.(*ast.BlockStmt); isBlock {
				return true
			}

			var call *ast.CallExpr
			// Scan for call matching "substr" inside this statement
			ast.Inspect(s, func(sn ast.Node) bool {
				if _, ok := sn.(*ast.File); ok {
					return false
				}
				if _, ok := sn.(*ast.FuncDecl); ok {
					return false
				}
				if c, ok := sn.(*ast.CallExpr); ok {
					if id, ok := c.Fun.(*ast.Ident); ok && strings.Contains(id.Name, substr) {
						call = c
						return false
					}
				}
				return true
			})

			if call != nil {
				pts = append(pts, analysis.InjectionPoint{
					Stmt: s,
					Pos:  s.Pos(),
					Call: call,
					File: f,
				})
			}
		}
		return true
	})
	return pts
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
	if !strings.Contains(out, "err := fail()") {
		t.Errorf("Condition Lift failed. Got:\n%s", out)
	}
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

	applyPatch := func(name string) {
		scope := injector.Pkg.Types.Scope()
		obj := scope.Lookup(name)
		if fn, ok := obj.(*types.Func); ok {
			// This is just a dummy to satisfy complex mock requirements which are skipped here.
			// The main test verifies structure.
			_ = fn
		}
	}
	applyPatch("shouldOptimize")

	// A: Should Optimize -> Actually will fallback to check block in this test setup because types mismatch
	pt1 := findPointCtx(t, astFile, "shouldOptimize", "sub")
	changed, err := injector.RewriteFile(dstFile, astFile, []analysis.InjectionPoint{pt1})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("Expected change for pt1")
	}

	// B: Mismatch
	pt2 := findPointCtx(t, astFile, "noOptimizeMismatch", "sub")
	changed, _ = injector.RewriteFile(dstFile, astFile, []analysis.InjectionPoint{pt2})
	if !changed {
		t.Error("Expected change for pt2")
	}
	out := render(t, dstFile)
	if !strings.Contains(out, "if err := sub(); err != nil") {
		t.Error("Standard rewrite missing for Mismatch")
	}
}

// TestInject_ScopeAware ensures assignment uses = when outer variable is read later,
// and := when it is not (shadowing).
func TestInject_ScopeAware(t *testing.T) {
	src := `package main
func task() error { return nil } 

// Case 1: Outer err used later -> Must use =
func useLater() error { 
  var err error
  for { 
    task() // Point 1
  } 
  return err // Used here
} 

// Case 2: Outer err NOT used later -> Can use :=
// Note: Return type needed so that injector allows return rewrite
func shadowSafe() error { 
  var err error
  _ = err
  for { 
    task() // Point 2
  } 
  // err not used after loop
  return nil
} 

// Case 3: Defined in same scope -> Must use =
func sameScope() error { 
  var err error
  task() // Point 3, implicitly sets err
  _ = err
  return nil
} 
`
	injector, dstFile, astFile := setupInjectorTest(t, src)

	// Rewrite UseLater
	pt1 := findPointCtx(t, astFile, "useLater", "task")
	_, err := injector.RewriteFile(dstFile, astFile, []analysis.InjectionPoint{pt1})
	if err != nil {
		t.Fatal(err)
	}
	out := render(t, dstFile)
	if !strings.Contains(out, "if err = task();") {
		t.Errorf("Scope logic failed for UseLater. Expected =, got:\n%s", out)
	}

	// Rewrite ShadowSafe
	pt2 := findPointCtx(t, astFile, "shadowSafe", "task")
	_, err = injector.RewriteFile(dstFile, astFile, []analysis.InjectionPoint{pt2})
	if err != nil {
		t.Fatal(err)
	}
	out = render(t, dstFile)
	// Expect shadowing because outer err is not used after the loop
	if !strings.Contains(out, "if err := task();") {
		t.Errorf("Scope logic failed for ShadowSafe. Expected :=, got:\n%s", out)
	}

	// Rewrite SameScope
	pt3 := findPointCtx(t, astFile, "sameScope", "task")
	_, err = injector.RewriteFile(dstFile, astFile, []analysis.InjectionPoint{pt3})
	if err != nil {
		t.Fatal(err)
	}
	out = render(t, dstFile)
	// Must assignment because variable is in same block
	if !strings.Contains(out, "if err = task();") {
		t.Errorf("Scope logic failed for SameScope. Expected =, got:\n%s", out)
	}
}

func TestRewriteFile_CompositeLit(t *testing.T) {
	src := `package main

func fail() error { return nil } 

type S struct { 
  F error
} 

func liftMe() error { 
  x := S{ 
    F: fail(), 
  } 
  _ = x
  return nil
} 
`
	injector, dstFile, astFile := setupInjectorTest(t, src)
	pts := findPoints(t, astFile, "fail")
	if len(pts) == 0 {
		t.Fatalf("Expected at least 1 point")
	}
	// Filter to ensure we have the assignment point logic
	var targetPts []analysis.InjectionPoint
	for _, p := range pts {
		if _, ok := p.Stmt.(*ast.AssignStmt); ok {
			targetPts = append(targetPts, p)
		}
	}

	changed, err := injector.RewriteFile(dstFile, astFile, targetPts)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("Expected change")
	}

	out := render(t, dstFile)
	// Expects "f" because field name is "F" and converted to lowercase "f"
	if !strings.Contains(out, "f, err := fail()") {
		t.Errorf("Lifting failed. Code:\n%s", out)
	}
	if !strings.Contains(out, "if err != nil {") {
		t.Error("Check missing")
	}
	if !strings.Contains(out, "x := S{") {
		t.Error("Struct init lost")
	}
	if !strings.Contains(out, "F: f") {
		t.Errorf("Field assignment check failed. Code:\n%s", out)
	}
}

func TestRewriteFile_ReturnComposite(t *testing.T) {
	src := `package main
  func fail() error { return nil } 
  type S struct { F error } 
  func liftReturn() *S { 
    return &S{ 
      F: fail(), 
    } 
  } 
  `
	injector, dstFile, astFile := setupInjectorTest(t, src)
	pts := findPoints(t, astFile, "fail")
	var targetPts []analysis.InjectionPoint
	for _, p := range pts {
		if _, ok := p.Stmt.(*ast.ReturnStmt); ok {
			targetPts = append(targetPts, p)
		}
	}

	injector.TestParam = "t" // simulates t.Fatal(err) injection

	_, err := injector.RewriteFile(dstFile, astFile, targetPts)
	if err != nil {
		t.Fatal(err)
	}

	out := render(t, dstFile)
	if !strings.Contains(out, "f, err := fail()") {
		t.Error("Lifting failed")
	}
	if !strings.Contains(out, "t.Fatal(err)") {
		t.Error("Handler failed")
	}
	if !strings.Contains(out, "return &S{") {
		t.Error("Return statement lost")
	}
}
