package rewrite

import (
	"fmt"
	"go/ast"
	"go/token"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/refactor"
	"golang.org/x/tools/go/ast/astutil"
)

// RewriteDefers scans the file for defer statements (including inside closures).
// It converts defers that ignore errors into a pattern using errors.Join.
//
// Pattern:
//
//	defer f()
//
// Rewrites to:
//
//	defer func() { err = errors.Join(err, f()) }()
//
// The rewriting logic respects scope: defers inside a closure target the return variable of
// that specific closure, not the outer function.
//
// file: The AST file to modify.
//
// Returns true if changes were applied.
func (i *Injector) RewriteDefers(file *ast.File) (bool, error) {
	applied := false
	var err error

	// Traverse the entire file. We handle each function boundary (Decl or Lit) individually.
	astutil.Apply(file, func(c *astutil.Cursor) bool {
		if err != nil {
			return false
		}

		node := c.Node()

		// Identify if we are entering a function scope
		var body *ast.BlockStmt
		var typeField *ast.FuncType

		switch fn := node.(type) {
		case *ast.FuncDecl:
			body = fn.Body
			typeField = fn.Type
		case *ast.FuncLit:
			body = fn.Body
			typeField = fn.Type
		}

		// If not a function or empty body, keep traversing
		if body == nil || typeField == nil {
			return true
		}

		// 1. Check if this function *immediately* contains relevant defers.
		// We use a shallow inspection that does NOT enter nested functions.
		hasTargetDefer := false
		hasDeferCheckErr := false

		// We must inspect the entire body block recursively (e.g. inside if/for),
		// but stop at nested FuncLit/FuncDecl.
		ast.Inspect(body, func(n ast.Node) bool {
			// Stop if we hit a nested function boundary
			if _, isFunc := n.(*ast.FuncLit); isFunc && n != node { // n != node check for safety if inspecting root
				return false
			}
			if _, isDecl := n.(*ast.FuncDecl); isDecl && n != node {
				return false
			}

			if deferStmt, isDefer := n.(*ast.DeferStmt); isDefer {
				if i.isErrorReturningCall(deferStmt.Call) {
					hasTargetDefer = true
					return false // Found one, no need to keep looking in this branch
				}
			}
			return true
		})

		if hasDeferCheckErr {
			// Should ideally handle error from inspect if we generated one,
			// but inspect doesn't support error return.
			// We effectively just skip if we failed to detect properly.
		}

		if !hasTargetDefer {
			return true // Continue traversal to find nested functions
		}

		// 2. Ensure Named Returns for THIS function context
		changedSig, ensureErr := i.ensureNamedReturnsForType(typeField)
		if ensureErr != nil {
			err = ensureErr
			return false
		}
		if changedSig {
			applied = true
		}

		// Verify "err" exists now
		targetErrName := i.getErrorReturnName(typeField)
		if targetErrName == "" {
			return true
		}

		// 3. Rewrite the defers in this function's scope
		// We use a localized Apply that stops at nested functions.
		astutil.Apply(body, func(cSub *astutil.Cursor) bool {
			nSub := cSub.Node()

			// Block nested functions from this pass (they will be visited by the outer Apply)
			if _, isFunc := nSub.(*ast.FuncLit); isFunc {
				return false
			}
			if _, isDecl := nSub.(*ast.FuncDecl); isDecl {
				return false
			}

			if deferStmt, isDefer := nSub.(*ast.DeferStmt); isDefer {
				if i.isErrorReturningCall(deferStmt.Call) {
					newDefer := i.generateDeferRewrite(deferStmt.Call, targetErrName)
					cSub.Replace(newDefer)
					applied = true
				}
			}
			return true
		}, nil)

		return true
	}, nil)

	return applied, err
}

// ensureNamedReturnsForType is a wrapper around refactor logic adjusted for FuncType.
// It names unnamed return values to allow defer capture.
//
// ft: The function type to modify.
func (i *Injector) ensureNamedReturnsForType(ft *ast.FuncType) (bool, error) {
	if ft.Results == nil || len(ft.Results.List) == 0 {
		return false, nil
	}

	// Go requires all returns to be named or all unnamed.
	results := ft.Results.List
	if len(results[0].Names) > 0 {
		return false, nil
	}

	changeMade := false
	nameCounts := make(map[string]int)
	total := len(results)

	for idx, field := range results {
		if len(field.Names) == 0 {
			// Determine name
			baseName := refactor.NameForExpr(field.Type) // default "v", "i", "s"

			// If it is the last return and looks like an error, name it "err" explicitely
			if idx == total-1 && i.isErrorExpr(field.Type) {
				baseName = "err"
			}

			// Collision handling
			name := baseName
			if count, seen := nameCounts[baseName]; seen {
				name = fmt.Sprintf("%s%d", baseName, count)
				nameCounts[baseName] = count + 1
			} else {
				nameCounts[baseName] = 1
			}

			field.Names = []*ast.Ident{{Name: name}}
			changeMade = true
		}
	}
	return changeMade, nil
}

// getErrorReturnName finds the name of the return variable that matches the error type.
// If multiple errors exist, it prioritizes "err".
//
// ft: The function type.
func (i *Injector) getErrorReturnName(ft *ast.FuncType) string {
	if ft.Results == nil {
		return ""
	}

	var lastErrName string

	for _, field := range ft.Results.List {
		if i.isErrorExpr(field.Type) {
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

// generateDeferRewrite creates: defer func() { err = errors.Join(err, f()) }()
//
// originalCall: The call expression being deferred.
// errName: The name of the return variable to join.
func (i *Injector) generateDeferRewrite(originalCall *ast.CallExpr, errName string) *ast.DeferStmt {
	assign := &ast.AssignStmt{
		Lhs: []ast.Expr{&ast.Ident{Name: errName}},
		Tok: token.ASSIGN,
		Rhs: []ast.Expr{
			&ast.CallExpr{
				Fun: &ast.SelectorExpr{
					X:   &ast.Ident{Name: "errors"},
					Sel: &ast.Ident{Name: "Join"},
				},
				Args: []ast.Expr{
					&ast.Ident{Name: errName},
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
