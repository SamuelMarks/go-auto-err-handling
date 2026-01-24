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
// It implements a Recursive Bubble-Up Strategy:
// 1. Unhandled error in `target()` requires modification of `caller()`.
// 2. If `caller()` is a standard Void function (no return), it is upgraded to return `error`.
// 3. This upgrade triggers the queueing of `caller` as a new target, recursively updating ITS callers.
//
// It respects entry points (`main`, `init`) and Test functions, treating them as terminals
// where errors are handled (log/panic) instead of bubbled.
//
// pkgs: The set of packages to search for callers.
// initialTarget: The function object whose signature was initially modified.
// strategy: The strategy to use for terminal handlers.
//
// Returns the total number of call sites updated.
func PropagateCallers(pkgs []*packages.Package, initialTarget *types.Func, strategy string) (int, error) {
	if initialTarget == nil {
		return 0, fmt.Errorf("target function is nil")
	}

	queue := []*types.Func{initialTarget}
	visited := make(map[*types.Func]bool)
	visited[initialTarget] = true

	totalUpdates := 0

	// BFS traversal of the call graph up the stack
	for len(queue) > 0 {
		target := queue[0]
		queue = queue[1:]

		// Scan entire package set for usages of 'target'
		for _, pkg := range pkgs {
			// Find AST Identifiers referring to current target object
			var callsToUpdate []*ast.Ident
			for id, obj := range pkg.TypesInfo.Uses {
				if obj == target {
					callsToUpdate = append(callsToUpdate, id)
				}
			}

			// Process each call site
			for _, id := range callsToUpdate {
				file := findFile(pkg, id.Pos())
				if file == nil {
					continue
				}

				updates, newTarget, err := processCallSite(pkg, file, id, target, strategy)
				if err != nil {
					return totalUpdates, err
				}
				if updates > 0 {
					totalUpdates++
				}

				// If the caller was upgraded to return error, add to queue for bubbling
				if newTarget != nil {
					if !visited[newTarget] {
						visited[newTarget] = true
						queue = append(queue, newTarget)
					}
				}
			}
		}
	}

	return totalUpdates, nil
}

// processCallSite handles the refactoring for a single usage.
// Returns 1 if updated, and the new function object if recursion is needed.
func processCallSite(pkg *packages.Package, file *ast.File, id *ast.Ident, target *types.Func, strategy string) (int, *types.Func, error) {
	path, _ := astutil.PathEnclosingInterval(file, id.Pos(), id.Pos())

	// Identify components
	var call *ast.CallExpr
	var enclosingStmt ast.Stmt
	var assignment *ast.AssignStmt

	// Walk up to find the Call and Statement
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
		return 0, nil, nil
	}

	// Analyze enclosing function context
	sig, funcObj, decl := findEnclosingFuncDetails(path, pkg.TypesInfo)

	isTerminal := false
	testParam := ""

	// Determine if terminal
	if funcObj != nil {
		isTerminal = isEntryPoint(funcObj)
	}
	if !isTerminal && decl != nil {
		if filter.IsTestHandler(decl) {
			testParam = filter.GetTestingParamName(decl)
		} else if isHelper, param := filter.IsTestHelper(decl); isHelper {
			testParam = param
		}
		if testParam != "" {
			isTerminal = true
		}
	}

	// Decision: Bubble or Handle?
	var nextTarget *types.Func

	if !isTerminal && decl != nil && funcObj != nil {
		// If not terminal, check if we need to/can upgrade signature
		canReturn := canReturnError(sig)

		// If it doesn't return error, we upgrade it to bubble the error up
		if !canReturn {
			// Perform Upgrade
			changed, err := AddErrorToSignature(pkg.Fset, decl)
			if err != nil {
				return 0, nil, err
			}
			if changed {
				// Patch Types so 'sig' and 'funcObj' reflect the new reality
				if err := PatchSignature(pkg.TypesInfo, decl, pkg.Types); err != nil {
					return 0, nil, err
				}
				// Reload objects from patched info
				obj := pkg.TypesInfo.ObjectOf(decl.Name)
				if fn, ok := obj.(*types.Func); ok {
					funcObj = fn
					sig = fn.Type().(*types.Signature)
					nextTarget = fn
				}
			}
		}
	}

	// Perform AST Rewrite of the call site
	if err := refactorCallSite(call, assignment, enclosingStmt, sig, file, pkg.Fset, isTerminal, MainHandlerStrategy(strategy), testParam); err != nil {
		return 0, nil, err
	}

	return 1, nextTarget, nil
}

// HandleEntryPoint injects terminal error handling for a call site within an entry point (main/init).
func HandleEntryPoint(file *ast.File, call *ast.CallExpr, stmt ast.Stmt, strategy string) error {
	var assignment *ast.AssignStmt
	if as, ok := stmt.(*ast.AssignStmt); ok {
		assignment = as
	}
	return refactorCallSite(call, assignment, stmt, nil, file, nil, true, MainHandlerStrategy(strategy), "")
}

// isIdentFunctionInCall checks if the identifier acts as the function expression in the call.
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
func findFile(pkg *packages.Package, pos token.Pos) *ast.File {
	for _, f := range pkg.Syntax {
		if f.Pos() <= pos && pos < f.End() {
			return f
		}
	}
	return nil
}

// findEnclosingFuncDetails walks the path upwards to find the function signature and object.
func findEnclosingFuncDetails(path []ast.Node, info *types.Info) (*types.Signature, *types.Func, *ast.FuncDecl) {
	for _, node := range path {
		if fn, ok := node.(*ast.FuncDecl); ok {
			if obj := info.ObjectOf(fn.Name); obj != nil {
				if sig, ok := obj.Type().(*types.Signature); ok {
					if funcObj, isFunc := obj.(*types.Func); isFunc {
						return sig, funcObj, fn
					}
					return sig, nil, fn
				}
			}
			return nil, nil, fn
		}
		if lit, ok := node.(*ast.FuncLit); ok {
			if tv, ok := info.Types[lit]; ok {
				if sig, ok := tv.Type.(*types.Signature); ok {
					return sig, nil, nil
				}
			}
		}
	}
	return nil, nil, nil
}

// isEntryPoint determines if the function is a main or init function.
func isEntryPoint(fn *types.Func) bool {
	if fn.Name() == "init" {
		return true
	}
	if fn.Name() == "main" && fn.Pkg() != nil && fn.Pkg().Name() == "main" {
		return true
	}
	return false
}

// refactorCallSite modifies the AST to handle the extra error return (injection logic).
func refactorCallSite(call *ast.CallExpr, assign *ast.AssignStmt, stmt ast.Stmt, enclosingSig *types.Signature, file *ast.File, fset *token.FileSet, isTerminal bool, strategy MainHandlerStrategy, testParam string) error {
	errName := "err"

	if assign != nil {
		newLHS := make([]ast.Expr, len(assign.Lhs))
		copy(newLHS, assign.Lhs)
		newLHS = append(newLHS, &ast.Ident{Name: errName})
		assign.Lhs = newLHS
	} else {
		return replaceStatementInBlock(file, stmt, enclosingSig, call, isTerminal, strategy, testParam)
	}

	if assign != nil {
		if isTerminal {
			check := generateTerminalCheck(strategy, testParam)
			cursorReplace(file, stmt, stmt, check)
		} else if canReturnError(enclosingSig) {
			check, err := generateErrorCheck(enclosingSig)
			if err != nil {
				return err
			}
			cursorReplace(file, stmt, stmt, check)
		} else {
			assign.Lhs[len(assign.Lhs)-1] = &ast.Ident{Name: "_"}
		}
	}
	return nil
}

// replaceStatementInBlock handles the case where ExprStmt needs to become AssignStmt + IfStmt.
func replaceStatementInBlock(file *ast.File, oldStmt ast.Stmt, sig *types.Signature, call *ast.CallExpr, isTerminal bool, strategy MainHandlerStrategy, testParam string) error {
	as := &ast.AssignStmt{
		Lhs: []ast.Expr{&ast.Ident{Name: "err"}},
		Tok: token.DEFINE,
		Rhs: []ast.Expr{call},
	}

	canReturn := false
	if !isTerminal {
		canReturn = canReturnError(sig)
	}

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
				check.Init = as
				c.Replace(check)
			} else {
				as.Lhs[0] = &ast.Ident{Name: "_"}
				if len(as.Lhs) == 1 {
					as.Tok = token.ASSIGN
				}
				c.Replace(as)
			}
			return false
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

func generateErrorCheck(sig *types.Signature) (*ast.IfStmt, error) {
	results := sig.Results()
	var retExprs []ast.Expr
	// PatchSignature adds 'error' to the end.
	// So we generate zero vals for len-1.
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

func generateTerminalCheck(strategy MainHandlerStrategy, testParam string) *ast.IfStmt {
	var bodyStmts []ast.Stmt

	if testParam != "" {
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
			bodyStmts = []ast.Stmt{
				&ast.ExprStmt{
					X: &ast.CallExpr{
						Fun:  &ast.Ident{Name: "panic"},
						Args: []ast.Expr{&ast.Ident{Name: "err"}},
					},
				},
			}
		case HandlerOsExit:
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
