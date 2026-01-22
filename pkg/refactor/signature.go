package refactor

import (
	"fmt"
	"go/ast"
	"go/token"

	"golang.org/x/tools/go/ast/astutil"
)

// AddErrorToSignature modifies a function declaration signature to include an error return type.
// It appends 'error' to the results list and updates all return statements within the function body
// to include a 'nil' value for the new error return.
//
// Examples:
//
//	func Foo()              -> func Foo() error
//	func Bar() int          -> func Bar() (int, error)
//	func Baz() (i int)      -> func Baz() (i int, err error)
//
// Nested function literals and declarations are ignored.
// If the function contained no return values (void), a 'return nil' statement is appended to the body
// to ensure valid control flow, unless the last statement is already a return.
//
// fset: The FileSet (unused but provided for compatibility/future use).
// decl: The function declaration to modify.
//
// Returns true if the signature was modified.
func AddErrorToSignature(fset *token.FileSet, decl *ast.FuncDecl) (bool, error) {
	if decl == nil {
		return false, fmt.Errorf("function declaration is nil")
	}

	// Initialize Results if nil (e.g., void function)
	if decl.Type.Results == nil {
		decl.Type.Results = &ast.FieldList{}
	}

	// Clear positions on Results to allow go/format to re-layout correctly
	// ignoring potentially stale comma positions.
	decl.Type.Results.Opening = token.NoPos
	decl.Type.Results.Closing = token.NoPos

	// 1. Analyze existing signature to see if we use named returns
	hasNamedReturns := false
	existingResults := decl.Type.Results.List
	wasVoid := len(existingResults) == 0

	for _, field := range existingResults {
		if len(field.Names) > 0 {
			hasNamedReturns = true
			break
		}
	}

	// 2. Modify Signature: Append 'error'
	errorType := &ast.Ident{Name: "error"}
	var newField *ast.Field

	if hasNamedReturns {
		// If using named returns, the new error must also be named to avoid syntax errors
		newField = &ast.Field{
			Names: []*ast.Ident{{Name: "err"}},
			Type:  errorType,
		}
	} else {
		newField = &ast.Field{
			Type: errorType,
		}
	}

	decl.Type.Results.List = append(decl.Type.Results.List, newField)

	// 3. Update Return Statements
	// We use astutil.Apply to traverse. We must skip nested functions.
	astutil.Apply(decl.Body, func(c *astutil.Cursor) bool {
		node := c.Node()

		// Skip nested function literals or declarations to avoid modifying their returns
		if _, isFuncLit := node.(*ast.FuncLit); isFuncLit {
			return false
		}
		if _, isFuncDecl := node.(*ast.FuncDecl); isFuncDecl {
			return false
		}

		// Handle Return Statements
		if ret, isRet := node.(*ast.ReturnStmt); isRet {
			// Logic for updating returns:
			// - If named returns are used, result list can be empty (naked return).
			//   In that case, the new 'err' var is implicitly returned as nil. No change needed.
			// - If result list is NOT empty (explicit return), we must append 'nil'.
			// - If it was previously void, result list is empty. We append 'nil'.

			if len(ret.Results) > 0 || wasVoid {
				// Append 'nil'
				ret.Results = append(ret.Results, &ast.Ident{Name: "nil"})
			}
			// Stop traversing children of the return statement
			return false
		}
		return true
	}, nil)

	// 4. Handle Void Fallthrough
	// If the function was void, it might not have an explicit return statement at the end.
	// We append "return nil" to the body to ensure it returns the error.
	if wasVoid {
		// Check if the last statement is already a return (optional visual optimization, though explicitly appending is strictly safer against branching fallthrough)
		needsAppend := true
		if len(decl.Body.List) > 0 {
			if _, ok := decl.Body.List[len(decl.Body.List)-1].(*ast.ReturnStmt); ok {
				needsAppend = false
			}
		}

		if needsAppend {
			decl.Body.List = append(decl.Body.List, &ast.ReturnStmt{
				Results: []ast.Expr{&ast.Ident{Name: "nil"}},
			})
		}
	}

	return true, nil
}

// EnsureNamedReturns inspects the function declaration and ensures all return values are named.
// If the function definition already uses named returns, no changes are made.
// If the function uses unnamed returns, it generates names for them (e.g., ret0, ret1, ..., err).
//
// This is crucial for deferred closures to capture and modify return values.
//
// fset: The FileSet (unused but provided for compatibility).
// decl: The function declaration to modify.
//
// Returns true if any changes were made.
func EnsureNamedReturns(fset *token.FileSet, decl *ast.FuncDecl) (bool, error) {
	if decl == nil {
		return false, fmt.Errorf("function declaration is nil")
	}
	if decl.Type.Results == nil || len(decl.Type.Results.List) == 0 {
		return false, nil
	}

	results := decl.Type.Results.List

	// Check if already named. Go forbids mixing named and unnamed, so check the first one.
	// However, the AST might theoretically interpret "(int, int)" as two fields with empty Names.
	if len(results[0].Names) > 0 {
		return false, nil
	}

	changeMade := false
	retCount := 0
	total := len(results)

	for i, field := range results {
		// Field should not have names if we are here (assuming consistent AST from valid Go),
		// but checking acts as a safety against malformed AST or partial states.
		if len(field.Names) == 0 {
			name := fmt.Sprintf("ret%d", retCount)

			// Heuristic: If it is the last return value and it is of type "error", name it "err".
			isLast := i == total-1
			if isLast && isErrorType(field.Type) {
				name = "err"
			} else {
				retCount++
			}

			field.Names = []*ast.Ident{{Name: name}}
			changeMade = true
		} else {
			// Should not occur in valid Go code if first field was unnamed, but handle safely
			retCount += len(field.Names)
		}
	}

	return changeMade, nil
}

// isErrorType checks if the AST expression represents the "error" type.
// It performs a simple string check on the identifier.
func isErrorType(expr ast.Expr) bool {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name == "error"
	}
	return false
}
