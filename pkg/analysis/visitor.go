package analysis

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/filter"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// InjectionPoint represents a location in the code where an error is unhandled.
type InjectionPoint struct {
	// Pkg is the package containing the code.
	Pkg *packages.Package
	// File is the AST file containing the statement.
	File *ast.File
	// Call is the function call expression returning the error.
	Call *ast.CallExpr
	// Assign is the assignment statement (e.g., "_ = foo()"). Nil if it's a bare expression statement.
	Assign *ast.AssignStmt
	// Stmt is the statement wrapping the call (either *ast.AssignStmt or *ast.ExprStmt).
	Stmt ast.Stmt
	// Pos is the position of the error return (usually the call site).
	Pos token.Pos
}

// Detect scans the provided packages for unhandled errors.
// It implements logic similar to errcheck: detecting calls processing errors that are
// either ignored via blank identifier or treated as expression statements.
func Detect(pkgs []*packages.Package, flt *filter.Filter) ([]InjectionPoint, error) {
	var injectionPoints []InjectionPoint

	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				// We look for function calls.
				// However, we need to look at the *Statement* level to know if it's ignored.

				// Case 1: Expression Statement (Bare call)
				// e.g. "func() { fail() }"
				if exprStmt, ok := node.(*ast.ExprStmt); ok {
					if call, ok := exprStmt.X.(*ast.CallExpr); ok {
						if isUnhandledError(pkg.TypesInfo, call) {
							if shouldInclude(pkg, file, call, flt) {
								injectionPoints = append(injectionPoints, InjectionPoint{
									Pkg:  pkg,
									File: file,
									Call: call,
									Stmt: exprStmt,
									Pos:  call.Pos(),
								})
							}
						}
					}
					return false // Don't recurse deeper into call for statement check
				}

				// Case 2: Assignment Statement (Assigned to _)
				// e.g. "_ = fail()" or "a, _ := failTwo()"
				if assignStmt, ok := node.(*ast.AssignStmt); ok {
					// Check RHS for calls
					for i, rhs := range assignStmt.Rhs {
						if call, ok := rhs.(*ast.CallExpr); ok {
							// Determine which LHS corresponds to the error.
							// This requires resolving the tuple size returned by call.
							if checksOut, errorIndex := isErrorReturningCall(pkg.TypesInfo, call); checksOut {
								// Check the corresponding LHS identifier
								// Logic:
								// If count(LHS) == count(RHS), then 1-to-1 match.
								// If count(LHS) > count(RHS), it implies RHS is a multivalued function.

								var lhsExpr ast.Expr

								if len(assignStmt.Lhs) == len(assignStmt.Rhs) {
									// 1 to 1 (func returns single value)
									lhsExpr = assignStmt.Lhs[i]
								} else {
									// Multivalue return.
									// assignStmt.Rhs must contain only this call (Go spec).
									// errorIndex tells us which return value is the error (0-based)
									if errorIndex < len(assignStmt.Lhs) {
										lhsExpr = assignStmt.Lhs[errorIndex]
									}
								}

								if isBlankIdentifier(lhsExpr) {
									// Found unhandled error assigned to _
									if shouldInclude(pkg, file, call, flt) {
										injectionPoints = append(injectionPoints, InjectionPoint{
											Pkg:    pkg,
											File:   file,
											Call:   call,
											Assign: assignStmt,
											Stmt:   assignStmt,
											Pos:    call.Pos(),
										})
									}
								}
							}
						}
					}
					return false
				}

				return true
			})
		}
	}

	return injectionPoints, nil
}

// Helper to check filters
func shouldInclude(pkg *packages.Package, file *ast.File, call *ast.CallExpr, flt *filter.Filter) bool {
	if flt == nil {
		return true
	}
	// Check File exclusion
	if flt.MatchesFile(pkg.Fset, call.Pos()) {
		return false
	}
	// Check Symbol exclusion
	if fn := getCalledFunction(pkg.TypesInfo, call); fn != nil {
		if flt.MatchesSymbol(fn) {
			return false
		}
	}
	return true
}

func isUnhandledError(info *types.Info, call *ast.CallExpr) bool {
	// For bare expressions, if it returns error as last argument, it's unhandled.
	ok, _ := isErrorReturningCall(info, call)
	return ok
}

// isErrorReturningCall returns true if the call returns an error as its last value.
// Also returns the index of the error return value.
func isErrorReturningCall(info *types.Info, call *ast.CallExpr) (bool, int) {
	if info == nil {
		return false, -1
	}

	tv, ok := info.Types[call]
	if !ok {
		return false, -1
	}

	// Check if type is a tuple
	isTuple := false
	if _, ok := tv.Type.(*types.Tuple); ok {
		isTuple = true
	}

	// Case: Single return
	if !tv.IsVoid() && !isTuple {
		if isErrorType(tv.Type) {
			return true, 0
		}
	}

	// Case: Tuple return
	if tuple, ok := tv.Type.(*types.Tuple); ok {
		if tuple.Len() > 0 {
			last := tuple.At(tuple.Len() - 1)
			if isErrorType(last.Type()) {
				return true, tuple.Len() - 1
			}
		}
	}

	return false, -1
}

func isErrorType(t types.Type) bool {
	return t.String() == "error" || t.String() == "builtin.error"
	// Note: t.String() for error interface is usually "error" within standard Go universe.
	// A more robust check performs interface comparison with types.Universe.Lookup("error").Type(),
	// but string matching is extremely common for this specific builtin indirection.
}

func isBlankIdentifier(expr ast.Expr) bool {
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name == "_"
	}
	return false
}

// getCalledFunction attempts to resolve the *types.Func associated with a call expression.
func getCalledFunction(info *types.Info, call *ast.CallExpr) *types.Func {
	// Handle standard Identifier calls: foo()
	if ident, ok := call.Fun.(*ast.Ident); ok {
		if obj := info.ObjectOf(ident); obj != nil {
			if fn, ok := obj.(*types.Func); ok {
				return fn
			}
		}
	}

	// Handle Selector calls: pkg.foo() or obj.method()
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if obj := info.ObjectOf(sel.Sel); obj != nil {
			if fn, ok := obj.(*types.Func); ok {
				return fn
			}
		}
	}

	// Helper for checking filter logic against path
	// We need to resolve path of Package here if needed by filter
	return nil
}

// To assist filters that require looking up enclosing context
func pathEnclosing(file *ast.File, start, end token.Pos) []ast.Node {
	path, _ := astutil.PathEnclosingInterval(file, start, end)
	return path
}
