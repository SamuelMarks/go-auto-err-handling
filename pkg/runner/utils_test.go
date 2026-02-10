package runner

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/analysis"
	"golang.org/x/tools/go/packages"
)

func TestIsThirdParty(t *testing.T) {
	if isThirdParty(analysis.InjectionPoint{}) {
		t.Fatal("expected nil package to be non-third-party")
	}

	pkgTypes := types.NewPackage("example.com/p", "p")
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	pkg := &packages.Package{Types: pkgTypes, TypesInfo: info}

	extPkg := types.NewPackage("example.com/ext", "ext")
	extFn := types.NewFunc(token.NoPos, extPkg, "Do", types.NewSignatureType(nil, nil, nil, nil, nil, false))

	sel := &ast.SelectorExpr{X: ast.NewIdent("ext"), Sel: ast.NewIdent("Do")}
	info.Uses[sel.Sel] = extFn
	call := &ast.CallExpr{Fun: sel}
	point := analysis.InjectionPoint{Pkg: pkg, Call: call}

	if !isThirdParty(point) {
		t.Fatal("expected third-party call to be detected")
	}

	localFn := types.NewFunc(token.NoPos, pkgTypes, "Local", types.NewSignatureType(nil, nil, nil, nil, nil, false))
	selLocal := &ast.SelectorExpr{X: ast.NewIdent("p"), Sel: ast.NewIdent("Local")}
	info.Uses[selLocal.Sel] = localFn
	point.Call = &ast.CallExpr{Fun: selLocal}
	if isThirdParty(point) {
		t.Fatal("expected local call to be non-third-party")
	}

	point.Call = &ast.CallExpr{Fun: ast.NewIdent("unknown")}
	if isThirdParty(point) {
		t.Fatal("expected unknown object to be non-third-party")
	}
}

func TestHasErrorReturn(t *testing.T) {
	if hasErrorReturn(nil) {
		t.Fatal("expected nil signature to be false")
	}

	sigNoResults := types.NewSignatureType(nil, nil, nil, nil, nil, false)
	if hasErrorReturn(sigNoResults) {
		t.Fatal("expected no results to be false")
	}

	errType := types.Universe.Lookup("error").Type()
	results := types.NewTuple(types.NewVar(token.NoPos, nil, "", errType))
	sigErr := types.NewSignatureType(nil, nil, nil, nil, results, false)
	if !hasErrorReturn(sigErr) {
		t.Fatal("expected error return to be true")
	}
}

func TestFormatAST(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", "package main\nfunc main() {}\n", 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	out, err := formatAST(fset, file, "main.go")
	if err != nil {
		t.Fatalf("unexpected format error: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected formatted output")
	}

	if _, err := formatAST(fset, nil, "main.go"); err == nil {
		t.Fatal("expected error for invalid node")
	}
}
