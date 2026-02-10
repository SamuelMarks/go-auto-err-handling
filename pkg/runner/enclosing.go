// Package runner orchestrates analysis, refactoring, and persistence.
package runner

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/filter"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// FuncContext represents the function (Declaration or Literal) enclosing a specific point in code.
// It provides access to the semantic signature and the syntactic AST node required for refactoring.
type FuncContext struct {
	// Sig is the type signature of the function, resolved from type information.
	Sig *types.Signature
	// Decl is set if the enclosing function is a named declaration (e.g. func Foo() {}).
	Decl *ast.FuncDecl
	// Lit is set if the enclosing function is a literal (e.g. func() {}).
	Lit *ast.FuncLit
	// Node is the AST node corresponding to the function (either Decl or Lit).
	Node ast.Node
	// TestParam is the name of the *testing.T (or B/F) parameter if this function is a test handler.
	// This is populated for top-level Test functions AND subtest closures (t.Run).
	TestParam string
}

// IsLiteral reports whether the function context is an anonymous function literal.
func (fc *FuncContext) IsLiteral() bool {
	return fc.Lit != nil
}

// FindEnclosingFunc resolves the nearest function wrapping the provided position in the file.
// It traverses the AST upwards from the position to find either a FuncDecl or a FuncLit.
// It checks for testing contexts (Test functions or t.Run closures) to ensure correct error handling.
//
// pkg: The package containing the file, populated with syntax and type info.
// file: The AST file associated with the position.
// pos: The token position to start the search from.
//
// Returns nil if no function context encloses the position or if type information is missing.
func FindEnclosingFunc(pkg *packages.Package, file *ast.File, pos token.Pos) *FuncContext {
	path, _ := astutil.PathEnclosingInterval(file, pos, pos)

	for i, node := range path {
		switch fn := node.(type) {
		case *ast.FuncDecl:
			// Found a named function declaration.
			if obj := pkg.TypesInfo.ObjectOf(fn.Name); obj != nil {
				if sig, ok := obj.Type().(*types.Signature); ok {
					ctx := &FuncContext{
						Sig:  sig,
						Decl: fn,
						Node: fn,
					}
					// Check if it's a top-level test
					if filter.IsTestHandler(fn) {
						ctx.TestParam = filter.GetTestingParamName(fn)
					}
					return ctx
				}
			}
			return nil

		case *ast.FuncLit:
			// Found an anonymous function literal.
			if tv, ok := pkg.TypesInfo.Types[fn]; ok {
				if sig, ok := tv.Type.(*types.Signature); ok {
					ctx := &FuncContext{
						Sig:  sig,
						Lit:  fn,
						Node: fn,
					}

					// Check if this is a subtest closure (argument to t.Run)
					// We check if the parent node is a CallExpr invoking "Run" on a testing type
					if i+1 < len(path) {
						if call, ok := path[i+1].(*ast.CallExpr); ok {
							if isSubtestCall(call, pkg.TypesInfo) {
								// Check the params of the literal itself for the 't' name
								// func(t *testing.T)
								if len(fn.Type.Params.List) > 0 {
									// Usually only 1 param for t.Run callback
									if len(fn.Type.Params.List[0].Names) > 0 {
										ctx.TestParam = fn.Type.Params.List[0].Names[0].Name
									}
								}
							}
						}
					}
					return ctx
				}
			}
			return nil
		}
	}

	return nil
}

// isSubtestCall checks if the call expression is `t.Run(...)`.
func isSubtestCall(call *ast.CallExpr, info *types.Info) bool {
	// Must be a selector call: t.Run
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel.Name != "Run" {
		return false
	}

	// Check Receiver Type
	// We rely on type info map for precision
	if info != nil {
		if selObj := info.ObjectOf(sel.Sel); selObj != nil {
			// Check package of the method. Use Path() for checking "testing" as it is robust.
			if selObj.Pkg() != nil && selObj.Pkg().Path() == "testing" {
				return true
			}
		}
		// Fallback: Check type of X
		if tv, ok := info.Types[sel.X]; ok {
			// Should be *testing.T (or B/F)
			tStr := tv.Type.String()
			// Matches "*testing.T", "*testing.B", "testing.F" or qualified variants
			if strings.HasSuffix(tStr, "testing.T") || strings.HasSuffix(tStr, "testing.B") {
				// Ensure it is a pointer
				if _, isPtr := tv.Type.(*types.Pointer); isPtr {
					return true
				}
			}
		}
	}
	return false
}
