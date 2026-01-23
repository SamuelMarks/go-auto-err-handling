package rewrite

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/importer"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/analysis"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/imports"
)

// NOTE: Creating a full *packages.Package with TypesInfo from scratch manually for tests is verbose.
// We use a small helper to parse and typecheck source string.

func parseAndCheck(t *testing.T, source string) (*ast.File, *packages.Package) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", source, parser.ParseComments)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	// Run type checker to populate TypeInfo
	conf := types.Config{
		Importer: importer.Default(), // Necessary to resolve stdlib imports like "errors"
		Error: func(err error) {
			t.Logf("Type check error: %v", err)
		},
	}
	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Scopes:     make(map[ast.Node]*types.Scope),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}

	pkg, err := conf.Check("main", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("Type check error: %v", err)
	}

	// Wrap in packages.Package
	pack := &packages.Package{
		PkgPath:         "main",
		Fset:            fset,
		Syntax:          []*ast.File{f},
		Types:           pkg,
		TypesInfo:       info,
		GoFiles:         []string{"main.go"},
		CompiledGoFiles: []string{"main.go"},
	}

	return f, pack
}

// findStmt finds the first statement matching a substring in the source code.
// This mocks the analysis phase finding.
func findStmt(file *ast.File, fset *token.FileSet, match string) (ast.Stmt, *ast.CallExpr) {
	var targetStmt ast.Stmt
	var targetCall *ast.CallExpr

	ast.Inspect(file, func(n ast.Node) bool {
		if targetStmt != nil {
			return false
		}

		if stmt, ok := n.(ast.Stmt); ok {
			switch stmt.(type) {
			case *ast.BlockStmt, *ast.IfStmt, *ast.ForStmt, *ast.SwitchStmt, *ast.CaseClause:
				return true
			}

			var buf bytes.Buffer
			printer.Fprint(&buf, fset, stmt)
			if strings.Contains(buf.String(), match) && targetStmt == nil {
				targetStmt = stmt
				ast.Inspect(stmt, func(sn ast.Node) bool {
					if c, ok := sn.(*ast.CallExpr); ok {
						targetCall = c
						return false
					}
					return true
				})
				return false
			}
		}
		return true
	})
	return targetStmt, targetCall
}

// normalizeSpace compacts whitespace to ensure robust comparison against generated code.
func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// formatCode helper that uses imports.Process to clean up code
func formatCode(fset *token.FileSet, file *ast.File) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return ""
	}
	out, err := imports.Process("main.go", buf.Bytes(), nil)
	if err != nil {
		return buf.String()
	}
	return string(out)
}

func TestRewriteFile(t *testing.T) {
	srcUnique := `package main
func f(i int) error { return nil }
func g() (int, error) { return 0, nil }

func case1() error {
  f(1)
  return nil
}

func case2() (string, error) {
  _ = f(2)
  return "", nil
}

func case3() (int, error) {
  i, _ := g()
  return i, nil
}

func caseSkip() {
  f(4)
}
`
	file, pkg := parseAndCheck(t, srcUnique)
	injector := NewInjector(pkg, "", "")

	pts := []analysis.InjectionPoint{}

	addPt := func(match string, assign *ast.AssignStmt) {
		s, c := findStmt(file, pkg.Fset, match)
		if s == nil {
			t.Fatalf("Could not find stmt for %q", match)
		}
		if assign == nil {
			if a, ok := s.(*ast.AssignStmt); ok {
				assign = a
			}
		}
		pts = append(pts, analysis.InjectionPoint{Stmt: s, Call: c, Assign: assign, Pos: s.Pos()})
	}

	addPt("f(1)", nil)
	addPt("_ = f(2)", nil)
	addPt("i, _ := g()", nil)
	addPt("f(4)", nil)

	changed, err := injector.RewriteFile(file, pts)
	if err != nil {
		t.Fatalf("RewriteFile failed: %v", err)
	}
	if !changed {
		t.Errorf("Expected changes, got none")
	}

	var buf bytes.Buffer
	printer.Fprint(&buf, pkg.Fset, file)
	output := buf.String()
	normOutput := normalizeSpace(output)

	if !strings.Contains(normOutput, "if err := f(1); err != nil") {
		t.Errorf("Case 1 not using collapsed syntax. Got:\n%s", output)
	}
	if !strings.Contains(output, "return err") {
		t.Errorf("Case 1 return missing. Got:\n%s", output)
	}

	if !strings.Contains(normOutput, "if err := f(2); err != nil") {
		t.Errorf("Case 2 not using collapsed syntax. Got:\n%s", output)
	}
	if !strings.Contains(output, `return "", err`) {
		t.Errorf("Case 2 return statement incorrect. Got:\n%s", output)
	}

	if strings.Contains(normOutput, "if i, err := g();") {
		t.Errorf("Case 3 incorrectly collapsed (scope unsafe). Got:\n%s", output)
	}
	if !strings.Contains(output, "i, err := g()") {
		t.Errorf("Case 3 assignment rewrite incorrect. Got:\n%s", output)
	}
}

// TestRewriteFile_Shadowing verifies smart shadowing logic:
// 1. If 'err' exists and is error, use it with (=) or (:=) as context permits.
// 2. If 'err' exists and is incompatible (int param), rename to semantic name (failErr).
func TestRewriteFile_Shadowing(t *testing.T) {
	srcShadow := `package main
func fail() error { return nil }

func usage(err int) error {
  _ = err
  fail()
  return nil
}

func reuse() error {
  var err error
  fail()
  return err
}

func preAssigned() error {
  var x int
  x = 1
  _ = x
  fail()
  return nil
}
`
	file, pkg := parseAndCheck(t, srcShadow)
	injector := NewInjector(pkg, "", "")

	pts := []analysis.InjectionPoint{}

	// Case 1: Incompatible 'err' (int param) -> Rename to failErr
	s, c := findStmt(file, pkg.Fset, "fail()")
	if s == nil {
		t.Fatal("stm not found")
	}
	pts = append(pts, analysis.InjectionPoint{Stmt: s, Call: c, Pos: s.Pos()})

	// Case 2: Compatible 'err' exists -> Reuse 'err', use assignment '='
	// Iterate to find the *second* call to fail() which is inside reuse()
	// The first finding above finds usage()'s fail.
	var sReuse ast.Stmt
	var cReuse *ast.CallExpr

	ast.Inspect(file, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok && fn.Name.Name == "reuse" {
			ast.Inspect(fn.Body, func(bn ast.Node) bool {
				if stmt, ok := bn.(*ast.ExprStmt); ok {
					if ce, ok := stmt.X.(*ast.CallExpr); ok {
						if ident, ok := ce.Fun.(*ast.Ident); ok && ident.Name == "fail" {
							sReuse = stmt
							cReuse = ce
							return false
						}
					}
				}
				return true
			})
			return false
		}
		return true
	})

	if sReuse != nil {
		pts = append(pts, analysis.InjectionPoint{Stmt: sReuse, Call: cReuse, Pos: sReuse.Pos()})
	} else {
		t.Fatal("Could not locate fail() call in reuse()")
	}

	// Case 3: Assignment needing var decl
	// We check if preAssigned logic executes or if we need another point.
	// For this test, we rely on the 2 points added.

	changed, err := injector.RewriteFile(file, pts)
	if err != nil {
		t.Fatalf("RewriteFile failed: %v", err)
	}
	if !changed {
		t.Fatal("Expected changes")
	}

	var buf bytes.Buffer
	printer.Fprint(&buf, pkg.Fset, file)
	output := buf.String()
	normOutput := normalizeSpace(output)

	// Check Case 1 (Rename)
	// Since 'err' is int, we expect semantic name 'failErr'.
	if !strings.Contains(normOutput, "if failErr := fail(); failErr != nil") {
		t.Errorf("Expected semantic name 'failErr' for incompatible shadow. Output:\n%s", output)
	}

	// Check Case 2 (Reuse)
	// We expect "err = fail()" (assignment, not definition) because err exists in scope.
	// We do NOT expect "err := fail()"
	if strings.Contains(normOutput, "if err := fail()") {
		t.Errorf("Expected reuse via assignment (=), got definition (:=). Output chunk:\n...reuse()...")
	}
	if !strings.Contains(normOutput, "err = fail()") {
		t.Errorf("Expected 'err = fail()' assignment reuse. Output:\n%s", output)
	}
}

// TestRewriteFile_MixedAssignmentVarDecl verifies the explicit injection of "var err error"
// when replacing an assignment statement like "x = foo()" where err is new.
func TestRewriteFile_MixedAssignmentVarDecl(t *testing.T) {
	srcValid := `package main
func foo() (int, error) { return 0, nil }
func run() error {
  var x int
  x, _ = foo()
  _ = x
  return nil
}
`
	file, pkg := parseAndCheck(t, srcValid)
	injector := NewInjector(pkg, "", "")

	// Find stmt
	s, c := findStmt(file, pkg.Fset, "x, _ = foo()")
	pt := analysis.InjectionPoint{Stmt: s, Call: c, Pos: s.Pos()}
	// Fix assign mapping
	if as, ok := s.(*ast.AssignStmt); ok {
		pt.Assign = as
	}

	changed, err := injector.RewriteFile(file, []analysis.InjectionPoint{pt})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("Expected change")
	}

	var buf bytes.Buffer
	printer.Fprint(&buf, pkg.Fset, file)
	output := buf.String()
	norm := normalizeSpace(output)

	// Expect:
	// var err error
	// x, err = foo()
	if !strings.Contains(norm, "var err error") {
		t.Error("Expected 'var err error' injection")
	}
	if !strings.Contains(norm, "x, err = foo()") {
		t.Error("Expected assignment 'x, err = foo()'")
	}
}

func TestRewriteFile_Comments(t *testing.T) {
	src := `package main
func do() error { return nil }
func run() error {
  do() // IMPORTANT COMMENT
  return nil
}
`
	file, pkg := parseAndCheck(t, src)
	injector := NewInjector(pkg, "", "")

	pts := []analysis.InjectionPoint{}
	s, c := findStmt(file, pkg.Fset, "do()")
	if s == nil {
		t.Fatal("stm not found")
	}
	pts = append(pts, analysis.InjectionPoint{Stmt: s, Call: c, Pos: s.Pos()})

	changed, err := injector.RewriteFile(file, pts)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("Expected change")
	}

	var buf bytes.Buffer
	if err := format.Node(&buf, pkg.Fset, file); err != nil {
		t.Fatal(err)
	}
	outputStr := buf.String()

	if !strings.Contains(outputStr, "IMPORTANT COMMENT") {
		t.Errorf("Comment lost during rewrite. Output:\n%s", outputStr)
	}
}

func TestRewriteFile_GoStmt(t *testing.T) {
	src := `package main
func task() error { return nil }
func multi() (int, error) { return 0, nil }

func main() {
  go task()
  go multi()
}
`
	file, pkg := parseAndCheck(t, src)
	injector := NewInjector(pkg, "", "log-fatal")

	pts := []analysis.InjectionPoint{}
	s1, c1 := findStmt(file, pkg.Fset, "go task()")
	if s1 == nil {
		t.Fatal("go task() not found")
	}
	pts = append(pts, analysis.InjectionPoint{Stmt: s1, Call: c1, Pos: s1.Pos()})

	s2, c2 := findStmt(file, pkg.Fset, "go multi()")
	if s2 == nil {
		t.Fatal("go multi() not found")
	}
	pts = append(pts, analysis.InjectionPoint{Stmt: s2, Call: c2, Pos: s2.Pos()})

	changed, err := injector.RewriteFile(file, pts)
	if err != nil {
		t.Fatalf("RewriteFile GoStmt failed: %v", err)
	}
	if !changed {
		t.Fatal("Expected changes for go routines")
	}

	var buf bytes.Buffer
	printer.Fprint(&buf, pkg.Fset, file)
	output := buf.String()
	norm := normalizeSpace(output)

	if !strings.Contains(norm, "go func() {") {
		t.Error("Did not create anonymous func wrapper")
	}
	if !strings.Contains(norm, "err := task()") {
		t.Error("Did not assign error inside wrapper (task)")
	}
	if !strings.Contains(norm, "log.Fatal(err)") {
		t.Error("Did not inject log.Fatal")
	}

	if !strings.Contains(norm, "_, err := multi()") && !strings.Contains(norm, "_, err := multi") {
		t.Errorf("Did not discard value returns for multi. Got:\n%s", output)
	}
}

func TestLogFallback(t *testing.T) {
	src := `package main
func fail() error { return nil }
func job() {
  fail()
}
`
	file, pkg := parseAndCheck(t, src)
	injector := NewInjector(pkg, "", "")

	// 1. Identify point
	s, c := findStmt(file, pkg.Fset, "fail()")
	if s == nil {
		t.Fatal("fail() not found")
	}
	pt := analysis.InjectionPoint{Stmt: s, Call: c, Pos: s.Pos()}

	// 2. Invoke LogFallback
	changed, err := injector.LogFallback(file, pt)
	if err != nil {
		t.Fatalf("LogFallback failed: %v", err)
	}
	if !changed {
		t.Error("LogFallback returned false, expected true")
	}

	// 3. Verify Output
	out := formatCode(injector.Fset, file)
	normOut := normalizeSpace(out)

	if !strings.Contains(out, `"log"`) {
		t.Error("log import not added")
	}

	// Expect standard 'err' shadowing since no conflict
	if !strings.Contains(normOut, `if err := fail(); err != nil { log.Printf("ignored error in fail: %v", err) }`) {
		t.Errorf("Log fallback logic mismatch mismatch. Got:\n%s", out)
	}
}
