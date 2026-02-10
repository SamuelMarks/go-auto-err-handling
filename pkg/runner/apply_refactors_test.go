package runner

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/analysis"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/refactor"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/report"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/rewrite"
	"github.com/dave/dst"
	"golang.org/x/tools/go/packages"
)

func makePkgWithSig(t *testing.T, sig *types.Signature) (*packages.Package, *ast.File, *ast.FuncDecl, *types.Func) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", "package p\nfunc target() {}\n", 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	var decl *ast.FuncDecl
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			decl = fd
			break
		}
	}
	if decl == nil {
		t.Fatal("expected func decl")
	}

	pkgTypes := types.NewPackage("example.com/p", "p")
	fnObj := types.NewFunc(token.NoPos, pkgTypes, decl.Name.Name, sig)
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  map[*ast.Ident]types.Object{decl.Name: fnObj},
		Uses:  make(map[*ast.Ident]types.Object),
	}
	pkg := &packages.Package{
		Fset:      fset,
		Syntax:    []*ast.File{file},
		Types:     pkgTypes,
		TypesInfo: info,
	}
	return pkg, file, decl, fnObj
}

func makePoint(pkg *packages.Package, file *ast.File, fnObj *types.Func) analysis.InjectionPoint {
	ident := ast.NewIdent(fnObj.Name())
	pkg.TypesInfo.Uses[ident] = fnObj
	call := &ast.CallExpr{Fun: ident}
	return analysis.InjectionPoint{
		Pkg:  pkg,
		File: file,
		Call: call,
		Pos:  call.Pos(),
	}
}

func TestApplyRefactors_ThirdPartySkip(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	pkgTypes := types.NewPackage("example.com/p", "p")
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	pkg := &packages.Package{Types: pkgTypes, TypesInfo: info}

	extPkg := types.NewPackage("example.com/ext", "ext")
	extFn := types.NewFunc(token.NoPos, extPkg, "Do", types.NewSignatureType(nil, nil, nil, nil, nil, false))
	sel := &ast.SelectorExpr{X: ast.NewIdent("ext"), Sel: ast.NewIdent("Do")}
	info.Uses[sel.Sel] = extFn

	point := analysis.InjectionPoint{Pkg: pkg, Call: &ast.CallExpr{Fun: sel}}
	mgr := &dstManager{pkgs: map[string]*packages.Package{}}
	opts := Options{EnableThirdPartyErr: false, Reporter: report.New()}

	count, err := applyRefactors(mgr, []analysis.InjectionPoint{point}, opts, &analysis.InterfaceRegistry{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no changes, got %d", count)
	}
}

func TestApplyRefactors_GetError(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	sig := types.NewSignatureType(nil, nil, nil, nil, nil, false)
	pkg, file, _, fnObj := makePkgWithSig(t, sig)

	mgr := &dstManager{
		fset:     token.NewFileSet(),
		cache:    map[string]*dst.File{},
		modified: map[string]bool{},
		pkgs:     map[string]*packages.Package{"p": pkg},
	}

	point := makePoint(pkg, file, fnObj)
	opts := Options{EnableNonExistingErr: true, Reporter: report.New()}

	if _, err := applyRefactors(mgr, []analysis.InjectionPoint{point}, opts, &analysis.InterfaceRegistry{}); err == nil {
		t.Fatal("expected error from mgr.Get")
	}
}

func TestApplyRefactors_CtxNil(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	sig := types.NewSignatureType(nil, nil, nil, nil, nil, false)
	pkg, file, _, fnObj := makePkgWithSig(t, sig)
	mgr := newDstManager([]*packages.Package{pkg})
	decorateFileFn = func(*token.FileSet, *ast.File) (*dst.File, error) {
		return &dst.File{}, nil
	}
	findEnclosingFuncFn = func(*packages.Package, *ast.File, token.Pos) *FuncContext {
		return nil
	}

	point := makePoint(pkg, file, fnObj)
	opts := Options{EnableNonExistingErr: true, Reporter: report.New()}

	count, err := applyRefactors(mgr, []analysis.InjectionPoint{point}, opts, &analysis.InterfaceRegistry{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no changes, got %d", count)
	}
}

func TestApplyRefactors_SkipTestRefactor(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	sig := types.NewSignatureType(nil, nil, nil, nil, nil, false)
	pkg, file, decl, fnObj := makePkgWithSig(t, sig)
	mgr := newDstManager([]*packages.Package{pkg})
	decorateFileFn = func(*token.FileSet, *ast.File) (*dst.File, error) {
		return &dst.File{}, nil
	}
	findEnclosingFuncFn = func(*packages.Package, *ast.File, token.Pos) *FuncContext {
		return &FuncContext{Sig: sig, Decl: decl, Node: decl, TestParam: "t"}
	}

	point := makePoint(pkg, file, fnObj)
	opts := Options{EnableTestRefactor: false, Reporter: report.New()}

	count, err := applyRefactors(mgr, []analysis.InjectionPoint{point}, opts, &analysis.InterfaceRegistry{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no changes, got %d", count)
	}
}

func TestApplyRefactors_PreexistingTestParamAndRewriteError(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	errType := types.Universe.Lookup("error").Type()
	results := types.NewTuple(types.NewVar(token.NoPos, nil, "", errType))
	sig := types.NewSignatureType(nil, nil, nil, nil, results, false)
	pkg, file, decl, fnObj := makePkgWithSig(t, sig)
	mgr := newDstManager([]*packages.Package{pkg})
	decorateFileFn = func(*token.FileSet, *ast.File) (*dst.File, error) {
		return &dst.File{}, nil
	}

	findEnclosingFuncFn = func(*packages.Package, *ast.File, token.Pos) *FuncContext {
		return &FuncContext{Sig: sig, Decl: decl, Node: decl, TestParam: "t"}
	}

	rewriteFileFn = func(inj *rewrite.Injector, _ *dst.File, _ *ast.File, _ []analysis.InjectionPoint) (bool, error) {
		if inj.TestParam != "t" {
			t.Fatalf("expected injector.TestParam to be set, got %q", inj.TestParam)
		}
		return false, errors.New("rewrite failed")
	}

	point := makePoint(pkg, file, fnObj)
	opts := Options{EnablePreexistingErr: true, EnableTestRefactor: true, Reporter: report.New()}

	if _, err := applyRefactors(mgr, []analysis.InjectionPoint{point}, opts, &analysis.InterfaceRegistry{}); err == nil {
		t.Fatal("expected rewrite error")
	}
}

func TestApplyRefactors_LiteralSkip(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	sig := types.NewSignatureType(nil, nil, nil, nil, nil, false)
	pkg, file, _, fnObj := makePkgWithSig(t, sig)
	mgr := newDstManager([]*packages.Package{pkg})
	decorateFileFn = func(*token.FileSet, *ast.File) (*dst.File, error) {
		return &dst.File{}, nil
	}
	findEnclosingFuncFn = func(*packages.Package, *ast.File, token.Pos) *FuncContext {
		return &FuncContext{Sig: sig, Lit: &ast.FuncLit{}}
	}

	point := makePoint(pkg, file, fnObj)
	opts := Options{EnableNonExistingErr: true, Reporter: report.New()}

	count, err := applyRefactors(mgr, []analysis.InjectionPoint{point}, opts, &analysis.InterfaceRegistry{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no changes, got %d", count)
	}
}

func TestApplyRefactors_DeclNilSkip(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	sig := types.NewSignatureType(nil, nil, nil, nil, nil, false)
	pkg, file, _, fnObj := makePkgWithSig(t, sig)
	mgr := newDstManager([]*packages.Package{pkg})
	decorateFileFn = func(*token.FileSet, *ast.File) (*dst.File, error) {
		return &dst.File{}, nil
	}
	findEnclosingFuncFn = func(*packages.Package, *ast.File, token.Pos) *FuncContext {
		return &FuncContext{Sig: sig}
	}

	point := makePoint(pkg, file, fnObj)
	opts := Options{EnableNonExistingErr: true, Reporter: report.New()}

	count, err := applyRefactors(mgr, []analysis.InjectionPoint{point}, opts, &analysis.InterfaceRegistry{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no changes, got %d", count)
	}
}

func TestApplyRefactors_RewriteAndPropagateErrors(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	sig := types.NewSignatureType(nil, nil, nil, nil, nil, false)
	pkg, file, decl, fnObj := makePkgWithSig(t, sig)
	mgr := newDstManager([]*packages.Package{pkg})
	decorateFileFn = func(*token.FileSet, *ast.File) (*dst.File, error) {
		return &dst.File{}, nil
	}
	findEnclosingFuncFn = func(*packages.Package, *ast.File, token.Pos) *FuncContext {
		return &FuncContext{Sig: sig, Decl: decl, Node: decl}
	}
	checkComplianceFn = func(*analysis.InterfaceRegistry, *types.Func) ([]analysis.InterfaceConflict, error) {
		return nil, nil
	}
	addErrorToSignatureFn = func(*token.FileSet, *ast.FuncDecl) (bool, error) { return true, nil }
	patchSignatureFn = func(*types.Info, *ast.FuncDecl, *types.Package) error { return nil }
	findDstNodeFn = func(*token.FileSet, *dst.File, *ast.File, ast.Node) (rewrite.DstMapResult, error) {
		return rewrite.DstMapResult{}, nil
	}
	rewriteFileFn = func(*rewrite.Injector, *dst.File, *ast.File, []analysis.InjectionPoint) (bool, error) {
		return false, errors.New("rewrite failed")
	}

	point := makePoint(pkg, file, fnObj)
	opts := Options{EnableNonExistingErr: true, Reporter: report.New()}

	if _, err := applyRefactors(mgr, []analysis.InjectionPoint{point}, opts, &analysis.InterfaceRegistry{}); err == nil {
		t.Fatal("expected rewrite error")
	}

	rewriteFileFn = func(*rewrite.Injector, *dst.File, *ast.File, []analysis.InjectionPoint) (bool, error) {
		return true, nil
	}
	propagateCallersFn = func([]*packages.Package, refactor.DstProvider, types.Object, string) (int, error) {
		return 0, errors.New("propagate failed")
	}

	if _, err := applyRefactors(mgr, []analysis.InjectionPoint{point}, opts, &analysis.InterfaceRegistry{}); err == nil {
		t.Fatal("expected propagate error")
	}
}

func TestApplyRefactors_PanicErrors(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", "package p\nfunc f() {}\n", 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	pkg := &packages.Package{Fset: fset, Syntax: []*ast.File{file}}

	mgr := &dstManager{
		fset:     token.NewFileSet(),
		cache:    map[string]*dst.File{},
		modified: map[string]bool{},
		pkgs:     map[string]*packages.Package{"p": pkg},
	}
	opts := Options{PanicToReturn: true, Reporter: report.New()}

	if _, err := applyRefactors(mgr, nil, opts, &analysis.InterfaceRegistry{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mgr.fset = fset
	decorateFileFn = func(*token.FileSet, *ast.File) (*dst.File, error) {
		return &dst.File{}, nil
	}
	rewritePanicsFn = func(*rewrite.Injector, *dst.File, *ast.File) (bool, error) {
		return false, errors.New("panic rewrite failed")
	}

	if _, err := applyRefactors(mgr, nil, opts, &analysis.InterfaceRegistry{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
