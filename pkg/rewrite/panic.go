package rewrite

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/astgen"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/refactor"
	"golang.org/x/tools/go/ast/astutil"
)

// RewritePanics scans the provided file for explicit panic calls (e.g., panic(err)) and converts them
// into return statements with an error.
//
// This transformation involves:
// 1. Identifying panic calls with arguments.
// 2. Modifying the enclosing function signature to return an error (if it doesn't already).
// 3. Replacing the panic statement with a return statement:
//   - If the argument is an error: return zero_vals..., arg
//   - If the argument is a string: return zero_vals..., fmt.Errorf(arg)
//   - Otherwise: return zero_vals..., fmt.Errorf("%v", arg)
//
// This is triggered via a specific CLI flag configuration passed to the runner.
//
// file: The AST file to modify.
//
// Returns true if any changes were applied.
func (i *Injector) RewritePanics(file *ast.File) (bool, error) {
	applied := false
	var firstErr error

	// We use astutil.Apply to traverse and modify declarations and statements
	astutil.Apply(file, func(c *astutil.Cursor) bool {
		if firstErr != nil {
			return false
		}

		node := c.Node()

		// We focus on Function Declarations
		fnDecl, ok := node.(*ast.FuncDecl)
		if !ok {
			return true
		}

		// Check if this function contains valid panic calls to rewrite
		if !i.containsRewriteablePanic(fnDecl) {
			return false // Skip descent if no panics
		}

		// 1. Signature Adjustment
		// Determine if we need to add 'error' to the return signature
		needsSigUpdate := !i.hasTrailingErrorReturn(fnDecl)

		if needsSigUpdate {
			changed, err := refactor.AddErrorToSignature(i.Fset, fnDecl)
			if err != nil {
				firstErr = err
				return false
			}
			if changed {
				applied = true
			}
		}

		// 2. Body Rewrite
		// We now replace panic() statements with return statements.
		// We must recalculate the expected return types (zero values) based on the current signature.
		// Note: TypesInfo might be slightly stale if signature just changed,
		// but the *original* return types (pre-error) are still valid in TypesInfo.

		rewriteErr := i.replacePanicsInBody(fnDecl)
		if rewriteErr != nil {
			firstErr = rewriteErr
			return false
		}

		// If we modified body or signature, we mark applied true.
		// replacePanicsInBody returns error, but we can assume if it returns nil,
		// and we knew containsRewriteablePanic was true, we likely made changes.
		// To be precise, we trust the combination.
		applied = true

		return false // Stop traversing children of this func (processed internally)
	}, nil)

	return applied, firstErr
}

// containsRewriteablePanic checks if a function body has panic calls with arguments.
func (i *Injector) containsRewriteablePanic(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if _, isFunc := n.(*ast.FuncLit); isFunc {
			return false // Don't descend into closures for this check
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if i.isPanicCall(call) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// isPanicCall checks if the call expression is the built-in panic function.
func (i *Injector) isPanicCall(call *ast.CallExpr) bool {
	if ident, ok := call.Fun.(*ast.Ident); ok {
		// Verify it's the builtin panic, not a shadowed variable.
		// In strictly typed checking:
		if i.Pkg.TypesInfo != nil {
			if obj := i.Pkg.TypesInfo.ObjectOf(ident); obj != nil {
				// Builtins usually have no pkg in Object (pkg=nil) and parent is Universe
				if obj.Pkg() == nil && obj.Name() == "panic" {
					return true
				}
				// If TypesInfo is partial/missing (e.g. tests), check Name
				return obj.Name() == "panic"
			}
		}
		// Fallback purely syntactic check (if TypesInfo not populated or incomplete)
		return ident.Name == "panic"
	}
	return false
}

// hasTrailingErrorReturn checks if the function already returns an error as the last value.
func (i *Injector) hasTrailingErrorReturn(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
		return false
	}
	lastField := fn.Type.Results.List[len(fn.Type.Results.List)-1]

	// Check AST type expression
	if i.isErrorExpr(lastField.Type) {
		return true
	}

	// Check TypesInfo if available
	if i.Pkg.TypesInfo != nil {
		if t, ok := i.Pkg.TypesInfo.Types[lastField.Type]; ok {
			return i.isErrorType(t.Type)
		}
	}
	return false
}

// replacePanicsInBody iterates the function body statements and replaces panic calls.
func (i *Injector) replacePanicsInBody(fn *ast.FuncDecl) error {
	var err error

	astutil.Apply(fn.Body, func(c *astutil.Cursor) bool {
		if err != nil {
			return false
		}

		n := c.Node()

		// Skip nested functions
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}

		// Look for ExprStmt containing CallExpr (panic)
		// e.g. "panic(err)"
		if exprStmt, ok := n.(*ast.ExprStmt); ok {
			if call, ok := exprStmt.X.(*ast.CallExpr); ok {
				if i.isPanicCall(call) {
					// Found panic to replace
					retStmt, genErr := i.generateReturnFromPanic(fn, call)
					if genErr != nil {
						err = genErr
						return false
					}
					c.Replace(retStmt)
				}
			}
		}
		return true
	}, nil)

	return err
}

// generateReturnFromPanic constructs the ReturnStmt to replace the panic.
// It generates zero values for non-error returns and processes the panic argument into an error.
func (i *Injector) generateReturnFromPanic(fn *ast.FuncDecl, panicCall *ast.CallExpr) (*ast.ReturnStmt, error) {
	if len(panicCall.Args) == 0 {
		return nil, fmt.Errorf("panic with no arguments logic not supported")
	}
	arg := panicCall.Args[0]

	// 1. Generate Zero Values for preceding returns
	var results []ast.Expr

	// The function signature MUST have 'error' at the end now (we ensured signature update).
	// We iterate all but the last result field.
	// Note: fn.Type.Results.List fields might contain multiple names "a, b int".
	// We must count the actual return values needed.

	resFields := fn.Type.Results.List
	// If the last field is the error we added/verified, we exclude it from zero-gen loop.
	// However, AST fields group names. We iterate fields, and for each name in field, adds a zero.
	// If field has no names, it counts as 1.

	// We need to count total returns to identify the last one (error).
	totalReturns := 0
	for _, f := range resFields {
		if len(f.Names) > 0 {
			totalReturns += len(f.Names)
		} else {
			totalReturns++
		}
	}

	processedCount := 0
	targetCount := totalReturns - 1 // All except the newly added error

	for _, f := range resFields {
		count := len(f.Names)
		if count == 0 {
			count = 1
		}

		// For each return variable defined by this field
		for k := 0; k < count; k++ {
			if processedCount >= targetCount {
				// This is the last one (error), stop
				break
			}

			// Generate zero for this field type
			// We try to get type from TypesInfo.
			var zero ast.Expr
			var zErr error

			// If we have types info for the AST expression
			if i.Pkg != nil && i.Pkg.TypesInfo != nil {
				if t, ok := i.Pkg.TypesInfo.Types[f.Type]; ok {
					zero, zErr = astgen.ZeroExpr(t.Type, astgen.ZeroCtx{})
				}
			}

			// If TypeInfo failed (or stale), fallback to basic AST analysis (optional but robust)
			// For this implementation, we assume TypesInfo works for original types.
			// If nil, astgen might error or we might need fallback.
			if zero == nil {
				// Fallback to "nil" is risky for int, but safe for pointers.
				// Let's produce a safe fallback if TypesInfo is missing?
				// astgen.ZeroExpr requires types.Type.
				if zErr != nil {
					return nil, zErr
				}
				// If we couldn't resolve type, we error out as we can't safely refactor
				return nil, fmt.Errorf("unable to resolve zero value for return position %d", processedCount)
			}

			results = append(results, zero)
			processedCount++
		}
	}

	// 2. Generate Error Expression from Panic Arg
	errExpr := i.convertPanicArgToError(arg)
	results = append(results, errExpr)

	return &ast.ReturnStmt{Results: results}, nil
}

// convertPanicArgToError creates an expression that fits an 'error' return type.
// If arg is already error type -> arg.
// If arg is string -> fmt.Errorf(arg).
// Else -> fmt.Errorf("%v", arg).
//
// This function assumes 'fmt' might need importing.
func (i *Injector) convertPanicArgToError(arg ast.Expr) ast.Expr {
	// Determine type of arg
	isError := false
	isString := false

	if i.Pkg != nil && i.Pkg.TypesInfo != nil {
		if tv, ok := i.Pkg.TypesInfo.Types[arg]; ok {
			if i.isErrorType(tv.Type) {
				isError = true
			} else if basic, ok := tv.Type.(*types.Basic); ok && basic.Info()&types.IsString != 0 {
				isString = true
			}
		}
	}

	// Fallback AST checks if TypesInfo missing (e.g. literals)
	if !isError && !isString {
		if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			isString = true
		}
	}

	if isError {
		return arg
	}

	// Construct fmt.Errorf call
	// If string: fmt.Errorf(arg)
	// If other: fmt.Errorf("%v", arg)

	sel := &ast.SelectorExpr{
		X:   &ast.Ident{Name: "fmt"},
		Sel: &ast.Ident{Name: "Errorf"},
	}

	var args []ast.Expr
	if isString {
		// fmt.Errorf(originalString)
		// NOTE: if originalString contains %, strict vetting might complain,
		// but typically panic messages are static.
		// Safe way: fmt.Errorf("%s", str)
		args = []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: "\"%s\""}, arg}
	} else {
		// fmt.Errorf("%v", obj)
		args = []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: "\"%v\""}, arg}
	}

	return &ast.CallExpr{
		Fun:  sel,
		Args: args,
	}
}
