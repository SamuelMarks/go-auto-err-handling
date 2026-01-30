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

	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
	"golang.org/x/tools/go/packages"
)

// setupDstEnv creates the environment for DST-based rewriting.
// Returns Injector, AST File, and DST File.
// If skipTypes is true, ignores type checker errors.
func setupDstEnv(t *testing.T, src string, skipTypes bool) (*Injector, *ast.File, *dst.File) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parser failed: %v", err)
	}

	conf := types.Config{Importer: importer.Default()}
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}
	_, err = conf.Check("main", fset, []*ast.File{f}, info)
	if err != nil && !skipTypes {
		t.Fatalf("type check failed: %v", err)
	}

	dstFile, err := decorator.NewDecorator(fset).DecorateFile(f)
	if err != nil {
		t.Fatalf("decoration failed: %v", err)
	}

	pkg := &packages.Package{
		Fset:      fset,
		TypesInfo: info,
	}
	injector := NewInjector(pkg, "", "")

	return injector, f, dstFile
}

// renderDstFile helper to print DST to string.
func renderDstFile(t *testing.T, file *dst.File) string {
	res := decorator.NewRestorer()
	var buf bytes.Buffer
	if err := res.Fprint(&buf, file); err != nil {
		t.Fatalf("restorer failed: %v", err)
	}
	return buf.String()
}

func normalizeStr(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func TestRewritePanics_DST(t *testing.T) {
	// Ensure all imports are actively used
	src := `package main

import "errors"
import "fmt"

func panicString() {
	panic("fail")
}

func panicError() {
	panic(errors.New("fail"))
}

// Comments should be preserved
func panicOther() {
	// Pre-panic comment
	panic(123)
}

func existingError() error {
	panic("boom")
}

func complexReturn() int {
	panic("c")
}

func useFmt() {
    fmt.Println("usage")
}
`
	injector, astFile, dstFile := setupDstEnv(t, src, false)

	changed, err := injector.RewritePanics(dstFile, astFile)
	if err != nil {
		t.Fatalf("RewritePanics failed: %v", err)
	}
	if !changed {
		t.Error("Expected changes")
	}

	out := renderDstFile(t, dstFile)
	norm := normalizeStr(out)

	// Case 1: String -> fmt.Errorf
	if !strings.Contains(norm, `return fmt.Errorf("%s", "fail")`) {
		t.Errorf("panicString failed. Got:\n%s", out)
	}

	// Case 2: Error -> direct return
	if !strings.Contains(norm, `return errors.New("fail")`) {
		t.Errorf("panicError failed. Got:\n%s", out)
	}

	// Case 3: Other -> fmt.Errorf("%v") + Comments
	if !strings.Contains(out, "// Pre-panic comment") {
		t.Error("Comment lost in panicOther")
	}
	if !strings.Contains(norm, `return fmt.Errorf("%v", 123)`) {
		t.Errorf("panicOther failed. Got:\n%s", out)
	}

	// Case 4: Existing Error
	// Should not double assign signature
	if strings.Contains(out, "error, error") {
		t.Error("Signature doubled for existingError")
	}
	if !strings.Contains(norm, `return fmt.Errorf("%s", "boom")`) {
		t.Error("existingError body not updated")
	}

	// Case 5: Complex (int) -> (int, error)
	if !strings.Contains(norm, `func complexReturn() (int, error)`) {
		t.Error("complexReturn signature not updated")
	}
	if !strings.Contains(norm, `return 0, fmt.Errorf("%s", "c")`) {
		t.Errorf("complexReturn body not updated. Got:\n%s", out)
	}
}

func TestRewritePanics_NoArgs(t *testing.T) {
	// Invalid Go code (panic requires arg), type check will fail, so we skip it to test robustness
	src := `package main
func f() { panic() }`
	injector, astFile, dstFile := setupDstEnv(t, src, true)
	_, err := injector.RewritePanics(dstFile, astFile)
	if err == nil {
		t.Error("Expected error for panic() without args")
	}
}

func TestRewritePanics_NilInputs(t *testing.T) {
	injector := &Injector{}
	_, err := injector.RewritePanics(nil, nil)
	if err == nil {
		t.Error("Expected error for nil inputs")
	}
}
