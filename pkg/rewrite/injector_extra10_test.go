package rewrite

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"testing"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/analysis"
	"github.com/dave/dst"
	"golang.org/x/tools/go/packages"
)

func TestRewriteFile_Branches(t *testing.T) {
	pkg := &packages.Package{
		TypesInfo: &types.Info{
			Defs: make(map[*ast.Ident]types.Object),
			Uses: make(map[*ast.Ident]types.Object),
		},
		Fset: token.NewFileSet(),
	}
	injector := NewInjector(pkg, "", "panic", false)
	dstFile := &dst.File{}
	astFile := &ast.File{Name: &ast.Ident{Name: "test", NamePos: 2}}

	// Mock FindDstNode temporarily
	oldFindDstNode := findDstNodeFunc
	defer func() { findDstNodeFunc = oldFindDstNode }()
	findDstNodeFunc = func(fset *token.FileSet, dstFile *dst.File, astFile *ast.File, targetNode ast.Node) (DstMapResult, error) {
		if _, ok := targetNode.(*ast.EmptyStmt); ok {
			// Return a non-stmt node
			return DstMapResult{Node: &dst.Ident{}}, nil
		}
		if _, ok := targetNode.(*ast.IncDecStmt); ok {
			return DstMapResult{Node: &dst.IncDecStmt{}}, nil
		}
		if ident, ok := targetNode.(*ast.Ident); ok && ident.Name == "trigger_err" {
			return DstMapResult{}, fmt.Errorf("mock error")
		}
		if ident, ok := targetNode.(*ast.Ident); ok && ident.Name == "trigger_not_ok" {
			return DstMapResult{Node: &dst.Ident{}}, nil // not a CallExpr
		}
		return oldFindDstNode(fset, dstFile, astFile, targetNode)
	}

	points := []analysis.InjectionPoint{
		{Stmt: nil},
		{Stmt: &ast.DeferStmt{}},
		{Stmt: &ast.ExprStmt{X: &ast.CallExpr{Fun: &ast.Ident{NamePos: 1}}}}, // Can't be mapped
		{Stmt: &ast.EmptyStmt{}}, // Triggers !ok for stmt
		{Stmt: &ast.IncDecStmt{}, Call: &ast.CallExpr{Fun: &ast.Ident{Name: "trigger_err", NamePos: 1}}},
		{Stmt: &ast.IncDecStmt{}, Call: &ast.CallExpr{Fun: &ast.Ident{Name: "trigger_not_ok", NamePos: 1}}},
	}

	_, err := injector.RewriteFile(dstFile, astFile, points)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
