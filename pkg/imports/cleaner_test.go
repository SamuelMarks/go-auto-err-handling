package imports

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"golang.org/x/tools/go/packages"
)

func loadTestPackage(t *testing.T, src string) (*packages.Package, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}
	conf := types.Config{Importer: importer.Default()}
	pkgTypes, err := conf.Check("p", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatalf("typecheck error: %v", err)
	}
	pkg := &packages.Package{
		Fset:      fset,
		Types:     pkgTypes,
		TypesInfo: info,
		Syntax:    []*ast.File{file},
	}
	return pkg, file
}

func TestResolveAlias_NoConflict(t *testing.T) {
	src := `package p
import "fmt"
func f() { _ = fmt.Sprintf("%s", "x") }`
	pkg, file := loadTestPackage(t, src)
	resolver := NewConflictResolver(pkg, file)

	alias, changed := resolver.ResolveAlias("fmt", "fmt")
	if changed {
		t.Fatal("expected no alias change")
	}
	if alias != "fmt" {
		t.Fatalf("expected alias fmt, got %q", alias)
	}
}

func TestResolveAlias_ConflictWithLocalIdent(t *testing.T) {
	src := `package p
import stdErrors "errors"
func f() {
	errors := 1
	_ = errors
	_ = stdErrors.New("boom")
}`
	pkg, file := loadTestPackage(t, src)
	resolver := NewConflictResolver(pkg, file)

	alias, changed := resolver.ResolveAlias("errors", "errors")
	if !changed {
		t.Fatal("expected alias change")
	}
	if alias == "errors" {
		t.Fatal("expected non-default alias")
	}
	found := false
	for _, imp := range file.Imports {
		if imp.Path != nil && imp.Path.Value == `"errors"` {
			if imp.Name != nil && imp.Name.Name == alias {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected import alias %q to be added", alias)
	}
}

func TestResolveAlias_TypesInfoNil(t *testing.T) {
	src := `package p
func f() { fmt := 1; _ = fmt }`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	pkg := &packages.Package{Fset: fset, Syntax: []*ast.File{file}}
	resolver := NewConflictResolver(pkg, file)

	alias, changed := resolver.ResolveAlias("fmt", "fmt")
	if !changed {
		t.Fatal("expected alias change with nil TypesInfo")
	}
	if alias == "fmt" {
		t.Fatal("expected alias to differ from requested")
	}
}

func TestResolveAlias_DefsFallback(t *testing.T) {
	src := `package p
func f() { fmt := 1; _ = fmt }`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	var defIdent *ast.Ident
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || assign.Tok != token.DEFINE {
			return true
		}
		for _, lhs := range assign.Lhs {
			if ident, ok := lhs.(*ast.Ident); ok && ident.Name == "fmt" {
				defIdent = ident
				return false
			}
		}
		return true
	})
	if defIdent == nil {
		t.Fatal("expected to find fmt definition")
	}
	info := &types.Info{
		Defs: make(map[*ast.Ident]types.Object),
	}
	info.Defs[defIdent] = nil
	pkg := &packages.Package{Fset: fset, Syntax: []*ast.File{file}, TypesInfo: info}
	resolver := NewConflictResolver(pkg, file)

	alias, changed := resolver.ResolveAlias("fmt", "fmt")
	if !changed {
		t.Fatal("expected alias change from defs fallback")
	}
	if alias == "fmt" {
		t.Fatal("expected alias to differ from requested")
	}
}
