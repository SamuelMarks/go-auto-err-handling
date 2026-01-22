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
		Importer: nil, // Use default or mock if needed. Source shouldn't depend on external pkgs for this unit test.
		Error: func(err error) {
			t.Logf("Type check error: %v", err)
		},
	}
	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
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

func TestRewriteFile(t *testing.T) {
	// Source code with different scenarios
	// Uses Arguments to differentiate calls so finding statements works reliably with print
	srcUnique := `package main
func f(i int) error { return nil } 
func g() (int, error) { return 0, nil } 

func case1() error { 
  f(1) // Point 1
  return nil
} 

func case2() (string, error) { 
  _ = f(2) // Point 2
  return "", nil
} 

func case3() (int, error) { 
  i, _ := g() // Point 3
  return i, nil
} 

func caseSkip() { 
  f(4) // Point 4
} 
`
	file, pkg := parseAndCheck(t, srcUnique)
	injector := NewInjector(pkg)

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
		pts = append(pts, analysis.InjectionPoint{Stmt: s, Call: c, Assign: assign})
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

	// Validation
	// Case 1: Expected "err := f(1); if err != nil { return err }"
	if !strings.Contains(output, "err := f(1)") || !strings.Contains(output, "return err") {
		t.Errorf("Case 1 definition missing or incorrect. Got:\n%s", output)
	}

	// Case 2: Expected "err := f(2); if err != nil { return "", err }"
	if !strings.Contains(output, `return "", err`) {
		t.Errorf("Case 2 return statement incorrect. Got:\n%s", output)
	}

	// Case 3: Expected "i, err := g(); if err != nil { return 0, err }"
	if !strings.Contains(output, "i, err := g()") {
		t.Errorf("Case 3 assignment rewrite incorrect. Got:\n%s", output)
	}

	// Case Skip: Enclosing func caseSkip() returns nothing. Should NOT contain "if err != nil" logic inserted for f(4).
	// Since caseSkip has no return values, Injector should skip rewrite.
}
