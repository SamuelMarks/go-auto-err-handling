package rewrite

import (
	"go/ast"
	"go/token"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/refactor"
	"golang.org/x/tools/go/ast/astutil"
)

// RewriteDefers scans the file for defer statements that discard errors and rewrites them
// to use errors.Join.
//
// Targeted pattern:
//
//	defer f()
//
// rewrite to:
//
//	defer func() { err = errors.Join(err, f()) }()
//
// Requirements:
// 1. The deferred call must return exactly one value of type error.
// 2. The enclosing function must return named error 'err' (automatically enforced).
// 3. The 'errors' package is imported (automatically added by post-processing).
func (i *Injector) RewriteDefers(file *ast.File) (bool, error) {
	applied := false
	var err error

	// We traverse looking for FuncDecls first
	ast.Inspect(file, func(node ast.Node) bool {
		if err != nil {
			return false
		}

		fnDecl, ok := node.(*ast.FuncDecl)
		if !ok {
			return true
		}

		// Check if function has Defers that act on errors
		hasTargetDefer := false
		ast.Inspect(fnDecl.Body, func(n ast.Node) bool {
			if _, isFuncLit := n.(*ast.FuncLit); isFuncLit {
				return false // Skip nested functions
			}
			if deferStmt, isDefer := n.(*ast.DeferStmt); isDefer {
				if i.isErrorReturningCall(deferStmt.Call) {
					hasTargetDefer = true
				}
			}
			return true
		})

		if !hasTargetDefer {
			return false
		}

		// Ensure named returns
		changedSig, ensureErr := refactor.EnsureNamedReturns(i.Fset, fnDecl)
		if ensureErr != nil {
			err = ensureErr
			return false
		}
		if changedSig {
			applied = true
		}

		if !i.hasNamedErrorReturn(fnDecl) {
			return false
		}

		// Rewrite the defers
		astutil.Apply(fnDecl.Body, func(c *astutil.Cursor) bool {
			n := c.Node()
			if _, isFuncLit := n.(*ast.FuncLit); isFuncLit {
				return false
			}

			deferStmt, isDefer := n.(*ast.DeferStmt)
			if !isDefer {
				return true
			}

			if !i.isErrorReturningCall(deferStmt.Call) {
				return true
			}

			// Generate the new defer statement
			newDefer := i.generateDeferRewrite(deferStmt.Call)
			c.Replace(newDefer)
			applied = true

			return false
		}, nil)

		return false
	})

	return applied, err
}

// hasNamedErrorReturn checks if the function has a return variable named "err" of type error.
func (i *Injector) hasNamedErrorReturn(decl *ast.FuncDecl) bool {
	if decl.Type.Results == nil {
		return false
	}
	for _, field := range decl.Type.Results.List {
		for _, name := range field.Names {
			if name.Name == "err" && i.isErrorExpr(field.Type) {
				return true
			}
		}
	}
	return false
}

// generateDeferRewrite creates: defer func() { err = errors.Join(err, originalCall()) }()
func (i *Injector) generateDeferRewrite(originalCall *ast.CallExpr) *ast.DeferStmt {
	assign := &ast.AssignStmt{
		Lhs: []ast.Expr{&ast.Ident{Name: "err"}},
		Tok: token.ASSIGN,
		Rhs: []ast.Expr{
			&ast.CallExpr{
				Fun: &ast.SelectorExpr{
					X:   &ast.Ident{Name: "errors"},
					Sel: &ast.Ident{Name: "Join"},
				},
				Args: []ast.Expr{
					&ast.Ident{Name: "err"},
					originalCall,
				},
			},
		},
	}

	funcLit := &ast.FuncLit{
		Type: &ast.FuncType{
			Params:  &ast.FieldList{},
			Results: nil,
		},
		Body: &ast.BlockStmt{
			List: []ast.Stmt{assign},
		},
	}

	return &ast.DeferStmt{
		Call: &ast.CallExpr{
			Fun: funcLit,
		},
	}
}
