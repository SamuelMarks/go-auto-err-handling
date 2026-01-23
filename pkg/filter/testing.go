package filter

import (
	"go/ast"
	"strings"
	"unicode"
)

// IsTestHandler checks if the provided function declaration matches a standard Go testing entry point signature.
//
// It detects:
//   - Unit Tests: func TestXxx(t *testing.T)
//   - Benchmarks: func BenchmarkXxx(b *testing.B)
//   - Fuzz Tests: func FuzzXxx(f *testing.F)
//   - Test Main:  func TestMain(m *testing.M)
//   - Examples:   func ExampleXxx()
//
// These functions are critical because their signatures cannot be modified (e.g., adding an error return)
// without breaking the 'go test' runner.
//
// decl: The function declaration to inspect.
//
// Returns true if the function is a testing entry point.
func IsTestHandler(decl *ast.FuncDecl) bool {
	if decl == nil || decl.Name == nil {
		return false
	}

	name := decl.Name.Name

	// 1. Example functions (no parameters, usually)
	if strings.HasPrefix(name, "Example") {
		// ExampleXxx where Xxx does not start with lower case
		if isValidSuffix(name, "Example") {
			// Examples typically have 0 arguments.
			if decl.Type.Params.NumFields() == 0 {
				return true
			}
		}
		// Fallthrough: Example with arguments is not a standard test example entry point?
		// Actually, standard examples have 0 params.
	}

	// 2. TestMain (func TestMain(m *testing.M))
	if name == "TestMain" {
		return isTestingFunc(decl, "M")
	}

	// 3. Test (func TestXxx(t *testing.T))
	if strings.HasPrefix(name, "Test") {
		if isValidSuffix(name, "Test") {
			return isTestingFunc(decl, "T")
		}
	}

	// 4. Benchmark (func BenchmarkXxx(b *testing.B))
	if strings.HasPrefix(name, "Benchmark") {
		if isValidSuffix(name, "Benchmark") {
			return isTestingFunc(decl, "B")
		}
	}

	// 5. Fuzz (func FuzzXxx(f *testing.F))
	if strings.HasPrefix(name, "Fuzz") {
		if isValidSuffix(name, "Fuzz") {
			return isTestingFunc(decl, "F")
		}
	}

	return false
}

// GetTestingParamName returns the name of the testing parameter (e.g., "t", "b", "f") if the
// function is a valid test handler.
//
// This is useful for refactoring tools that need to inject "t.Fatal(err)" calls instead of returns.
//
// decl: The function declaration.
//
// Returns the identifier name of the first parameter if it is a testing type, or empty string.
func GetTestingParamName(decl *ast.FuncDecl) string {
	if !IsTestHandler(decl) {
		return ""
	}
	if decl.Type.Params.NumFields() > 0 {
		field := decl.Type.Params.List[0]
		if len(field.Names) > 0 {
			return field.Names[0].Name
		}
	}
	return ""
}

// isValidSuffix checks the "Xxx" part of "TestXxx".
// According to Go spec, Xxx must not start with a lower case letter.
// e.g. "Test" is valid (suffix empty). "Test1" is valid. "Test_foo" is valid. "Testfoo" (lower 'f') is invalid.
func isValidSuffix(name, prefix string) bool {
	if len(name) == len(prefix) {
		return true // e.g. "Test"
	}
	suffix := name[len(prefix):]
	r := rune(suffix[0])
	return !unicode.IsLower(r)
}

// isTestingFunc checks if the function has exactly one parameter of type *testing.<typeName>.
func isTestingFunc(decl *ast.FuncDecl, typeName string) bool {
	// Must have exactly 1 parameter (or list of params that resolves to 1 argument)
	if decl.Type.Params.NumFields() != 1 {
		return false
	}

	field := decl.Type.Params.List[0]
	// AST allows "func(a, b int)", so we must ensure it's a single variable
	if len(field.Names) > 1 {
		return false
	}

	// Check type: must be *testing.typeName
	starExpr, ok := field.Type.(*ast.StarExpr)
	if !ok {
		return false
	}

	selExpr, ok := starExpr.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	// Check package "testing"
	if pkgIdent, ok := selExpr.X.(*ast.Ident); ok {
		if pkgIdent.Name != "testing" {
			return false
		}
	} else {
		return false
	}

	// Check type name ("T", "B", "F", "M")
	return selExpr.Sel.Name == typeName
}
