package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ast/astutil"
)

// AddErrorToSignature modifies a function declaration signature to include an error return type.
// It appends 'error' to the results list and updates all return statements within the function body
// to include a 'nil' value for the new error return.
//
// It handles named return value collisions. If 'err' is already used as a parameter or return value name,
// it generates a safe alternative (e.g., 'err1', 'err2').
//
// Examples:
//
//	func Foo()              -> func Foo() error
//	func Bar() int          -> func Bar() (int, error)
//	func Baz() (i int)      -> func Baz() (i int, err error)
//	func Err(err int) (i int) -> func Err(err int) (i int, err1 error)
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

	// 1. Analyze existing elements for naming and collisions
	hasNamedReturns := false
	usedNames := make(map[string]bool)

	// Scan parameters for used names
	if decl.Type.Params != nil {
		for _, field := range decl.Type.Params.List {
			for _, name := range field.Names {
				usedNames[name.Name] = true
			}
		}
	}

	// Scan existing results for used names and check if named returns are used
	existingResults := decl.Type.Results.List
	wasVoid := len(existingResults) == 0

	for _, field := range existingResults {
		if len(field.Names) > 0 {
			hasNamedReturns = true
			for _, name := range field.Names {
				usedNames[name.Name] = true
			}
		}
	}

	// 2. Modify Signature: Append 'error'
	errorType := &ast.Ident{Name: "error"}
	var newField *ast.Field

	if hasNamedReturns {
		// Calculate safe name
		baseName := "err"
		name := baseName
		count := 1
		for usedNames[name] {
			name = fmt.Sprintf("%s%d", baseName, count)
			count++
		}

		newField = &ast.Field{
			Names: []*ast.Ident{{Name: name}},
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
// If the function uses unnamed returns, it generates names for them (e.g., s, i, ctx) using type heuristics.
// It handles name collisions locally (e.g. s, s1, s2).
//
// This is crucial for deferred closures to capture and modify return values.
//
// fset: The FileSet.
// decl: The function declaration to modify.
// info: The type info (optional, can be nil). If provided, richer type-based naming is used.
//
// Returns true if any changes were made.
func EnsureNamedReturns(fset *token.FileSet, decl *ast.FuncDecl, info *types.Info) (bool, error) {
	if decl == nil {
		return false, fmt.Errorf("function declaration is nil")
	}
	if decl.Type.Results == nil || len(decl.Type.Results.List) == 0 {
		return false, nil
	}

	results := decl.Type.Results.List

	// Check if already named. Go forbids mixing named and unnamed, so check the first one.
	if len(results[0].Names) > 0 {
		return false, nil
	}

	changeMade := false
	nameCounts := make(map[string]int)

	total := len(results)
	for i, field := range results {
		// We expect unnamed fields
		if len(field.Names) == 0 {
			var baseName string

			// Heuristic Logic
			// 1. Try TypeInfo
			if info != nil {
				if t, ok := info.Types[field.Type]; ok {
					baseName = NameForType(t.Type)
				}
			}
			// 2. Fallback to AST
			if baseName == "" || baseName == "v" {
				baseName = NameForExpr(field.Type)
			}

			// 3. Special case for trailing error
			// If it's the very last return and it's an error type, force 'err'
			// (NameForType usually returns 'err' for error, but AST fallback might not if type is aliased/unclear)
			isLast := i == total-1
			if isLast && isErrorType(field.Type) {
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

// isErrorType checks if the AST expression represents the "error" type.
// It performs a simple string check on the identifier.
//
// expr: The AST expression to check.
func isErrorType(expr ast.Expr) bool {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name == "error"
	}
	// Also check qualified "builtin.error" or similar if ast contains it?
	// Usually just "error" in signatures.
	return false
}
