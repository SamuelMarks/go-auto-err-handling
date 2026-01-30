package rewrite

import (
	"fmt"
	"go/ast"
	"go/token"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/refactor"
	"github.com/dave/dst"
	"golang.org/x/tools/go/ast/astutil"
)

// RewriteDefers scans the file for defer statements (including inside closures).
// It converts defers that ignore errors into a pattern using errors.Join.
func (i *Injector) RewriteDefers(dstFile *dst.File, astFile *ast.File) (bool, error) {
	if dstFile == nil || astFile == nil {
		return false, fmt.Errorf("files cannot be nil")
	}

	targets := make(map[*ast.FuncDecl][]*ast.DeferStmt)
	litTargets := make(map[*ast.FuncLit][]*ast.DeferStmt)

	type scopeCtx struct {
		decl *ast.FuncDecl
		lit  *ast.FuncLit
	}
	var stack []scopeCtx

	astutil.Apply(astFile, func(c *astutil.Cursor) bool {
		node := c.Node()
		if decl, ok := node.(*ast.FuncDecl); ok {
			stack = append(stack, scopeCtx{decl: decl})
		}
		if lit, ok := node.(*ast.FuncLit); ok {
			stack = append(stack, scopeCtx{lit: lit})
		}
		if deferStmt, ok := node.(*ast.DeferStmt); ok {
			if i.isErrorReturningCall(deferStmt.Call) {
				if len(stack) > 0 {
					current := stack[len(stack)-1]
					if current.decl != nil {
						targets[current.decl] = append(targets[current.decl], deferStmt)
					} else if current.lit != nil {
						litTargets[current.lit] = append(litTargets[current.lit], deferStmt)
					}
				}
			}
		}
		return true
	}, func(c *astutil.Cursor) bool {
		node := c.Node()
		if _, ok := node.(*ast.FuncDecl); ok {
			stack = stack[:len(stack)-1]
		}
		if _, ok := node.(*ast.FuncLit); ok {
			stack = stack[:len(stack)-1]
		}
		return true
	})

	applied := false

	// Process FuncDecls
	for astDecl, defers := range targets {
		res, err := FindDstNode(i.Fset, dstFile, astFile, astDecl)
		if err != nil {
			return applied, err
		}
		dstDecl, ok := res.Node.(*dst.FuncDecl)
		if !ok {
			continue
		}

		if hasAnonymousReturnsDST(dstDecl.Type) {
			continue
		}

		changed, err := refactor.EnsureNamedReturnsDST(dstDecl)
		if err != nil {
			return applied, err
		}
		if changed {
			applied = true
		}

		errName := i.getErrorReturnNameDST(dstDecl.Type)
		if errName == "" {
			continue
		}

		if i.rewriteDefersInDST(dstDecl.Body, defers, astFile, dstFile, errName) {
			applied = true
		}
	}

	// Process FuncLits
	for astLit, defers := range litTargets {
		res, err := FindDstNode(i.Fset, dstFile, astFile, astLit)
		if err != nil {
			return applied, err
		}
		dstLit, ok := res.Node.(*dst.FuncLit)
		if !ok {
			continue
		}

		if hasAnonymousReturnsDST(dstLit.Type) {
			continue
		}

		changed, err := refactor.EnsureNamedReturnsDST(&dst.FuncDecl{Type: dstLit.Type})
		if err != nil {
			return applied, err
		}
		if changed {
			applied = true
		}

		errName := i.getErrorReturnNameDST(dstLit.Type)
		if errName == "" {
			continue
		}

		if i.rewriteDefersInDST(dstLit.Body, defers, astFile, dstFile, errName) {
			applied = true
		}
	}

	return applied, nil
}

func (i *Injector) rewriteDefersInDST(body *dst.BlockStmt, astDefers []*ast.DeferStmt, astFile *ast.File, dstFile *dst.File, errName string) bool {
	changed := false
	for _, astDefer := range astDefers {
		res, err := FindDstNode(i.Fset, dstFile, astFile, astDefer)
		if err != nil {
			continue
		}
		dstDefer, ok := res.Node.(*dst.DeferStmt)
		if !ok {
			continue
		}

		newDefer := i.generateDeferRewriteDST(dstDefer.Call, errName)

		if replaceDstStmt(body, dstDefer, newDefer) {
			changed = true
		}
	}
	return changed
}

func hasAnonymousReturnsDST(ft *dst.FuncType) bool {
	if ft.Results == nil {
		return false
	}
	for _, field := range ft.Results.List {
		if len(field.Names) == 0 {
			return true
		}
	}
	return false
}

func (i *Injector) getErrorReturnNameDST(ft *dst.FuncType) string {
	if ft.Results == nil {
		return ""
	}
	var lastErrName string
	for _, field := range ft.Results.List {
		if isErrorDstExpr(field.Type) {
			for _, name := range field.Names {
				if name.Name == "err" {
					return "err"
				}
				lastErrName = name.Name
			}
		}
	}
	return lastErrName
}

func isErrorDstExpr(expr dst.Expr) bool {
	if id, ok := expr.(*dst.Ident); ok {
		return id.Name == "error"
	}
	return false
}

func (i *Injector) generateDeferRewriteDST(originalCall *dst.CallExpr, errName string) *dst.DeferStmt {
	callClone := dst.Clone(originalCall).(*dst.CallExpr)

	assign := &dst.AssignStmt{
		Lhs: []dst.Expr{dst.NewIdent(errName)},
		Tok: token.ASSIGN,
		Rhs: []dst.Expr{
			&dst.CallExpr{
				Fun: &dst.SelectorExpr{
					X:   dst.NewIdent("errors"),
					Sel: dst.NewIdent("Join"),
				},
				Args: []dst.Expr{
					dst.NewIdent(errName),
					callClone,
				},
			},
		},
	}

	funcLit := &dst.FuncLit{
		Type: &dst.FuncType{
			Params:  &dst.FieldList{},
			Results: nil,
		},
		Body: &dst.BlockStmt{
			List: []dst.Stmt{assign},
		},
	}

	return &dst.DeferStmt{
		Call: &dst.CallExpr{
			Fun: funcLit,
		},
	}
}

// Reuse helper check
func (i *Injector) isErrorReturningCall(call *ast.CallExpr) bool {
	if i.Pkg.TypesInfo == nil {
		return false
	}
	tv, ok := i.Pkg.TypesInfo.Types[call]
	if !ok {
		return false
	}
	if i.isErrorType(tv.Type) {
		return true
	}
	return false
}
