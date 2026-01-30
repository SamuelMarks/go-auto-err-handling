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

	injector := NewInjector(pkg, "", "")
	return injector, dstFile, astFile
}

// findPoint helper
func findPoint(t *testing.T, f *ast.File, substr string) analysis.InjectionPoint {
	var pt analysis.InjectionPoint
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		if s, ok := n.(ast.Stmt); ok {
			// Skip BlockStmt to match Detection logic and prevent mapping issues
			if _, isBlock := s.(*ast.BlockStmt); isBlock {
				return true
			}

			// Naive check if statement contains the substring via token pos?
			// Let's look for call expr inside stmt.
			if callStmtContains(s, substr) {
				pt = analysis.InjectionPoint{
					Stmt: s,
					File: f,
					Pos:  s.Pos(),
				}
				// Populate Call
				ast.Inspect(s, func(sn ast.Node) bool {
					if c, ok := sn.(*ast.CallExpr); ok {
						pt.Call = c
						return false
					}
					return true
				})
				// Populate Assign
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

func callStmtContains(s ast.Stmt, sub string) bool {
	match := false
	ast.Inspect(s, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			if strings.Contains(id.Name, sub) {
				match = true
			}
		}
		return true
	})
	return match
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
	pt := findPoint(t, astFile, "fail")

	changed, err := injector.RewriteFile(dstFile, astFile, []analysis.InjectionPoint{pt})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("Expected change")
	}

	out := render(t, dstFile)

	// Check Trivia Placement with flexible whitespace
	// Expected roughly:
	// // Pre-comment
	// if err := fail(); ...

	if !strings.Contains(out, "// Pre-comment") {
		t.Error("Pre-comment missing")
	}
	if !strings.Contains(out, "if err := fail();") {
		t.Error("Injection failed")
	}

	// Comment check
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
	pt := findPoint(t, astFile, "task")

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

func TestLogFallback(t *testing.T) {
	src := `package main
func task() error { return nil }
func main() {
	task()
}
`
	injector, dstFile, astFile := setupInjectorTest(t, src)
	pt := findPoint(t, astFile, "task")

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
