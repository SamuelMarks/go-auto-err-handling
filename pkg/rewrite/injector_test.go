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

func TestRewriteFile_Comments_AssignStmt(t *testing.T) {
	src := `package main
func do() error { return nil }
func run() error {
  // IMPORTANT COMMENT
  do()
  return nil
}
`
	file, pkg := parseAndCheck(t, src)
	injector := NewInjector(pkg, "", "")

	s, c := findStmt(file, pkg.Fset, "do()")
	pts := []analysis.InjectionPoint{{Stmt: s, Call: c, Pos: s.Pos()}}

	changed, err := injector.RewriteFile(file, pts)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("Expected change")
	}

	out := formatCode(pkg.Fset, file)

	// We expect:
	// // IMPORTANT COMMENT
	// if err := do(); err != nil {
	if !strings.Contains(out, "// IMPORTANT COMMENT") {
		t.Error("Comment lost")
	}
}

// TestRewriteFile_Comments_DeclStmt ensures that if we inject a var declaration,
// comments attach to it properly.
func TestRewriteFile_Comments_DeclStmt(t *testing.T) {
	src := `package main
func foo() (int, error) { return 0, nil }
func run() error {
	var x int
	// DOC COMMENT
	x, _ = foo()
	_ = x
	return nil
}
`
	file, pkg := parseAndCheck(t, src)
	injector := NewInjector(pkg, "", "")

	s, c := findStmt(file, pkg.Fset, "x, _ = foo()")
	// Make sure we set Assign field correctly for rewrite logic
	as := s.(*ast.AssignStmt)
	pts := []analysis.InjectionPoint{{Stmt: s, Call: c, Assign: as, Pos: s.Pos()}}

	changed, err := injector.RewriteFile(file, pts)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("Expected change")
	}

	out := formatCode(pkg.Fset, file)

	// Since refactoring injects "var err error" before "x, err = foo()",
	// the comment should conceptually stick to the assignment or the var decl?
	// The injector logic attaches comments to the FIRST generated node.
	// Generated nodes: [DeclStmt(var err), AssignStmt(x, err = ...), IfStmt]
	// Expectation:
	// // DOC COMMENT
	// var err error
	if !strings.Contains(out, "// DOC COMMENT\n\tvar err error") {
		t.Errorf("Comment not attached to injected decl. Got:\n%s", out)
	}
}

func TestRewriteFile_Comments_GoStmt(t *testing.T) {
	src := `package main
func task() error { return nil }
func main() {
	// ASYNC TASK
	go task()
}
`
	file, pkg := parseAndCheck(t, src)
	injector := NewInjector(pkg, "", "")

	s, c := findStmt(file, pkg.Fset, "go task()")
	pts := []analysis.InjectionPoint{{Stmt: s, Call: c, Pos: s.Pos()}}

	changed, err := injector.RewriteFile(file, pts)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("Expected change")
	}

	out := formatCode(pkg.Fset, file)

	// Expect:
	// // ASYNC TASK
	// go func() { ... }
	if !strings.Contains(out, "// ASYNC TASK\n\tgo func()") {
		t.Errorf("Comment not attached to go stmt. Got:\n%s", out)
	}
}

func TestLogFallback_Comments(t *testing.T) {
	src := `package main
func fail() error { return nil }
func job() {
	// IGNORED
	fail()
}
`
	file, pkg := parseAndCheck(t, src)
	injector := NewInjector(pkg, "", "")

	s, c := findStmt(file, pkg.Fset, "fail()")
	pt := analysis.InjectionPoint{Stmt: s, Call: c, Pos: s.Pos()}

	changed, err := injector.LogFallback(file, pt)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("Expected change")
	}

	out := formatCode(pkg.Fset, file)

	if !strings.Contains(out, "// IGNORED") {
		t.Error("Comment lost in log fallback")
	}
}
