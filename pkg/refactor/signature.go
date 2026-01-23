package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ast/astutil"
)

// AddErrorToSignature modifies a function declaration signature to include an error return type.
// It performs smart anonymization of return values to prefer idiomatic Go signatures
// (e.g., "func() (int, error)" instead of "func() (i int, err error)") when logically safe.
//
// The logic is as follows:
// 1. Appends 'error' to the results list.
// 2. Checks if named returns are strictly necessary (i.e., if "naked returns" are used in the body).
// 3. If naked returns exist, it preserves all parameter names and ensures the new error is named (e.g., "err").
// 4. If no naked returns exist, it "anonymizes" the signature:
//   - Removes names from existing result fields.
//   - If a removed name was referenced in the function body, it injects a "var <name> <type>"
//     declaration at the top of the function to maintain validity.
//
// 5. Updates all explicitly returned values to include "nil" for the error position.
//
// fset: The FileSet for position handling.
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

	// Clear positions to allow reformatting
	decl.Type.Results.Opening = token.NoPos
	decl.Type.Results.Closing = token.NoPos

	// 1. Analyze Context: Do we have named returns? Are they naked?
	hasNamedReturns := false
	for _, field := range decl.Type.Results.List {
		if len(field.Names) > 0 {
			hasNamedReturns = true
			break
		}
	}

	hasNakedReturns := false
	if hasNamedReturns {
		hasNakedReturns = scanForNakedReturns(decl.Body)
	}

	// 2. Strategy Decision
	preserveNames := hasNakedReturns
	wasVoid := len(decl.Type.Results.List) == 0

	// 3. Apply Anonymization (if applicable)
	var injectedStmts []ast.Stmt

	if hasNamedReturns && !preserveNames {
		var newResultList []*ast.Field

		for _, field := range decl.Type.Results.List {
			typeExpr := field.Type

			// Handle multi-name fields like "func() (a, b int)"
			for _, name := range field.Names {
				// Inject var if used
				if isNameUsed(decl.Body, name.Name) {
					declStmt := &ast.DeclStmt{
						Decl: &ast.GenDecl{
							Tok: token.VAR,
							Specs: []ast.Spec{
								&ast.ValueSpec{
									Names: []*ast.Ident{{Name: name.Name}},
									Type:  typeExpr,
								},
							},
						},
					}
					injectedStmts = append(injectedStmts, declStmt)
				}
				// Append anonymous field for THIS specific variable types.
				newResultList = append(newResultList, &ast.Field{
					Type: typeExpr, // Sharing pointer is fine for AST logic, printer handles it.
				})
			}
		}

		decl.Type.Results.List = newResultList
		hasNamedReturns = false
	}

	// 4. Prepend Injected Variables (if any)
	if len(injectedStmts) > 0 {
		decl.Body.List = append(injectedStmts, decl.Body.List...)
	}

	// 5. Append 'error' field
	errorType := &ast.Ident{Name: "error"}
	var newField *ast.Field

	if hasNamedReturns {
		// Calculate safe name avoiding collisions
		usedNames := make(map[string]bool)
		if decl.Type.Params != nil {
			for _, f := range decl.Type.Params.List {
				for _, n := range f.Names {
					usedNames[n.Name] = true
				}
			}
		}
		for _, f := range decl.Type.Results.List {
			for _, n := range f.Names {
				usedNames[n.Name] = true
			}
		}

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

	// 6. Update Return Statements
	astutil.Apply(decl.Body, func(c *astutil.Cursor) bool {
		node := c.Node()

		// Skip nested function literals or declarations
		if _, isFuncLit := node.(*ast.FuncLit); isFuncLit {
			return false
		}
		if _, isFuncDecl := node.(*ast.FuncDecl); isFuncDecl {
			return false
		}

		// Handle Return Statements
		if ret, isRet := node.(*ast.ReturnStmt); isRet {
			isNaked := hasNamedReturns && len(ret.Results) == 0

			if !isNaked && (len(ret.Results) > 0 || wasVoid) {
				ret.Results = append(ret.Results, &ast.Ident{Name: "nil"})
			}
			return false
		}
		return true
	}, nil)

	// 7. Handle Void Fallthrough
	if wasVoid {
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

// scanForNakedReturns traverses the block to find any empty return statements.
// Does not descend into nested functions.
//
// body: The function body to scan.
//
// Returns true if a naked return is found.
func scanForNakedReturns(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		// Don't modify context of nested functions
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if _, ok := n.(*ast.FuncDecl); ok {
			return false
		}

		if ret, ok := n.(*ast.ReturnStmt); ok {
			if len(ret.Results) == 0 {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// isNameUsed checks if a specific identifier name is referenced in the block.
// Used to determine if a stripped return parameter needs a local variable declaration.
//
// body: The block to scan.
// name: The identifier name to look for.
//
// Returns true if the name is used.
func isNameUsed(body *ast.BlockStmt, name string) bool {
	if body == nil {
		return false
	}
	used := false
	ast.Inspect(body, func(n ast.Node) bool {
		if used {
			return false
		}
		// Don't confuse scope with nested functions
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if _, ok := n.(*ast.FuncDecl); ok {
			return false
		}

		if id, ok := n.(*ast.Ident); ok {
			if id.Name == name {
				used = true
				return false
			}
		}
		return true
	})
	return used
}

// isErrorType checks if the AST expression represents the "error" type.
// It performs a simple string check on the identifier.
//
// expr: The AST expression to check.
//
// Returns true if the expression represents a standard error type.
func isErrorType(expr ast.Expr) bool {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name == "error"
	}
	// Also check qualified "builtin.error" or similar if ast contains it?
	// Usually just "error" in signatures.
	return false
}
