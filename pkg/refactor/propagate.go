package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/astgen"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/filter"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// MainHandlerStrategy defines how errors should be handled in entry points.
type MainHandlerStrategy string

const (
	// HandlerLogFatal uses log.Fatal(err).
	HandlerLogFatal MainHandlerStrategy = "log-fatal"
	// HandlerOsExit uses fmt.Println(err) followed by os.Exit(1).
	HandlerOsExit MainHandlerStrategy = "os-exit"
	// HandlerPanic uses panic(err).
	HandlerPanic MainHandlerStrategy = "panic"
)

// PropagateCallers updates all call sites of a modified function to match its new signature
// (assuming the signature acquired an extra 'error' return value).
//
// It scans the provided packages for usages of the target function. For each call site found,
// it:
// 1. Updates the assignment to receive the new error value (e.g., `x := foo()` -> `x, err := foo()`).
// 2. Checks if the enclosing function is an entry point (main/init) or a Test Handler (TestX).
// 3. If entry point or Test, injects a terminal handler (log.Fatal/panic/t.Fatal).
// 4. If regular function, checks if it returns error.
//   - If yes, injects `if err := ...; err != nil { return ..., err }` (using collapse if possible).
//   - If no, assigns error to `_` (or leaves for future passes).
//
// pkgs: The set of packages to search for callers.
// target: The function object whose signature was modified.
// strategy: The strategy to use when the caller is main() or init().
func PropagateCallers(pkgs []*packages.Package, target *types.Func, strategy string) (int, error) {
	if target == nil {
		return 0, fmt.Errorf("target function is nil")
	}

	updates := 0

	for _, pkg := range pkgs {
		// 1. Find usages
		var callsToUpdate []*ast.Ident
		for id, obj := range pkg.TypesInfo.Uses {
			if obj == target {
				callsToUpdate = append(callsToUpdate, id)
			}
		}

		for _, id := range callsToUpdate {
			// Find the file containing this identifier
			file := findFile(pkg, id.Pos())
			if file == nil {
				continue
			}

			// Get the path to the node
			path, _ := astutil.PathEnclosingInterval(file, id.Pos(), id.Pos())

			// We identify the CallExpr
			var call *ast.CallExpr
			var enclosingStmt ast.Stmt
			var assignment *ast.AssignStmt

			for _, node := range path {
				if c, ok := node.(*ast.CallExpr); ok && call == nil {
					if isIdentFunctionInCall(c, id) {
						call = c
					}
				}
				if stmt, ok := node.(ast.Stmt); ok && call != nil {
					enclosingStmt = stmt
					if as, ok := stmt.(*ast.AssignStmt); ok {
						assignment = as
					}
					break
				}
			}

			if call == nil || enclosingStmt == nil {
				continue
			}

			// Find enclosing function to determine behavior
			enclosingSig, enclosingFunc, enclosingDecl := findEnclosingFuncDetails(path, pkg.TypesInfo)

			entryPoint := false
			testParam := ""

			if enclosingFunc != nil {
				entryPoint = isEntryPoint(enclosingFunc)
			}

			// Check for Test Handler context
			if !entryPoint && enclosingDecl != nil {
				if filter.IsTestHandler(enclosingDecl) {
					testParam = filter.GetTestingParamName(enclosingDecl)
					// Treat as entry point (terminal handling) if we found a valid test param
					if testParam != "" {
						entryPoint = true
					}
				}
			}

			// Perform Refactor on this call site
			if err := refactorCallSite(call, assignment, enclosingStmt, enclosingSig, file, pkg.Fset, entryPoint, MainHandlerStrategy(strategy), testParam); err != nil {
				return updates, err
			}
			updates++
		}
	}

	return updates, nil
}

// HandleEntryPoint injects terminal error handling for a call site within an entry point (main/init)
// without creating a compilation error by forcing a signature change.
//
// This is used by the runner to handle unhandled errors directly inside main().
func HandleEntryPoint(file *ast.File, call *ast.CallExpr, stmt ast.Stmt, strategy string) error {
	var assignment *ast.AssignStmt
	if as, ok := stmt.(*ast.AssignStmt); ok {
		assignment = as
	}
	// We pass nil for enclosingSig because terminal handling (true) bypasses the return check.
	return refactorCallSite(call, assignment, stmt, nil, file, nil, true, MainHandlerStrategy(strategy), "")
}

// isIdentFunctionInCall checks if the identifier acts as the function expression in the call.
//
// call: The call expression to check.
// id: The identifier to look for.
func isIdentFunctionInCall(call *ast.CallExpr, id *ast.Ident) bool {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun == id
	case *ast.SelectorExpr:
		return fun.Sel == id
	}
	return false
}

// findFile locates the AST file for a given position.
//
// pkg: The package to search.
// pos: The position to look up.
func findFile(pkg *packages.Package, pos token.Pos) *ast.File {
	for _, f := range pkg.Syntax {
		if f.Pos() <= pos && pos < f.End() {
			return f
		}
	}
	return nil
}

// findEnclosingFuncDetails walks the path upwards to find the function signature and object.
//
// path: The AST path to the node.
// info: Type info for resolution.
//
// Returns Signature, Func Object, and Func Decl (if available).
func findEnclosingFuncDetails(path []ast.Node, info *types.Info) (*types.Signature, *types.Func, *ast.FuncDecl) {
	for _, node := range path {
		if fn, ok := node.(*ast.FuncDecl); ok {
			// Get signature from object
			if obj := info.ObjectOf(fn.Name); obj != nil {
				if sig, ok := obj.Type().(*types.Signature); ok {
					if funcObj, isFunc := obj.(*types.Func); isFunc {
						return sig, funcObj, fn
					}
					return sig, nil, fn
				}
			}
			return nil, nil, fn // Should be found
		}
		if lit, ok := node.(*ast.FuncLit); ok {
			if tv, ok := info.Types[lit]; ok {
				if sig, ok := tv.Type.(*types.Signature); ok {
					return sig, nil, nil // FuncLits are not named entry points
				}
			}
		}
	}
	return nil, nil, nil
}

// isEntryPoint determines if the function is a main or init function.
//
// fn: The function object to check.
func isEntryPoint(fn *types.Func) bool {
	if fn.Name() == "init" {
		return true
	}
	if fn.Name() == "main" && fn.Pkg() != nil && fn.Pkg().Name() == "main" {
		return true
	}
	return false
}

// refactorCallSite modifies the AST to handle the extra error return.
//
// call: The call expression.
// assign: The assignment statement (nil if ExprStmt).
// stmt: The statement wrapping the call.
// enclosingSig: The signature of the function containing the call.
// file: The AST file.
// fset: The file set for imports.
// isTerminal: True if enclosing function is main/init/Test.
// strategy: The handling strategy if isTerminal (default for main).
// testParam: The name of the testing parameter (e.g. "t") if isTerminal is due to a test.
func refactorCallSite(call *ast.CallExpr, assign *ast.AssignStmt, stmt ast.Stmt, enclosingSig *types.Signature, file *ast.File, fset *token.FileSet, isTerminal bool, strategy MainHandlerStrategy, testParam string) error {
	// 1. Determine var name for error
	errName := "err"

	// 2. Modify Assignment / LHS
	if assign != nil {
		// Existing assignment: "a := foo()" or "a, b = foo()"
		// We simply append "err" to LHS.

		// Clone LHS to avoid mutating original slice backing array unpredictably during iteration if shared
		newLHS := make([]ast.Expr, len(assign.Lhs))
		copy(newLHS, assign.Lhs)

		newLHS = append(newLHS, &ast.Ident{Name: errName})

		assign.Lhs = newLHS
	} else {
		// Expression statement: "foo()"
		// Convert to "if err := foo(); err != nil ..." via replacement block
		return replaceStatementInBlock(file, stmt, enclosingSig, call, isTerminal, strategy, testParam)
	}

	// 3. Inject Check
	// If existing assignment, we kept the pointer 'assign' valid (modified in place), so we just need to insert 'if' after.
	if assign != nil {
		if isTerminal {
			check := generateTerminalCheck(strategy, testParam)
			cursorReplace(file, stmt, stmt, check)
			// Imports handled by post-processing in runner
		} else if canReturnError(enclosingSig) {
			check, err := generateErrorCheck(enclosingSig)
			if err != nil {
				return err
			}
			cursorReplace(file, stmt, stmt, check)
		} else {
			// Caller doesn't return error.
			// We modified assignment to include 'err'. If we don't check it, use '_'.
			// Update last LHS to '_'
			assign.Lhs[len(assign.Lhs)-1] = &ast.Ident{Name: "_"}
		}
	}

	return nil
}

// replaceStatementInBlock handles the case where ExprStmt needs to become AssignStmt + IfStmt.
// It tries to use the collapsed syntax `if err := call; err != nil { ... }`.
//
// file: The AST file.
// oldStmt: The statement to replace.
// sig: The enclosing signature.
// call: The call expression.
// isTerminal: Whether to generate terminal handling.
// strategy: The terminal handling strategy.
// testParam: The testing parameter name used for terminal test handling.
func replaceStatementInBlock(file *ast.File, oldStmt ast.Stmt, sig *types.Signature, call *ast.CallExpr, isTerminal bool, strategy MainHandlerStrategy, testParam string) error {
	// We map unhandled returns to "err := " assuming previously void or intentionally ignored.
	as := &ast.AssignStmt{
		Lhs: []ast.Expr{&ast.Ident{Name: "err"}},
		Tok: token.DEFINE,
		Rhs: []ast.Expr{call},
	}

	// Determine if control flow allows return (non-terminal strategies do, terminal ones don't need it)
	canReturn := false
	if !isTerminal {
		canReturn = canReturnError(sig)
	}

	// Determine check logic
	var check *ast.IfStmt
	var err error

	if isTerminal {
		check = generateTerminalCheck(strategy, testParam)
	} else if canReturn {
		check, err = generateErrorCheck(sig)
		if err != nil {
			return err
		}
	}

	astutil.Apply(file, func(c *astutil.Cursor) bool {
		if c.Node() == oldStmt {
			if check != nil {
				// Use collapsed syntax: if err := call; err != nil { ... }
				check.Init = as
				c.Replace(check)
			} else {
				// If we can't check (no return error and not terminal), we mute it.
				// as becomes "_ = call".
				as.Lhs[0] = &ast.Ident{Name: "_"}
				// We don't have "err != nil", so strictly we just replace with the assignment.
				// However, standard assignment _ = x might be assignment specific.
				if len(as.Lhs) == 1 {
					as.Tok = token.ASSIGN
				}
				c.Replace(as)
			}
			return false // stop
		}
		return true
	}, nil)

	return nil
}

func cursorReplace(file *ast.File, target ast.Stmt, replacement ast.Stmt, insertAfter ast.Stmt) {
	astutil.Apply(file, func(c *astutil.Cursor) bool {
		if c.Node() == target {
			c.Replace(replacement)
			if insertAfter != nil {
				c.InsertAfter(insertAfter)
			}
			return false
		}
		return true
	}, nil)
}

func canReturnError(sig *types.Signature) bool {
	if sig == nil || sig.Results().Len() == 0 {
		return false
	}
	last := sig.Results().At(sig.Results().Len() - 1)
	return last.Type().String() == "error" || last.Type().String() == "builtin.error"
}

// generateErrorCheck generates `if err != nil { return ..., err }`.
func generateErrorCheck(sig *types.Signature) (*ast.IfStmt, error) {
	// return zero, zero, ..., err
	results := sig.Results()
	var retExprs []ast.Expr
	for i := 0; i < results.Len()-1; i++ {
		z, err := astgen.ZeroExpr(results.At(i).Type(), astgen.ZeroCtx{})
		if err != nil {
			return nil, err
		}
		retExprs = append(retExprs, z)
	}
	retExprs = append(retExprs, &ast.Ident{Name: "err"})

	return &ast.IfStmt{
		Cond: &ast.BinaryExpr{
			X:  &ast.Ident{Name: "err"},
			Op: token.NEQ,
			Y:  &ast.Ident{Name: "nil"},
		},
		Body: &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.ReturnStmt{Results: retExprs},
			},
		},
	}, nil
}

// generateTerminalCheck generates code to handle error at entry points or tests.
// Note: This returns an IfStmt *without* the Init field set. Caller must set Init if collapsing.
//
// strategy: The main handler strategy.
// testParam: If non-empty, triggers test logic (t.Fatal) overriding strategy.
func generateTerminalCheck(strategy MainHandlerStrategy, testParam string) *ast.IfStmt {
	var bodyStmts []ast.Stmt

	if testParam != "" {
		// t.Fatal(err)
		bodyStmts = []ast.Stmt{
			&ast.ExprStmt{
				X: &ast.CallExpr{
					Fun: &ast.SelectorExpr{
						X:   &ast.Ident{Name: testParam},
						Sel: &ast.Ident{Name: "Fatal"},
					},
					Args: []ast.Expr{&ast.Ident{Name: "err"}},
				},
			},
		}
	} else {
		switch strategy {
		case HandlerPanic:
			// panic(err)
			bodyStmts = []ast.Stmt{
				&ast.ExprStmt{
					X: &ast.CallExpr{
						Fun:  &ast.Ident{Name: "panic"},
						Args: []ast.Expr{&ast.Ident{Name: "err"}},
					},
				},
			}
		case HandlerOsExit:
			// fmt.Println(err); os.Exit(1)
			bodyStmts = []ast.Stmt{
				&ast.ExprStmt{
					X: &ast.CallExpr{
						Fun: &ast.SelectorExpr{
							X:   &ast.Ident{Name: "fmt"},
							Sel: &ast.Ident{Name: "Println"},
						},
						Args: []ast.Expr{&ast.Ident{Name: "err"}},
					},
				},
				&ast.ExprStmt{
					X: &ast.CallExpr{
						Fun: &ast.SelectorExpr{
							X:   &ast.Ident{Name: "os"},
							Sel: &ast.Ident{Name: "Exit"},
						},
						Args: []ast.Expr{&ast.BasicLit{Kind: token.INT, Value: "1"}},
					},
				},
			}
		case HandlerLogFatal:
			fallthrough
		default:
			// log.Fatal(err)
			bodyStmts = []ast.Stmt{
				&ast.ExprStmt{
					X: &ast.CallExpr{
						Fun: &ast.SelectorExpr{
							X:   &ast.Ident{Name: "log"},
							Sel: &ast.Ident{Name: "Fatal"},
						},
						Args: []ast.Expr{&ast.Ident{Name: "err"}},
					},
				},
			}
		}
	}

	return &ast.IfStmt{
		Cond: &ast.BinaryExpr{
			X:  &ast.Ident{Name: "err"},
			Op: token.NEQ,
			Y:  &ast.Ident{Name: "nil"},
		},
		Body: &ast.BlockStmt{
			List: bodyStmts,
		},
	}
}
