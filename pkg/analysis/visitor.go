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
	// Assign is the assignment statement (e.g., "_ = foo()"). Nil if it's a bare expression, defer, go, or gen decl.
	Assign *ast.AssignStmt
	// Stmt is the statement wrapping the call.
	// Can be *ast.ExprStmt, *ast.AssignStmt, *ast.DeferStmt, *ast.GoStmt, *ast.IfStmt, *ast.SwitchStmt, or nil for Global Decls.
	Stmt ast.Stmt
	// Pos is the position of the error return (usually the call site).
	Pos token.Pos
}

// Detect scans the provided packages for unhandled errors.
// It detects calls processing errors that are ignored via blank identifier,
// treated as expression statements, ignored in defer/go statements,
// embedded in control structures, ignored in global variable initializers,
// or hidden within method chains (`foo().bar()`).
//
// It respects the "// auto-err:ignore" directive. If this text appears in comments
// associated with the statement, the injection point is skipped.
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
				// Helper to register points
				addPoint := func(call *ast.CallExpr, stmt ast.Stmt, assign *ast.AssignStmt) {
					if shouldInclude(pkg, file, call, stmt, cmap, flt, debug) {
						injectionPoints = append(injectionPoints, InjectionPoint{
							Pkg:    pkg,
							File:   file,
							Call:   call,
							Stmt:   stmt,
							Assign: assign,
							Pos:    call.Pos(),
						})
					}
				}

				// Case 1: Expression Statement (Bare call)
				if exprStmt, ok := node.(*ast.ExprStmt); ok {
					// Check root call
					if call, ok := exprStmt.X.(*ast.CallExpr); ok {
						if isUnhandledError(pkg.TypesInfo, call) {
							addPoint(call, exprStmt, nil)
						} else if debug {
							logDebug(pkg, call, "ExprStmt call does not return error")
						}
					}
					// Check chains (e.g. foo().bar())
					checkForChains(pkg.TypesInfo, exprStmt.X, func(c *ast.CallExpr) {
						addPoint(c, exprStmt, nil)
					})
					return false
				}

				// Case 2: Assignment Statement (Assigned to _)
				if assignStmt, ok := node.(*ast.AssignStmt); ok {
					for i, rhs := range assignStmt.Rhs {
						// Check root call
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
									addPoint(call, assignStmt, assignStmt)
								} else if debug {
									logDebug(pkg, call, "Error not assigned to blank identifier")
								}
							} else if debug {
								logDebug(pkg, call, "AssignStmt RHS does not return error")
							}
						}
						// Check chains in RHS
						checkForChains(pkg.TypesInfo, rhs, func(c *ast.CallExpr) {
							addPoint(c, assignStmt, assignStmt)
						})
					}
					return false
				}

				// Case 3: Defer Statement
				if deferStmt, ok := node.(*ast.DeferStmt); ok {
					if isUnhandledError(pkg.TypesInfo, deferStmt.Call) {
						addPoint(deferStmt.Call, deferStmt, nil)
					} else if debug {
						logDebug(pkg, deferStmt.Call, "Defer statement does not return error")
					}
					// Check chains in defer
					checkForChains(pkg.TypesInfo, deferStmt.Call, func(c *ast.CallExpr) {
						addPoint(c, deferStmt, nil)
					})
					return false
				}

				// Case 4: Go Statement
				if goStmt, ok := node.(*ast.GoStmt); ok {
					if isUnhandledError(pkg.TypesInfo, goStmt.Call) {
						addPoint(goStmt.Call, goStmt, nil)
					} else if debug {
						logDebug(pkg, goStmt.Call, "Go statement call does not return error")
					}
					// Check chains in go stmt
					checkForChains(pkg.TypesInfo, goStmt.Call, func(c *ast.CallExpr) {
						addPoint(c, goStmt, nil)
					})
					return false
				}

				// Case 5: If Statement (Embedded call in condition)
				if ifStmt, ok := node.(*ast.IfStmt); ok {
					if call := findSafeEmbeddedCall(ifStmt.Cond); call != nil {
						if isUnhandledError(pkg.TypesInfo, call) {
							addPoint(call, ifStmt, nil)
						} else if debug {
							logDebug(pkg, call, "Embedded if-condition call does not return error")
						}
					}
					// Chains inside condition? Probably too complex for "SafeEmbeddedCall" logic.
					return true
				}

				// Case 6: Switch Statement (Embedded call in tag)
				if switchStmt, ok := node.(*ast.SwitchStmt); ok {
					if call := findSafeEmbeddedCall(switchStmt.Tag); call != nil {
						if isUnhandledError(pkg.TypesInfo, call) {
							addPoint(call, switchStmt, nil)
						}
					}
					return true
				}

				// Case 7: GenDecl (Global Variable Init)
				if genDecl, ok := node.(*ast.GenDecl); ok && genDecl.Tok == token.VAR {
					for _, spec := range genDecl.Specs {
						if vSpec, ok := spec.(*ast.ValueSpec); ok {
							for i, rhs := range vSpec.Values {
								// Check Root Call
								if call, ok := rhs.(*ast.CallExpr); ok {
									if isGlobalErrorIgnored(pkg.TypesInfo, vSpec, i, call) {
										addPoint(call, nil, nil)
									}
								}
								// Check Chains in Global Init
								checkForChains(pkg.TypesInfo, rhs, func(c *ast.CallExpr) {
									addPoint(c, nil, nil)
								})
							}
						}
					}
					return true
				}

				return true
			})
		}
	}

	return injectionPoints, nil
}

// checkForChains inspects an expression tree for SelectorExpr nodes where the X (receiver)
// is a CallExpr that returns an error. This identifies `foo().bar()` where `foo()` needs handling.
//
// info: Type info.
// root: The expression to inspect.
// callback: Function to invoke when a broken chain call is found.
func checkForChains(info *types.Info, root ast.Expr, callback func(*ast.CallExpr)) {
	ast.Inspect(root, func(n ast.Node) bool {
		// Stop if we hit a different expression root (block execution order complexity)
		// but SelectorExpr/CallExpr/IndexExpr etc are fine to traverse.
		if sel, ok := n.(*ast.SelectorExpr); ok {
			// Check if X is a CallExpr
			if call, ok := sel.X.(*ast.CallExpr); ok {
				if isUnhandledError(info, call) {
					callback(call)
				}
			}
		}
		return true
	})
}

// findSafeEmbeddedCall recursively inspects an expression to find a function call
// that is safe to extract (lift) out of the expression.
// expr: The expression to inspect.
// Returns the CallExpr if found and safe, otherwise nil.
func findSafeEmbeddedCall(expr ast.Expr) *ast.CallExpr {
	if expr == nil {
		return nil
	}
	switch e := expr.(type) {
	case *ast.CallExpr:
		return e
	case *ast.UnaryExpr:
		return findSafeEmbeddedCall(e.X)
	case *ast.ParenExpr:
		return findSafeEmbeddedCall(e.X)
	}
	return nil
}

// isGlobalErrorIgnored logic mirrors checks for AssignStmt but for ValueSpec.
func isGlobalErrorIgnored(info *types.Info, vSpec *ast.ValueSpec, rhsIndex int, call *ast.CallExpr) bool {
	checksOut, errorIndex := isErrorReturningCall(info, call)
	if !checksOut {
		return false
	}

	var lhsExpr ast.Expr
	if len(vSpec.Names) == len(vSpec.Values) {
		// 1-to-1 assignment
		lhsExpr = vSpec.Names[rhsIndex]
	} else if len(vSpec.Values) == 1 {
		// Multi-return function call is the single RHS
		if errorIndex < len(vSpec.Names) {
			lhsExpr = vSpec.Names[errorIndex]
		}
	}

	return isBlankIdentifier(lhsExpr)
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
		// Update: Handle error-returning MatchesFile
		matchedFile, err := flt.MatchesFile(pkg.Fset, call.Pos())
		if err != nil {
			if debug {
				logDebug(pkg, call, fmt.Sprintf("Error checking file filter: %v", err))
			}
			// If we can't check file (permission?), skipping usually safer or better to fail?
			// We skip to be safe.
			return false
		}
		if matchedFile {
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
	// Stmt might be nil for global vars logic
	if stmt != nil && hasIgnoreDirective(stmt, cmap) {
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
	if expr == nil {
		return false
	}
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
