package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/astgen"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// PropagateCallers updates all call sites of a modified function to match its new signature
// (assuming the signature acquired an extra 'error' return value).
//
// It scans the provided packages for usages of the target function. For each call site found,
// it:
// 1. Updates the assignment to receive the new error value (e.g., `x := foo()` -> `x, err := foo()`).
// 2. Checks if the enclosing function returns an error.
// 3. If yes, injects existing Level 0 logic (`if err != nil { return ..., err }`).
// 4. If no, it assigns the error to `_` to maintain compilation validity, leaving further refactoring for subsequent passes.
//
// pkgs: The set of packages to search for callers.
// target: The function object whose signature was modified.
func PropagateCallers(pkgs []*packages.Package, target *types.Func) (int, error) {
	if target == nil {
		return 0, fmt.Errorf("target function is nil")
	}

	updates := 0

	for _, pkg := range pkgs {
		// 1. Find usages
		var callsToUpdate []*ast.Ident
		for id, obj := range pkg.TypesInfo.Uses {
			if obj == target {
				callsToUpdate = append(callsToUpdate, id)
			}
		}

		for _, id := range callsToUpdate {
			// Find the file containing this identifier
			file := findFile(pkg, id.Pos())
			if file == nil {
				continue
			}

			// Get the path to the node
			path, _ := astutil.PathEnclosingInterval(file, id.Pos(), id.Pos())

			// We identify the CallExpr
			var call *ast.CallExpr
			var enclosingStmt ast.Stmt
			var assignment *ast.AssignStmt

			for _, node := range path {
				if c, ok := node.(*ast.CallExpr); ok && call == nil {
					if isIdentFunctionInCall(c, id) {
						call = c
					}
				}
				if stmt, ok := node.(ast.Stmt); ok && call != nil {
					enclosingStmt = stmt
					if as, ok := stmt.(*ast.AssignStmt); ok {
						assignment = as
					}
					break
				}
			}

			if call == nil || enclosingStmt == nil {
				continue
			}

			// Find enclosing function signature to determine if we can inject return
			enclosingSig := findEnclosingSignature(path, pkg.TypesInfo)

			// Perform Refactor on this call site
			if err := refactorCallSite(call, assignment, enclosingStmt, enclosingSig, path[len(path)-1].(*ast.File)); err != nil {
				return updates, err
			}
			updates++
		}
	}

	return updates, nil
}

// isIdentFunctionInCall checks if the identifier acts as the function expression in the call.
func isIdentFunctionInCall(call *ast.CallExpr, id *ast.Ident) bool {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun == id
	case *ast.SelectorExpr:
		return fun.Sel == id
	}
	return false
}

// findFile locates the AST file for a given position.
func findFile(pkg *packages.Package, pos token.Pos) *ast.File {
	for _, f := range pkg.Syntax {
		if f.Pos() <= pos && pos < f.End() {
			return f
		}
	}
	return nil
}

// findEnclosingSignature walks the path upwards to find the function signature.
func findEnclosingSignature(path []ast.Node, info *types.Info) *types.Signature {
	for _, node := range path {
		if fn, ok := node.(*ast.FuncDecl); ok {
			// Get signature from object
			if obj := info.ObjectOf(fn.Name); obj != nil {
				if sig, ok := obj.Type().(*types.Signature); ok {
					return sig
				}
			}
			return nil // Should be found
		}
		if lit, ok := node.(*ast.FuncLit); ok {
			if tv, ok := info.Types[lit]; ok {
				if sig, ok := tv.Type.(*types.Signature); ok {
					return sig
				}
			}
		}
	}
	return nil
}

// refactorCallSite modifies the AST to handle the extra error return.
func refactorCallSite(call *ast.CallExpr, assign *ast.AssignStmt, stmt ast.Stmt, enclosingSig *types.Signature, file *ast.File) error {
	// 1. Determine var name for error
	errName := "err"

	// 2. Modify Assignment / LHS
	if assign != nil {
		// Existing assignment: "a := foo()" or "a, b = foo()"
		// We simply append "err" to LHS.

		// Clone LHS to avoid mutating original slice backing array unpredictably during iteration if shared
		newLHS := make([]ast.Expr, len(assign.Lhs))
		copy(newLHS, assign.Lhs)

		newLHS = append(newLHS, &ast.Ident{Name: errName})

		assign.Lhs = newLHS
	} else {
		// Expression statement: "foo()"
		// Convert to "_, ..., err := foo()" via replacement block
		return replaceStatementInBlock(file, stmt, func() ast.Stmt {
			return nil
		}, enclosingSig, call)
	}

	// 3. Inject Check
	// If existing assignment, we kept the pointer 'assign' valid (modified in place), so we just need to insert 'if' after.
	if assign != nil {
		if canReturnError(enclosingSig) {
			ifStmt, err := generateErrorCheck(enclosingSig)
			if err != nil {
				return err
			}
			cursorReplace(file, stmt, stmt, ifStmt)
		} else {
			// Caller doesn't return error.
			// We modified assignment to include 'err'. If we don't check it, use '_'.
			// Update last LHS to '_'
			assign.Lhs[len(assign.Lhs)-1] = &ast.Ident{Name: "_"}
		}
	}

	return nil
}

// replaceStatementInBlock handles the tricky case where ExprStmt needs to become AssignStmt + IfStmt
func replaceStatementInBlock(file *ast.File, oldStmt ast.Stmt, generator func() ast.Stmt, sig *types.Signature, call *ast.CallExpr) error {
	// We map unhandled returns to "err := " assuming previously void or intentionally ignored.
	as := &ast.AssignStmt{
		Lhs: []ast.Expr{&ast.Ident{Name: "err"}},
		Tok: token.DEFINE,
		Rhs: []ast.Expr{call},
	}

	canReturn := canReturnError(sig)

	astutil.Apply(file, func(c *astutil.Cursor) bool {
		if c.Node() == oldStmt {
			c.Replace(as)
			if canReturn {
				check, _ := generateErrorCheck(sig)
				c.InsertAfter(check)
			} else {
				// Mute usage
				as.Lhs[0] = &ast.Ident{Name: "_"}
				// If we have "_ := foo()", this is invalid Go. Must be "_ = foo()".
				if len(as.Lhs) == 1 {
					as.Tok = token.ASSIGN
				}
			}
			return false // stop
		}
		return true
	}, nil)

	return nil
}

func cursorReplace(file *ast.File, target ast.Stmt, replacement ast.Stmt, insertAfter ast.Stmt) {
	astutil.Apply(file, func(c *astutil.Cursor) bool {
		if c.Node() == target {
			c.Replace(replacement)
			if insertAfter != nil {
				c.InsertAfter(insertAfter)
			}
			return false
		}
		return true
	}, nil)
}

func canReturnError(sig *types.Signature) bool {
	if sig == nil || sig.Results().Len() == 0 {
		return false
	}
	last := sig.Results().At(sig.Results().Len() - 1)
	return last.Type().String() == "error" || last.Type().String() == "builtin.error"
}

func generateErrorCheck(sig *types.Signature) (*ast.IfStmt, error) {
	// return zero, zero, ..., err
	results := sig.Results()
	var retExprs []ast.Expr
	for i := 0; i < results.Len()-1; i++ {
		z, err := astgen.ZeroExpr(results.At(i).Type(), nil)
		if err != nil {
			return nil, err
		}
		retExprs = append(retExprs, z)
	}
	retExprs = append(retExprs, &ast.Ident{Name: "err"})

	return &ast.IfStmt{
		Cond: &ast.BinaryExpr{
			X:  &ast.Ident{Name: "err"},
			Op: token.NEQ,
			Y:  &ast.Ident{Name: "nil"},
		},
		Body: &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.ReturnStmt{Results: retExprs},
			},
		},
	}, nil
}
