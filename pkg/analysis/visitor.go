package analysis

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"log"
	"strings"

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
	// Assign is the assignment statement (e.g., "_ = foo()"). Nil if it's a bare expression statement or defer.
	Assign *ast.AssignStmt
	// Stmt is the statement wrapping the call (either *ast.AssignStmt, *ast.ExprStmt or *ast.DeferStmt).
	Stmt ast.Stmt
	// Pos is the position of the error return (usually the call site).
	Pos token.Pos
}

// Detect scans the provided packages for unhandled errors.
// It detects calls processing errors that are ignored via blank identifier,
// treated as expression statements, or ignored in defer statements.
//
// It respects the "// auto-err:ignore" directive. If this text appears in comments
// associated with the statement, the injection point is skipped.
//
// It optionally logs debug information if a logger is provided via config (or standard log if verbose).
//
// pkgs: The list of packages to analyze.
// flt: The filter rules to exclude specific files or symbols.
// debug: If true, prints verbose reasons why calls are ignored.
//
// Returns a slice of detected points where error handling is missing.
func Detect(pkgs []*packages.Package, flt *filter.Filter, debug bool) ([]InjectionPoint, error) {
	var injectionPoints []InjectionPoint

	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			// generate comment map for this file to support directives
			cmap := ast.NewCommentMap(pkg.Fset, file, file.Comments)

			ast.Inspect(file, func(node ast.Node) bool {
				// Case 1: Expression Statement (Bare call)
				if exprStmt, ok := node.(*ast.ExprStmt); ok {
					if call, ok := exprStmt.X.(*ast.CallExpr); ok {
						if isUnhandledError(pkg.TypesInfo, call) {
							if shouldInclude(pkg, file, call, exprStmt, cmap, flt, debug) {
								injectionPoints = append(injectionPoints, InjectionPoint{
									Pkg:  pkg,
									File: file,
									Call: call,
									Stmt: exprStmt,
									Pos:  call.Pos(),
								})
							}
						} else if debug {
							logDebug(pkg, call, "ExprStmt call does not return error")
						}
					}
					return false
				}

				// Case 2: Assignment Statement (Assigned to _)
				if assignStmt, ok := node.(*ast.AssignStmt); ok {
					for i, rhs := range assignStmt.Rhs {
						if call, ok := rhs.(*ast.CallExpr); ok {
							if checksOut, errorIndex := isErrorReturningCall(pkg.TypesInfo, call); checksOut {
								var lhsExpr ast.Expr
								if len(assignStmt.Lhs) == len(assignStmt.Rhs) {
									lhsExpr = assignStmt.Lhs[i]
								} else {
									if errorIndex < len(assignStmt.Lhs) {
										lhsExpr = assignStmt.Lhs[errorIndex]
									}
								}

								if isBlankIdentifier(lhsExpr) {
									if shouldInclude(pkg, file, call, assignStmt, cmap, flt, debug) {
										injectionPoints = append(injectionPoints, InjectionPoint{
											Pkg:    pkg,
											File:   file,
											Call:   call,
											Assign: assignStmt,
											Stmt:   assignStmt,
											Pos:    call.Pos(),
										})
									}
								} else if debug {
									logDebug(pkg, call, "Error not assigned to blank identifier")
								}
							} else if debug {
								logDebug(pkg, call, "AssignStmt RHS does not return error")
							}
						}
					}
					return false
				}

				// Case 3: Defer Statement
				if deferStmt, ok := node.(*ast.DeferStmt); ok {
					if isUnhandledError(pkg.TypesInfo, deferStmt.Call) {
						if shouldInclude(pkg, file, deferStmt.Call, deferStmt, cmap, flt, debug) {
							injectionPoints = append(injectionPoints, InjectionPoint{
								Pkg:  pkg,
								File: file,
								Call: deferStmt.Call,
								Stmt: deferStmt,
								Pos:  deferStmt.Pos(),
							})
						}
					} else if debug {
						logDebug(pkg, deferStmt.Call, "Defer statement does not return error")
					}
					return false
				}

				return true
			})
		}
	}

	return injectionPoints, nil
}

// shouldInclude checks if the detected call satisfies the filter and directives.
//
// pkg: Package info.
// file: AST File.
// call: The call expression.
// stmt: The statement wrapping the call (used for comment lookups).
// cmap: The comment map for the file.
// flt: Filter object.
// debug: Enable verbose logging.
//
// Returns true if the call should be processed.
func shouldInclude(pkg *packages.Package, file *ast.File, call *ast.CallExpr, stmt ast.Stmt, cmap ast.CommentMap, flt *filter.Filter, debug bool) bool {
	// 1. Check Filters
	if flt != nil {
		if flt.MatchesFile(pkg.Fset, call.Pos()) {
			if debug {
				logDebug(pkg, call, fmt.Sprintf("Filtered by file glob: %s", pkg.Fset.Position(call.Pos()).Filename))
			}
			return false
		}
		if fn := getCalledFunction(pkg.TypesInfo, call); fn != nil {
			if flt.MatchesSymbol(fn) {
				if debug {
					logDebug(pkg, call, fmt.Sprintf("Filtered by symbol glob: %s.%s", fn.Pkg().Path(), fn.Name()))
				}
				return false
			}
		}
	}

	// 2. Check Directives (// auto-err:ignore)
	if hasIgnoreDirective(stmt, cmap) {
		if debug {
			logDebug(pkg, call, "Skipped by directive 'auto-err:ignore'")
		}
		return false
	}

	return true
}

// hasIgnoreDirective checks if the AST node has an associated comment containing "auto-err:ignore".
//
// node: The AST node to check (typically the statement).
// cmap: The comment map for the file.
//
// Returns true if the directive is found.
func hasIgnoreDirective(node ast.Node, cmap ast.CommentMap) bool {
	if cmap == nil {
		return false
	}
	comments, ok := cmap[node]
	if !ok {
		return false
	}
	for _, cg := range comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, "auto-err:ignore") {
				return true
			}
		}
	}
	return false
}

// logDebug prints a formatted debug message explaining why a call was skipped.
//
// pkg: Context package.
// call: The call node.
// reason: The explanation.
func logDebug(pkg *packages.Package, call *ast.CallExpr, reason string) {
	pos := pkg.Fset.Position(call.Pos())
	log.Printf("[DEBUG] Skipped call at %s:%d: %s", pos.Filename, pos.Line, reason)
}

// isUnhandledError checks if the call is an error returning function.
//
// info: Type info.
// call: Call expression.
//
// Returns true if unhandled.
func isUnhandledError(info *types.Info, call *ast.CallExpr) bool {
	ok, _ := isErrorReturningCall(info, call)
	return ok
}

// isErrorReturningCall checks return types.
//
// info: Type info.
// call: Call expr.
//
// Returns true if error is returned, and its index.
func isErrorReturningCall(info *types.Info, call *ast.CallExpr) (bool, int) {
	if info == nil {
		return false, -1
	}
	tv, ok := info.Types[call]
	if !ok {
		return false, -1
	}

	isTuple := false
	if _, ok := tv.Type.(*types.Tuple); ok {
		isTuple = true
	}

	// Single Return
	if !tv.IsVoid() && !isTuple {
		if isErrorType(tv.Type) {
			return true, 0
		}
	}

	// Tuple Return
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

// isErrorType robust check against universe error.
//
// t: Type to check.
//
// Returns true if type is effectively 'error'.
func isErrorType(t types.Type) bool {
	return t.String() == "error" || t.String() == "builtin.error" ||
		types.Identical(t, types.Universe.Lookup("error").Type())
}

// isBlankIdentifier checks for "_".
//
// expr: AST expression.
//
// Returns true if identifier is blank.
func isBlankIdentifier(expr ast.Expr) bool {
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name == "_"
	}
	return false
}

// getCalledFunction resolves the function object from the call expression.
// It handles direct function calls and calls to variables (local closures or function fields),
// returning a *types.Func object representing the symbol.
//
// If the call target is a variable (e.g. `myFunc()` where `myFunc` is a var), it synthesizes via
// types.NewFunc to ensure filters work against the variable name.
//
// info: Type info.
// call: Call expression.
//
// Returns the function symbol object or nil.
func getCalledFunction(info *types.Info, call *ast.CallExpr) *types.Func {
	var obj types.Object

	switch fun := call.Fun.(type) {
	// Direct Identifier call: foo()
	case *ast.Ident:
		obj = info.ObjectOf(fun)
	// Selector call: pkg.Foo() or struct.Field()
	case *ast.SelectorExpr:
		obj = info.ObjectOf(fun.Sel)
	}

	if obj == nil {
		return nil
	}

	// Case 1: Properly typed function/method (e.g. func Foo() {})
	if fn, ok := obj.(*types.Func); ok {
		return fn
	}

	// Case 2: Variable of function type (e.g. var f func(), or local closure)
	// We want to return a symbol so the user can filter by the variable name.
	if v, ok := obj.(*types.Var); ok {
		// Check if the variable holds a function signature
		if _, isSig := v.Type().Underlying().(*types.Signature); isSig {
			// Create a synthetic types.Func reusing the variables Pos, Pkg and Name.
			// Using nil for signature is safe for current filtering logic which only uses Pkg/Name.
			return types.NewFunc(v.Pos(), v.Pkg(), v.Name(), nil)
		}
	}

	return nil
}

// pathEnclosing extracts AST path (helper).
//
// file: File.
// start: Start pos.
// end: End pos.
//
// Returns the node path.
func pathEnclosing(file *ast.File, start, end token.Pos) []ast.Node {
	path, _ := astutil.PathEnclosingInterval(file, start, end)
	return path
}
