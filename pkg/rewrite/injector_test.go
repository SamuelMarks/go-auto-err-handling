package rewrite

import (
	"bytes"
	"go/ast"
	"go/format" // Explicit import to fix undefined format error
	"go/importer"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/analysis"
	"golang.org/x/tools/go/packages"
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
		// Optimization: if we already found it, stop.
		if targetStmt != nil {
			return false
		}

		if stmt, ok := n.(ast.Stmt); ok {
			// Filter out container statements that might textually contain the match
			// but are not the atomic statement we want to replace.
			switch stmt.(type) {
			case *ast.BlockStmt, *ast.IfStmt, *ast.ForStmt, *ast.SwitchStmt, *ast.CaseClause:
				return true
			}

			// Simple heuristics: Check if printed stmt contains match
			var buf bytes.Buffer
			printer.Fprint(&buf, fset, stmt)
			if strings.Contains(buf.String(), match) && targetStmt == nil {
				// We found a candidate statement (e.g. ExprStmt or AssignStmt)
				targetStmt = stmt

				// Try to dig out the call expr to verify/populate Call
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

// normalizeSpace compacts whitespace to ensure robust comparison against generated code
// even if comments or formatter introduce newlines/indentation differences.
func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func TestRewriteFile(t *testing.T) {
	// Source code with different scenarios
	// NOTE: Comments removed from calls to allow clean AST rewriting verification
	// without comment location weirdness confusing the simple string checks.
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
	// Passing empty strings for defaults
	injector := NewInjector(pkg, "", "")

	pts := []analysis.InjectionPoint{}

	// Helper to add point
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

	// Run Rewrite
	changed, err := injector.RewriteFile(file, pts)
	if err != nil {
		t.Fatalf("RewriteFile failed: %v", err)
	}
	if !changed {
		t.Errorf("Expected changes, got none")
	}

	// Render output
	var buf bytes.Buffer
	printer.Fprint(&buf, pkg.Fset, file)
	output := buf.String()
	normOutput := normalizeSpace(output)

	// Validation
	// Case 1: Expected "if err := f(1); err != nil { return err }" (collapsed)
	if !strings.Contains(normOutput, "if err := f(1); err != nil") {
		t.Errorf("Case 1 not using collapsed syntax. Got:\n%s", output)
	}
	if !strings.Contains(output, "return err") {
		t.Errorf("Case 1 return missing. Got:\n%s", output)
	}

	// Case 2: Expected "if err := f(2); err != nil { return "", err }" (collapsed)
	if !strings.Contains(normOutput, "if err := f(2); err != nil") {
		t.Errorf("Case 2 not using collapsed syntax. Got:\n%s", output)
	}
	if !strings.Contains(output, `return "", err`) {
		t.Errorf("Case 2 return statement incorrect. Got:\n%s", output)
	}

	// Case 3: Expected "i, err := g(); if err != nil { return 0, err }"
	// Should NOT be collapsed because 'i' escapes the if scope
	if strings.Contains(normOutput, "if i, err := g();") {
		t.Errorf("Case 3 incorrectly collapsed (scope unsafe). Got:\n%s", output)
	}
	if !strings.Contains(output, "i, err := g()") {
		t.Errorf("Case 3 assignment rewrite incorrect. Got:\n%s", output)
	}

	// Case Skip: Enclosing func caseSkip() returns nothing. Should NOT contain "if err != nil" logic inserted for f(4).
	// Since caseSkip has no return values, Injector should skip rewrite.
}

func TestRewriteFile_Shadowing(t *testing.T) {
	// Scenario: "err" already defined in scope. Injector should use "err1".
	// We use "err" as an argument to ensure it is in the closest function scope.
	srcShadow := `package main
func fail() error { return nil }

func usage(err int) error {
	_ = err
	fail()
	return nil
}
`
	file, pkg := parseAndCheck(t, srcShadow)
	injector := NewInjector(pkg, "", "")

	pts := []analysis.InjectionPoint{}

	// Locate fail()
	s, c := findStmt(file, pkg.Fset, "fail()")
	if s == nil {
		t.Fatal("stm not found")
	}
	pts = append(pts, analysis.InjectionPoint{Stmt: s, Call: c, Pos: s.Pos()})

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

	// Check for collapsed "if err1 := fail(); err1 != nil"
	// We verify using normalized string to safely ignore comment-induced newlines.
	if !strings.Contains(normOutput, "if err1 := fail(); err1 != nil") {
		t.Errorf("Expected collapsed if with err1. Output:\n%s", output)
	}

	// Double check the AST structure for correctness
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", output, parser.ParseComments)
	if err != nil {
		t.Fatalf("Failed to parse output: %v\n%s", err, output)
	}

	foundErr1Check := false
	ast.Inspect(f, func(n ast.Node) bool {
		if ifStmt, ok := n.(*ast.IfStmt); ok {
			if bin, ok := ifStmt.Cond.(*ast.BinaryExpr); ok {
				if x, ok := bin.X.(*ast.Ident); ok && x.Name == "err1" {
					if bin.Op == token.NEQ {
						if y, ok := bin.Y.(*ast.Ident); ok && y.Name == "nil" {
							foundErr1Check = true
						}
					}
				}
			}
		}
		return true
	})

	if !foundErr1Check {
		t.Errorf("Expected 'if err1 != nil' check. Output:\n%s", output)
	}

	if !strings.Contains(output, "return err1") {
		t.Errorf("Expected return using 'err1'.")
	}
}

// TestRewriteFile_Comments ensures that comments attached to the replaced node are preserved.
func TestRewriteFile_Comments(t *testing.T) {
	src := `package main
func do() error { return nil }
func run() error { // Renamed from main to run to allow return values during typing
	do() // IMPORTANT COMMENT
	return nil
}
`
	// We need to parse with ParseComments enabled
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

	// Render and format
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
	// Find go task()
	s1, c1 := findStmt(file, pkg.Fset, "go task()")
	if s1 == nil {
		t.Fatal("go task() not found")
	}
	pts = append(pts, analysis.InjectionPoint{Stmt: s1, Call: c1, Pos: s1.Pos()})

	// Find go multi()
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

	// Build output
	var buf bytes.Buffer
	printer.Fprint(&buf, pkg.Fset, file)
	output := buf.String()
	norm := normalizeSpace(output)

	// Check wrapper for task()
	// Should be: go func() { err := task(); if err != nil { log.Fatal(err) } }()
	if !strings.Contains(norm, "go func() {") {
		t.Error("Did not create anonymous func wrapper")
	}
	if !strings.Contains(norm, "err := task()") {
		t.Error("Did not assign error inside wrapper (task)")
	}
	if !strings.Contains(norm, "log.Fatal(err)") {
		t.Error("Did not inject log.Fatal")
	}

	// Check wrapper for multi()
	// Should be: go func() { _, err := multi(); if err != nil ...
	// Note: normalizeSpace might make it `_, err := multi()`
	if !strings.Contains(norm, "_, err := multi()") && !strings.Contains(norm, "_, err := multi") {
		t.Errorf("Did not discard value returns for multi. Got:\n%s", output)
	}
}
