package filter

import (
	"go/ast"
)

// IsTestHelper determines if the function declaration behaves as a test helper.
// A function is considered a test helper if it calls the `Helper()` method on a parameter
// of type `*testing.T`, `*testing.B`, or `*testing.F`.
//
// This detection allows refactoring tools to choose `t.Fatal(err)` injection over
// signature modification, preserving the helper's signature (which often cannot return errors
// without breaking test suite patterns).
//
// decl: The function declaration to inspect.
//
// Returns true if `Helper()` is called, and the name of the testing parameter (e.g. "t").
func IsTestHelper(decl *ast.FuncDecl) (bool, string) {
	if decl == nil || decl.Body == nil {
		return false, ""
	}

	// 1. Identify the testing parameter name (e.g., "t", "b").
	// We look for a parameter pattern: func Helper(t *testing.T, ...)
	testParamName := ""

	if decl.Type.Params != nil {
		for _, field := range decl.Type.Params.List {
			if isTestingType(field.Type) {
				if len(field.Names) > 0 {
					testParamName = field.Names[0].Name
					break // Found the test controller
				}
			}
		}
	}

	if testParamName == "" {
		return false, ""
	}

	// 2. Scan the body for `param.Helper()` call.
	hasHelperCall := false
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		if hasHelperCall {
			return false
		}
		// Don't descend into nested functions (literals) as they have their own scope
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}

		if call, ok := n.(*ast.CallExpr); ok {
			if isHelperMethodCall(call, testParamName) {
				hasHelperCall = true
				return false
			}
		}
		return true
	})

	return hasHelperCall, testParamName
}

// isTestingType checks if the AST expression corresponds to *testing.T, *testing.B, etc.
//
// expr: The type expression from the field list.
func isTestingType(expr ast.Expr) bool {
	// Must be a pointer: *testing.T
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	if pkg.Name != "testing" {
		return false
	}
	// Verify common testing types
	switch sel.Sel.Name {
	case "T", "B", "F":
		return true
	}
	return false
}

// isHelperMethodCall checks if the call expression is `paramName.Helper()`.
//
// call: The call expression to check.
// paramName: The name of the testing parameter context (e.g., "t").
func isHelperMethodCall(call *ast.CallExpr, paramName string) bool {
	// Look for SelectorExpr like `t.Helper`
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	// Check Method Name
	if sel.Sel.Name != "Helper" {
		return false
	}
	// Check Receiver Name
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return id.Name == paramName
}
