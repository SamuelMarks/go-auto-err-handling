package refactor

import (
	"go/ast"
	"go/token"
	"go/types"
	"testing"

	"github.com/dave/dst"
	"golang.org/x/tools/go/packages"
)

func TestHandleEntryPoint_Errors(t *testing.T) {
	fset := token.NewFileSet()
	f := fset.AddFile("test.go", -1, 100)
	astFile := &ast.File{
		Package: f.Pos(1),
		Name:    &ast.Ident{Name: "testtesttesttesttest", NamePos: f.Pos(2)},
	}
	pkg := &packages.Package{
		Fset:   fset,
		Syntax: []*ast.File{astFile},
	}
	dstFile := &dst.File{}

	call := &ast.CallExpr{Fun: &ast.Ident{Name: "test", NamePos: f.Pos(10)}}
	stmt := &ast.ExprStmt{X: call}
	// Make stmt discoverable by putting it in astFile.Decls!
	// Wait, astFile.Decls needs an ast.Decl. stmt is an ExprStmt.
	// Let's wrap it in a FuncDecl.
	funcDecl := &ast.FuncDecl{
		Name: &ast.Ident{Name: "f", NamePos: f.Pos(3)},
		Body: &ast.BlockStmt{
			List: []ast.Stmt{stmt},
		},
	}
	astFile.Decls = []ast.Decl{funcDecl}

	err := HandleEntryPoint(pkg, dstFile, call, stmt, "panic")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	call2 := &ast.CallExpr{Fun: &ast.Ident{Name: "test", NamePos: f.Pos(100)}}
	stmt2 := &ast.ExprStmt{X: call2}
	err2 := HandleEntryPoint(pkg, dstFile, call2, stmt2, "panic")
	if err2 == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestHandleTestError_Errors(t *testing.T) {
	fset := token.NewFileSet()
	f := fset.AddFile("test.go", -1, 100)
	astFile := &ast.File{
		Package: f.Pos(1),
		Name:    &ast.Ident{Name: "testtesttesttesttest", NamePos: f.Pos(2)},
	}
	pkg := &packages.Package{
		Fset:   fset,
		Syntax: []*ast.File{astFile},
	}
	dstFile := &dst.File{}

	call := &ast.CallExpr{Fun: &ast.Ident{Name: "test", NamePos: f.Pos(10)}}
	stmt := &ast.ExprStmt{X: call}
	funcDecl := &ast.FuncDecl{
		Name: &ast.Ident{Name: "f", NamePos: f.Pos(3)},
		Body: &ast.BlockStmt{
			List: []ast.Stmt{stmt},
		},
	}
	astFile.Decls = []ast.Decl{funcDecl}

	err := HandleTestError(pkg, dstFile, call, stmt, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	call2 := &ast.CallExpr{Fun: &ast.Ident{Name: "test", NamePos: f.Pos(100)}}
	stmt2 := &ast.ExprStmt{X: call2}
	err2 := HandleTestError(pkg, dstFile, call2, stmt2, "")
	if err2 == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRefactorIfStmt_Errors(t *testing.T) {
	call := &dst.CallExpr{}
	ifStmt := &dst.IfStmt{Cond: &dst.CallExpr{}}
	ctx := rewriteContext{
		call: nil, // makes containsDstNode return false
	}
	err := refactorIfStmt(ctx, ifStmt)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Test Init branch
	ifStmtInit := &dst.IfStmt{Init: &dst.AssignStmt{Rhs: []dst.Expr{call}}}
	ctxInit := rewriteContext{
		call:   call,
		parent: &dst.BlockStmt{List: []dst.Stmt{ifStmtInit}},
	}
	_ = refactorIfStmt(ctxInit, ifStmtInit)
	// Test Cond branch
	ifStmtCond := &dst.IfStmt{Cond: call}
	ctxCond := rewriteContext{
		call:   call,
		parent: &dst.BlockStmt{List: []dst.Stmt{ifStmtCond}},
	}
	_ = refactorIfStmt(ctxCond, ifStmtCond)
}

func TestRefactorSwitchStmt_Errors(t *testing.T) {
	call := &dst.CallExpr{}
	swStmt := &dst.SwitchStmt{Tag: &dst.CallExpr{}}
	ctx := rewriteContext{
		call: nil,
	}
	err := refactorSwitchStmt(ctx, swStmt)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Test Init branch
	swStmtInit := &dst.SwitchStmt{Init: &dst.AssignStmt{Rhs: []dst.Expr{call}}}
	ctxInit := rewriteContext{
		call:   call,
		parent: &dst.BlockStmt{List: []dst.Stmt{swStmtInit}},
	}
	_ = refactorSwitchStmt(ctxInit, swStmtInit)
	// Test Tag branch
	swStmtTag := &dst.SwitchStmt{Tag: call}
	ctxTag := rewriteContext{
		call:   call,
		parent: &dst.BlockStmt{List: []dst.Stmt{swStmtTag}},
	}
	_ = refactorSwitchStmt(ctxTag, swStmtTag)
}

func TestRefactorTypeSwitchStmt_Errors(t *testing.T) {
	call := &dst.CallExpr{}
	swStmt := &dst.TypeSwitchStmt{Assign: &dst.AssignStmt{}}
	ctx := rewriteContext{
		call: nil,
	}
	err := refactorTypeSwitchStmt(ctx, swStmt)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Test Init branch
	swStmtInit := &dst.TypeSwitchStmt{Init: &dst.AssignStmt{Rhs: []dst.Expr{call}}}
	ctxInit := rewriteContext{
		call:   call,
		parent: &dst.BlockStmt{List: []dst.Stmt{swStmtInit}},
	}
	_ = refactorTypeSwitchStmt(ctxInit, swStmtInit)

	// Test Assign branch (AssignStmt)
	swStmtAssign := &dst.TypeSwitchStmt{Assign: &dst.AssignStmt{Rhs: []dst.Expr{&dst.TypeAssertExpr{X: call}}}}
	ctxAssign := rewriteContext{
		call:   call,
		parent: &dst.BlockStmt{List: []dst.Stmt{swStmtAssign}},
	}
	_ = refactorTypeSwitchStmt(ctxAssign, swStmtAssign)

	// Test Assign branch (ExprStmt)
	swStmtExpr := &dst.TypeSwitchStmt{Assign: &dst.ExprStmt{X: &dst.TypeAssertExpr{X: call}}}
	ctxExpr := rewriteContext{
		call:   call,
		parent: &dst.BlockStmt{List: []dst.Stmt{swStmtExpr}},
	}
	_ = refactorTypeSwitchStmt(ctxExpr, swStmtExpr)
}

func TestLiftCallAndCheck_IsNameSafe(t *testing.T) {
	namedType := types.NewNamed(types.NewTypeName(token.NoPos, nil, "MyType", nil), types.NewStruct(nil, nil), nil)
	ctx := rewriteContext{
		isTerminal:   false,
		enclosingSig: types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(types.NewVar(0, nil, "res", namedType), types.NewVar(0, nil, "err", types.Universe.Lookup("error").Type())), false),
		scope:        types.NewScope(nil, token.NoPos, token.NoPos, "test"),
		pos:          0,
		dstFile:      &dst.File{},
	}
	v := types.NewVar(token.NoPos, nil, "res", types.Typ[types.Int])
	ctx.scope.Insert(v)

	call := &dst.CallExpr{Fun: &dst.Ident{Name: "test"}}
	// 1. obj is nil
	res1 := liftCallAndCheck(ctx, call, false)
	t.Logf("Got %d stmts", len(res1))

	// 2. obj is expected
	ctx.scope.Insert(types.NewTypeName(token.NoPos, nil, "MyType", nil)) // fake object
	// Wait, namedType.Obj() is the expected object!
	ctx.scope.Insert(namedType.Obj())
	_ = liftCallAndCheck(ctx, call, false)

	// 3. scope is nil
	ctx.scope = nil
	_ = liftCallAndCheck(ctx, call, false)
}
