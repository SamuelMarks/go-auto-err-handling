package rewrite

import (
	"fmt"
	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"golang.org/x/tools/go/packages"
	"testing"
)

func TestRewriteDefers_NilFile(t *testing.T) {
	inj := &Injector{}
	_, err := inj.RewriteDefers(nil, nil)
	if err == nil {
		t.Errorf("expected err")
	}
}

func TestRewriteDefers_AST_MissingBody(t *testing.T) {
	inj := &Injector{}
	src := `package main
func Target() error { return nil }`
	fset := token.NewFileSet()
	astFile, _ := parser.ParseFile(fset, "main.go", src, 0)
	dstFile, _ := decorator.NewDecorator(fset).DecorateFile(astFile)

	// Remove body
	for _, d := range astFile.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			fd.Body = nil
		}
	}

	applied, err := inj.RewriteDefers(dstFile, astFile)
	if applied || err != nil {
		t.Errorf("expected false, nil")
	}
}

func TestRewriteDefersInDST(t *testing.T) {
	inj := &Injector{}
	src := `package main
func Target() error { defer func() {}(); return nil }`
	fset := token.NewFileSet()
	astFile, _ := parser.ParseFile(fset, "main.go", src, 0)
	dstFile, _ := decorator.NewDecorator(fset).DecorateFile(astFile)

	var astDefer *ast.DeferStmt
	ast.Inspect(astFile, func(n ast.Node) bool {
		if d, ok := n.(*ast.DeferStmt); ok {
			astDefer = d
		}
		return true
	})

	var dstBlock *dst.BlockStmt
	dst.Inspect(dstFile, func(n dst.Node) bool {
		if b, ok := n.(*dst.BlockStmt); ok {
			dstBlock = b
		}
		return true
	})

	inj.rewriteDefersInDST(dstBlock, []*ast.DeferStmt{astDefer}, astFile, dstFile, "err")
}

func TestRewriteDefers_Helpers(t *testing.T) {
	ft := &dst.FuncType{}
	if hasAnonymousReturnsDST(ft) {
		t.Errorf("expected false")
	}

	ft.Results = &dst.FieldList{
		List: []*dst.Field{
			{Names: nil},
		},
	}
	if !hasAnonymousReturnsDST(ft) {
		t.Errorf("expected true")
	}

	inj := &Injector{}
	if name := inj.getErrorReturnNameDST(&dst.FuncType{}); name != "" {
		t.Errorf("expected empty")
	}

	if isErrorDstExpr(&dst.SelectorExpr{X: dst.NewIdent("fmt"), Sel: dst.NewIdent("error")}) {
		t.Errorf("expected false")
	}

	ft2 := &dst.FuncType{
		Results: &dst.FieldList{
			List: []*dst.Field{
				{Names: []*dst.Ident{dst.NewIdent("a")}},
			},
		},
	}
	if hasAnonymousReturnsDST(ft2) {
		t.Errorf("expected false")
	}

	injPkgNil := &Injector{Pkg: &packages.Package{}}
	if injPkgNil.isErrorReturningCall(&ast.CallExpr{}) {
		t.Errorf("expected false")
	}

	injTypesInfo := &Injector{
		Pkg: &packages.Package{
			TypesInfo: &types.Info{
				Types: make(map[ast.Expr]types.TypeAndValue),
			},
		},
	}
	if injTypesInfo.isErrorReturningCall(&ast.CallExpr{}) {
		t.Errorf("expected false")
	}
}

func TestRewriteDefers_Helpers3(t *testing.T) {
	// hit isErrorReturningCall returns false on type match but not error type
	call := &ast.CallExpr{}
	injTypesInfo := &Injector{
		Pkg: &packages.Package{
			TypesInfo: &types.Info{
				Types: map[ast.Expr]types.TypeAndValue{
					call: {Type: types.Typ[types.Int]},
				},
			},
		},
	}
	if injTypesInfo.isErrorReturningCall(call) {
		t.Errorf("expected false")
	}
}

func TestRewriteDefers_NormalizeToNakedReturns_Nil(t *testing.T) {
	inj := &Injector{}
	inj.normalizeToNakedReturns(nil, nil)
	inj.normalizeToNakedReturns(&dst.BlockStmt{}, nil)
	inj.normalizeToNakedReturns(&dst.BlockStmt{}, &dst.FieldList{})
}

func TestRewriteDefers_NormalizeToNakedReturns_FuncLit(t *testing.T) {
	inj := &Injector{}
	block := &dst.BlockStmt{
		List: []dst.Stmt{
			&dst.ExprStmt{X: &dst.FuncLit{
				Type: &dst.FuncType{},
				Body: &dst.BlockStmt{
					List: []dst.Stmt{&dst.ReturnStmt{Results: []dst.Expr{dst.NewIdent("1")}}},
				},
			}},
		},
	}
	results := &dst.FieldList{
		List: []*dst.Field{
			{Names: []*dst.Ident{dst.NewIdent("a")}},
		},
	}
	inj.normalizeToNakedReturns(block, results)
	// Output should be un-modified.
	if len(block.List[0].(*dst.ExprStmt).X.(*dst.FuncLit).Body.List[0].(*dst.ReturnStmt).Results) == 0 {
		t.Errorf("expected return statement to not be modified to naked return")
	}
}

func TestRewriteDefers_FuncDecl_FindDstError(t *testing.T) {
	// Set up so findDstNodeFunc fails on the astDecl
	oldFind := findDstNodeFunc
	findDstNodeFunc = func(fset *token.FileSet, dstFile *dst.File, astFile *ast.File, target ast.Node) (DstMapResult, error) {
		if _, ok := target.(*ast.FuncDecl); ok {
			return DstMapResult{}, fmt.Errorf("mock error decl")
		}
		return oldFind(fset, dstFile, astFile, target)
	}
	defer func() { findDstNodeFunc = oldFind }()

	inj := &Injector{}
	src := `package main
func Target() error { defer func() {}(); return nil }`
	fset := token.NewFileSet()
	astFile, _ := parser.ParseFile(fset, "main.go", src, 0)
	dstFile, _ := decorator.NewDecorator(fset).DecorateFile(astFile)

	// Add mock info for error checking so defer gets picked up
	inj.Pkg = &packages.Package{
		TypesInfo: &types.Info{
			Types: make(map[ast.Expr]types.TypeAndValue),
		},
	}
	// find the defer call
	ast.Inspect(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			inj.Pkg.TypesInfo.Types[c] = types.TypeAndValue{Type: types.Universe.Lookup("error").Type()}
		}
		return true
	})

	applied, err := inj.RewriteDefers(dstFile, astFile)
	if err == nil {
		t.Errorf("expected find dst err")
	}
	_ = applied
}

func TestRewriteDefers_FuncLit_FindDstError(t *testing.T) {
	oldFind := findDstNodeFunc
	findDstNodeFunc = func(fset *token.FileSet, dstFile *dst.File, astFile *ast.File, target ast.Node) (DstMapResult, error) {
		if _, ok := target.(*ast.FuncLit); ok {
			return DstMapResult{}, fmt.Errorf("mock error lit")
		}
		return oldFind(fset, dstFile, astFile, target)
	}
	defer func() { findDstNodeFunc = oldFind }()

	inj := &Injector{}
	src := `package main
func Target() error { 
	_ = func() error { defer func() {}(); return nil }
	return nil 
}`
	fset := token.NewFileSet()
	astFile, _ := parser.ParseFile(fset, "main.go", src, 0)
	dstFile, _ := decorator.NewDecorator(fset).DecorateFile(astFile)

	inj.Pkg = &packages.Package{
		TypesInfo: &types.Info{
			Types: make(map[ast.Expr]types.TypeAndValue),
		},
	}
	ast.Inspect(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			inj.Pkg.TypesInfo.Types[c] = types.TypeAndValue{Type: types.Universe.Lookup("error").Type()}
		}
		return true
	})

	applied, err := inj.RewriteDefers(dstFile, astFile)
	if err == nil {
		t.Errorf("expected find dst err lit")
	}
	_ = applied
}

func TestRewriteDefers_FindDstErrorDefer(t *testing.T) {
	oldFind := findDstNodeFunc
	findDstNodeFunc = func(fset *token.FileSet, dstFile *dst.File, astFile *ast.File, target ast.Node) (DstMapResult, error) {
		if _, ok := target.(*ast.DeferStmt); ok {
			return DstMapResult{}, fmt.Errorf("mock error defer")
		}
		return oldFind(fset, dstFile, astFile, target)
	}
	defer func() { findDstNodeFunc = oldFind }()

	inj := &Injector{}
	src := `package main
func Target() error { 
	defer func() {}()
	_ = func() error { defer func() {}(); return nil }
	return nil 
}`
	fset := token.NewFileSet()
	astFile, _ := parser.ParseFile(fset, "main.go", src, 0)
	dstFile, _ := decorator.NewDecorator(fset).DecorateFile(astFile)

	inj.Pkg = &packages.Package{
		TypesInfo: &types.Info{
			Types: make(map[ast.Expr]types.TypeAndValue),
		},
	}
	ast.Inspect(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			inj.Pkg.TypesInfo.Types[c] = types.TypeAndValue{Type: types.Universe.Lookup("error").Type()}
		}
		return true
	})

	applied, err := inj.RewriteDefers(dstFile, astFile)
	if err != nil {
		t.Errorf("expected no err, defer skip only")
	}
	if applied {
		t.Errorf("expected false")
	}
}

func TestRewriteDefers_EnsureNamedReturnsError(t *testing.T) {
	// Need to force ensureNamedReturnsDST to return an error inside RewriteDefers.
	// `ensureNamedReturnsDST` is a test hook variable in `defer.go`:
	// `var ensureNamedReturnsDST = refactor.EnsureNamedReturnsDST`
	oldEnsure := ensureNamedReturnsDST
	ensureNamedReturnsDST = func(fn *dst.FuncDecl) (bool, error) {
		return false, fmt.Errorf("mock ensure err")
	}
	defer func() { ensureNamedReturnsDST = oldEnsure }()

	inj := &Injector{}
	src := `package main
func Target() error { 
	defer func() {}()
	_ = func() error { defer func() {}(); return nil }
	return nil 
}`
	fset := token.NewFileSet()
	astFile, _ := parser.ParseFile(fset, "main.go", src, 0)
	dstFile, _ := decorator.NewDecorator(fset).DecorateFile(astFile)

	inj.Pkg = &packages.Package{
		TypesInfo: &types.Info{
			Types: make(map[ast.Expr]types.TypeAndValue),
		},
	}
	ast.Inspect(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			inj.Pkg.TypesInfo.Types[c] = types.TypeAndValue{Type: types.Universe.Lookup("error").Type()}
		}
		return true
	})

	applied, err := inj.RewriteDefers(dstFile, astFile)
	if err == nil {
		t.Errorf("expected mock err from ensureNamedReturns")
	}
	_ = applied
}

func TestRewriteDefers_EnsureNamedReturnsError_FuncLit(t *testing.T) {
	oldEnsure := ensureNamedReturnsDST
	ensureNamedReturnsDST = func(fn *dst.FuncDecl) (bool, error) {
		// Mock to fail ONLY on the synthetic wrapper decl that has no Name (since it's a func lit wrapper)
		if fn.Name == nil {
			return false, fmt.Errorf("mock lit ensure err")
		}
		return false, nil
	}
	defer func() { ensureNamedReturnsDST = oldEnsure }()

	inj := &Injector{}
	src := `package main
func Target() error { 
	_ = func() error { defer func() {}(); return nil }
	return nil 
}`
	fset := token.NewFileSet()
	astFile, _ := parser.ParseFile(fset, "main.go", src, 0)
	dstFile, _ := decorator.NewDecorator(fset).DecorateFile(astFile)

	inj.Pkg = &packages.Package{
		TypesInfo: &types.Info{
			Types: make(map[ast.Expr]types.TypeAndValue),
		},
	}
	ast.Inspect(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			inj.Pkg.TypesInfo.Types[c] = types.TypeAndValue{Type: types.Universe.Lookup("error").Type()}
		}
		return true
	})

	applied, err := inj.RewriteDefers(dstFile, astFile)
	if err == nil {
		t.Errorf("expected mock lit ensure err")
	}
	_ = applied
}

func TestRewriteDefers_EnsureNamedReturns_Changed(t *testing.T) {
	oldEnsure := ensureNamedReturnsDST
	ensureNamedReturnsDST = func(fn *dst.FuncDecl) (bool, error) {
		return true, nil // hit the 'changed' block
	}
	defer func() { ensureNamedReturnsDST = oldEnsure }()

	inj := &Injector{}
	src := `package main
func Target() error { 
	defer func() {}()
	_ = func() error { defer func() {}(); return nil }
	return nil 
}`
	fset := token.NewFileSet()
	astFile, _ := parser.ParseFile(fset, "main.go", src, 0)
	dstFile, _ := decorator.NewDecorator(fset).DecorateFile(astFile)

	inj.Pkg = &packages.Package{
		TypesInfo: &types.Info{
			Types: make(map[ast.Expr]types.TypeAndValue),
		},
	}
	ast.Inspect(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			inj.Pkg.TypesInfo.Types[c] = types.TypeAndValue{Type: types.Universe.Lookup("error").Type()}
		}
		return true
	})

	applied, err := inj.RewriteDefers(dstFile, astFile)
	if err != nil {
		t.Errorf("expected no err")
	}
	if !applied {
		t.Errorf("expected applied true")
	}
}

func TestRewriteDefers_NoErrorName(t *testing.T) {
	// A test where getErrorReturnNameDST returns ""
	inj := &Injector{}
	src := `package main
func Target() { 
	defer func() {}()
}`
	fset := token.NewFileSet()
	astFile, _ := parser.ParseFile(fset, "main.go", src, 0)
	dstFile, _ := decorator.NewDecorator(fset).DecorateFile(astFile)

	inj.Pkg = &packages.Package{
		TypesInfo: &types.Info{
			Types: make(map[ast.Expr]types.TypeAndValue),
		},
	}
	ast.Inspect(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			inj.Pkg.TypesInfo.Types[c] = types.TypeAndValue{Type: types.Universe.Lookup("error").Type()}
		}
		return true
	})

	applied, err := inj.RewriteDefers(dstFile, astFile)
	if err != nil {
		t.Errorf("expected no err")
	}
	if applied {
		t.Errorf("expected applied false")
	}
}

func TestRewriteDefers_FuncLit_NoErrorName(t *testing.T) {
	inj := &Injector{}
	src := `package main
func Target() { 
	_ = func() { defer func() {}() }
}`
	fset := token.NewFileSet()
	astFile, _ := parser.ParseFile(fset, "main.go", src, 0)
	dstFile, _ := decorator.NewDecorator(fset).DecorateFile(astFile)

	inj.Pkg = &packages.Package{
		TypesInfo: &types.Info{
			Types: make(map[ast.Expr]types.TypeAndValue),
		},
	}
	ast.Inspect(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			inj.Pkg.TypesInfo.Types[c] = types.TypeAndValue{Type: types.Universe.Lookup("error").Type()}
		}
		return true
	})

	applied, err := inj.RewriteDefers(dstFile, astFile)
	if err != nil {
		t.Errorf("expected no err")
	}
	if applied {
		t.Errorf("expected applied false")
	}
}

func TestRewriteDefers_FuncLit_Changed(t *testing.T) {
	oldEnsure := ensureNamedReturnsDST
	ensureNamedReturnsDST = func(fn *dst.FuncDecl) (bool, error) {
		if fn.Name == nil { // FuncLit wrapper check
			return true, nil
		}
		return false, nil
	}
	defer func() { ensureNamedReturnsDST = oldEnsure }()

	inj := &Injector{}
	src := `package main
func Target() { 
	_ = func() error { defer func() {}(); return nil }
}`
	fset := token.NewFileSet()
	astFile, _ := parser.ParseFile(fset, "main.go", src, 0)
	dstFile, _ := decorator.NewDecorator(fset).DecorateFile(astFile)

	inj.Pkg = &packages.Package{
		TypesInfo: &types.Info{
			Types: make(map[ast.Expr]types.TypeAndValue),
		},
	}
	ast.Inspect(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			inj.Pkg.TypesInfo.Types[c] = types.TypeAndValue{Type: types.Universe.Lookup("error").Type()}
		}
		return true
	})

	applied, err := inj.RewriteDefers(dstFile, astFile)
	if err != nil {
		t.Errorf("expected no err")
	}
	if !applied {
		t.Errorf("expected applied true")
	}
}

func TestRewriteDefers_FuncLit_FindDstNode_NotFuncLit(t *testing.T) {
	// Force findDstNodeFunc to return a node that is NOT a *dst.FuncLit when mapping
	oldFind := findDstNodeFunc
	findDstNodeFunc = func(fset *token.FileSet, dstFile *dst.File, astFile *ast.File, target ast.Node) (DstMapResult, error) {
		if _, ok := target.(*ast.FuncLit); ok {
			return DstMapResult{Node: &dst.Ident{}}, nil // wrong type
		}
		return oldFind(fset, dstFile, astFile, target)
	}
	defer func() { findDstNodeFunc = oldFind }()

	inj := &Injector{}
	src := `package main
func Target() { 
	_ = func() error { defer func() {}(); return nil }
}`
	fset := token.NewFileSet()
	astFile, _ := parser.ParseFile(fset, "main.go", src, 0)
	dstFile, _ := decorator.NewDecorator(fset).DecorateFile(astFile)

	inj.Pkg = &packages.Package{
		TypesInfo: &types.Info{
			Types: make(map[ast.Expr]types.TypeAndValue),
		},
	}
	ast.Inspect(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			inj.Pkg.TypesInfo.Types[c] = types.TypeAndValue{Type: types.Universe.Lookup("error").Type()}
		}
		return true
	})

	applied, err := inj.RewriteDefers(dstFile, astFile)
	if err != nil {
		t.Errorf("expected no err")
	}
	if applied {
		t.Errorf("expected applied false")
	}
}

func TestRewriteDefers_FuncDecl_FindDstNode_NotFuncDecl(t *testing.T) {
	oldFind := findDstNodeFunc
	findDstNodeFunc = func(fset *token.FileSet, dstFile *dst.File, astFile *ast.File, target ast.Node) (DstMapResult, error) {
		if _, ok := target.(*ast.FuncDecl); ok {
			return DstMapResult{Node: &dst.Ident{}}, nil // wrong type
		}
		return oldFind(fset, dstFile, astFile, target)
	}
	defer func() { findDstNodeFunc = oldFind }()

	inj := &Injector{}
	src := `package main
func Target() error { 
	defer func() {}()
	return nil 
}`
	fset := token.NewFileSet()
	astFile, _ := parser.ParseFile(fset, "main.go", src, 0)
	dstFile, _ := decorator.NewDecorator(fset).DecorateFile(astFile)

	inj.Pkg = &packages.Package{
		TypesInfo: &types.Info{
			Types: make(map[ast.Expr]types.TypeAndValue),
		},
	}
	ast.Inspect(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			inj.Pkg.TypesInfo.Types[c] = types.TypeAndValue{Type: types.Universe.Lookup("error").Type()}
		}
		return true
	})

	applied, err := inj.RewriteDefers(dstFile, astFile)
	if err != nil {
		t.Errorf("expected no err")
	}
	if applied {
		t.Errorf("expected applied false")
	}
}

func TestNormalizeToNakedReturns_NakedReturn(t *testing.T) {
	inj := &Injector{}
	block := &dst.BlockStmt{
		List: []dst.Stmt{
			&dst.ReturnStmt{
				Results: nil,
			},
		},
	}
	results := &dst.FieldList{
		List: []*dst.Field{
			{Names: []*dst.Ident{dst.NewIdent("err")}, Type: dst.NewIdent("error")},
		},
	}
	inj.normalizeToNakedReturns(block, results)
}
