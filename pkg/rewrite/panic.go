package rewrite

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/astgen"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/refactor"
	"github.com/dave/dst"
	"golang.org/x/tools/go/ast/astutil"
)

// addErrorToSignatureDST is a test hook for overriding AddErrorToSignatureDST behavior.
var addErrorToSignatureDST = refactor.AddErrorToSignatureDST

// ensureTerminalReturnFunc is a test hook for overriding ensureTerminalReturn behavior.
var ensureTerminalReturnFunc = func(i *Injector, fn *dst.FuncDecl, astFn *ast.FuncDecl) error {
	return i.ensureTerminalReturn(fn, astFn)
}

// RewritePanics scans the provided DST file for explicit explicit panic calls (e.g., panic(err))
// and converts them into return statements with an error.
//
// Analysis is performed on the AST (for type safety), and transformations are applied
// to the DST (for comment preservation).
//
// It performs basic reachability analysis: if replacing a panic removal exposes a
// fall-through path in a non-void function (i.e. missing return), it injects a
// terminal return statement.
//
// If RetainPanics is enabled, it skips panics that do not appear to wrap errors
// (e.g. string assertions).
//
// dstFile: The DST file to modify.
// astFile: The AST file corresponding to the DST file (used for type analysis).
//
// Returns true if any changes were applied.
func (i *Injector) RewritePanics(dstFile *dst.File, astFile *ast.File) (bool, error) {
	if dstFile == nil || astFile == nil {
		return false, fmt.Errorf("files cannot be nil")
	}

	// 1. Identify Candidates via AST Analysis
	candidates := make(map[*ast.FuncDecl][]*ast.CallExpr)

	astutil.Apply(astFile, func(c *astutil.Cursor) bool {
		node := c.Node()
		fnDecl, ok := node.(*ast.FuncDecl)
		if !ok {
			return true
		}

		var panicCalls []*ast.CallExpr
		ast.Inspect(fnDecl.Body, func(n ast.Node) bool {
			if _, isClosure := n.(*ast.FuncLit); isClosure {
				return false
			}
			if call, ok := n.(*ast.CallExpr); ok {
				if i.isPanicCall(call) {
					// Check RetainPanics logic
					if i.RetainPanics {
						// Only allow if arg is error
						if len(call.Args) > 0 {
							if !i.isErrorArg(call.Args[0]) {
								return true // Skip this panic
							}
						}
					}
					panicCalls = append(panicCalls, call)
				}
			}
			return true
		})

		if len(panicCalls) > 0 {
			candidates[fnDecl] = panicCalls
		}
		return false
	}, nil)

	if len(candidates) == 0 {
		return false, nil
	}

	applied := false

	// 2. Apply Transformations to DST
	for astFn, panics := range candidates {
		mapRes, err := findDstNodeFunc(i.Fset, dstFile, astFile, astFn)
		if err != nil {
			return applied, fmt.Errorf("failed to map function %s to DST: %w", astFn.Name.Name, err)
		}
		dstFn, ok := mapRes.Node.(*dst.FuncDecl)
		if !ok {
			continue
		}

		needsSigUpdate := !i.hasTrailingErrorReturnDST(dstFn)
		if needsSigUpdate {
			changed, err := addErrorToSignatureDST(dstFn)
			if err != nil {
				return applied, err
			}
			if changed {
				applied = true
			}
		}

		for _, astPanic := range panics {
			panicMapRes, err := findDstNodeFunc(i.Fset, dstFile, astFile, astPanic)
			if err != nil {
				return applied, fmt.Errorf("failed to map panic call to DST: %w", err)
			}
			dstPanicCall, ok := panicMapRes.Node.(*dst.CallExpr)
			if !ok {
				continue
			}

			stmt, ok := panicMapRes.Parent.(*dst.ExprStmt)
			if !ok {
				continue
			}

			retStmt, err := i.generateReturnFromPanicDST(dstFn, dstPanicCall, astPanic)
			if err != nil {
				return applied, err
			}

			// Capture Trivia from original statement
			retStmt.Decorations().Before = stmt.Decorations().Before
			retStmt.Decorations().Start = stmt.Decorations().Start
			retStmt.Decorations().End = stmt.Decorations().End
			retStmt.Decorations().After = stmt.Decorations().After

			if replaceDstStmt(dstFn.Body, stmt, retStmt) {
				applied = true
			}
		}

		// 3. Control Flow Check: Ensure function terminates if we removed panics
		// Only check if we modified the function.
		// Use astFn to get types for zero value generation.
		if err := ensureTerminalReturnFunc(i, dstFn, astFn); err != nil {
			// soft failure?
			// return applied, err
		}
	}

	return applied, nil
}

// ensureTerminalReturn checks if the function body falls through appearing to miss a return statement
// (common when converting panic -> return errors in if-blocks).
// If so, it appends a zero-value return statement.
func (i *Injector) ensureTerminalReturn(fn *dst.FuncDecl, astFn *ast.FuncDecl) error {
	if fn.Body == nil {
		return nil
	}
	if i.isTerminating(fn.Body) {
		return nil
	}

	// Not terminating. Check if signature expects returns.
	if fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
		return nil
	}

	// Generate zero returns based on AST types (more reliable than DST for types)
	var results []dst.Expr

	// Iterate using AST type info
	if astFn != nil && astFn.Type.Results != nil {
		for _, field := range astFn.Type.Results.List {
			// Resolve type from AST
			// We need to check TypeInfo for the Type Expr to get types.Type
			// Or traverse logic locally.
			count := len(field.Names)
			if count == 0 {
				count = 1
			}

			// Lookup type
			var t types.Type
			if i.Pkg != nil && i.Pkg.TypesInfo != nil {
				if tv, ok := i.Pkg.TypesInfo.Types[field.Type]; ok {
					t = tv.Type
				}
			}

			for k := 0; k < count; k++ {
				var z dst.Expr
				if t != nil {
					// Use ASTGen
					var err error
					z, err = astgen.ZeroExprDST(t, astgen.ZeroCtx{})
					if err != nil {
						// Fallback to guess
						z = guessZeroDSTFromExpr(field.Type) // heuristic on AST expr
					}
				} else {
					// Fallback
					z = dst.NewIdent("nil")
				}
				results = append(results, z)
			}
		}
	} else {
		// Fallback purely on DST structure if AST mapping weak
		results = i.generateZerosFromDST(fn.Type)
	}

	// Append return stmt
	ret := &dst.ReturnStmt{
		Results: results,
	}
	// Formatting: ensure newline
	ret.Decorations().Before = dst.NewLine

	fn.Body.List = append(fn.Body.List, ret)
	return nil
}

func (i *Injector) isTerminating(stmt dst.Stmt) bool {
	switch s := stmt.(type) {
	case *dst.ReturnStmt:
		return true
	case *dst.BlockStmt:
		if len(s.List) == 0 {
			return false
		}
		return i.isTerminating(s.List[len(s.List)-1])
	case *dst.IfStmt:
		// Needs both branches
		if s.Else == nil {
			return false
		}
		return i.isTerminating(s.Body) && i.isTerminating(s.Else)
	case *dst.ForStmt:
		// Infinite loop without break?
		// Simple heuristic: if Cond is missing, assume infinite.
		// (Advanced: check for breaks. We do simple check).
		if s.Cond == nil {
			return true // "for {" is terminating unless broken, assume safe to not add return after
		}
		return false // "for cond {" might not run
	case *dst.SwitchStmt:
		// Needs default and all cases terminating
		hasDefault := false
		for _, clause := range s.Body.List {
			cc, ok := clause.(*dst.CaseClause)
			if !ok {
				continue
			}
			if cc.List == nil {
				hasDefault = true
			}
			// Check list of statements in case
			if len(cc.Body) == 0 {
				if !hasDefault {
					// empty case might fallthrough? No, implicit break.
					// If empty, it doesn't terminate.
					return false
				}
			} else {
				if !i.isTerminating(&dst.BlockStmt{List: cc.Body}) {
					return false
				}
			}
		}
		return hasDefault
	case *dst.SelectStmt:
		// Similar to switch, needs default
		hasDefault := false
		for _, clause := range s.Body.List {
			cc, ok := clause.(*dst.CommClause)
			if !ok {
				continue
			}
			if cc.Comm == nil {
				hasDefault = true
			}
			if len(cc.Body) == 0 {
				return false // break
			}
			if !i.isTerminating(&dst.BlockStmt{List: cc.Body}) {
				return false
			}
		}
		return hasDefault
	case *dst.ExprStmt:
		// Check for panic
		if call, ok := s.X.(*dst.CallExpr); ok {
			if id, ok := call.Fun.(*dst.Ident); ok && id.Name == "panic" {
				return true
			}
		}
	}
	return false
}

// replaceDstStmt matches a statement by pointer identity and replaces it in a BlockStmt.
func replaceDstStmt(block *dst.BlockStmt, target, replacement dst.Stmt) bool {
	found := false
	dst.Inspect(block, func(n dst.Node) bool {
		if found {
			return false
		}
		if blk, ok := n.(*dst.BlockStmt); ok {
			for idx, s := range blk.List {
				if s == target {
					blk.List[idx] = replacement
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

func (i *Injector) isPanicCall(call *ast.CallExpr) bool {
	if ident, ok := call.Fun.(*ast.Ident); ok {
		if i.Pkg.TypesInfo != nil {
			if obj := i.Pkg.TypesInfo.ObjectOf(ident); obj != nil {
				if obj.Pkg() == nil && obj.Name() == "panic" {
					return true
				}
				return obj.Name() == "panic"
			}
		}
		return ident.Name == "panic"
	}
	return false
}

func (i *Injector) isErrorArg(arg ast.Expr) bool {
	if i.Pkg != nil && i.Pkg.TypesInfo != nil {
		if tv, ok := i.Pkg.TypesInfo.Types[arg]; ok {
			return i.isErrorType(tv.Type)
		}
	}
	return false
}

func (i *Injector) hasTrailingErrorReturnDST(fn *dst.FuncDecl) bool {
	if fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
		return false
	}
	lastField := fn.Type.Results.List[len(fn.Type.Results.List)-1]
	if id, ok := lastField.Type.(*dst.Ident); ok {
		return id.Name == "error"
	}
	return false
}

func (i *Injector) generateReturnFromPanicDST(fn *dst.FuncDecl, panicCall *dst.CallExpr, astPanicCall *ast.CallExpr) (*dst.ReturnStmt, error) {
	if len(panicCall.Args) == 0 {
		return nil, fmt.Errorf("panic with no arguments not supported")
	}
	dstArg := panicCall.Args[0]
	astgen.ClearDecorations(dstArg)
	astArg := astPanicCall.Args[0]

	// Use heuristic DST zero generation corresponding to result slots
	// We want to match the *current* DST signature (which might have been updated).
	// Since we are inside the rewrite, relying on basic heuristics for now is safer
	// than trying to resync with AST which might be stale regarding the new "error" return.
	results := i.generateZerosFromDST(fn.Type)

	// Determine if we need to wrap the error
	errExpr := i.convertPanicArgToErrorDST(dstArg, astArg)

	// The last result in 'results' should be the error slot.
	// If the function returns (int, int, error), generateZerosFromDST produces (0, 0, nil).
	// We replace the last 'nil' with our errExpr.
	if len(results) > 0 {
		results[len(results)-1] = errExpr
	} else {
		// Should not happen if AddErrorToSignatureDST worked
		results = append(results, errExpr)
	}

	return &dst.ReturnStmt{Results: results}, nil
}

func (i *Injector) generateZerosFromDST(ft *dst.FuncType) []dst.Expr {
	var results []dst.Expr
	if ft.Results == nil {
		return results
	}

	for _, field := range ft.Results.List {
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		for k := 0; k < count; k++ {
			z := guessZeroDST(field.Type)
			results = append(results, z)
		}
	}
	return results
}

func guessZeroDST(t dst.Expr) dst.Expr {
	switch x := t.(type) {
	case *dst.Ident:
		switch x.Name {
		case "int", "int8", "int16", "int32", "int64",
			"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
			"byte", "rune", "float32", "float64", "complex64", "complex128":
			return &dst.BasicLit{Kind: token.INT, Value: "0"}
		case "bool":
			return dst.NewIdent("false")
		case "string":
			return &dst.BasicLit{Kind: token.STRING, Value: `""`}
		}
	case *dst.StarExpr, *dst.MapType, *dst.ArrayType, *dst.ChanType, *dst.FuncType, *dst.InterfaceType:
		return dst.NewIdent("nil")
	}
	return dst.NewIdent("nil")
}

func guessZeroDSTFromExpr(expr ast.Expr) dst.Expr {
	// Simple mapping from AST expr to DST zero
	switch x := expr.(type) {
	case *ast.Ident:
		if x.Name == "string" {
			return &dst.BasicLit{Kind: token.STRING, Value: `""`}
		}
		if x.Name == "bool" {
			return dst.NewIdent("false")
		}
		// Assume numeric default
		return &dst.BasicLit{Kind: token.INT, Value: "0"}
	case *ast.StarExpr, *ast.ArrayType, *ast.MapType, *ast.InterfaceType, *ast.ChanType:
		return dst.NewIdent("nil")
	}
	return dst.NewIdent("nil")
}

func (i *Injector) convertPanicArgToErrorDST(dstArg dst.Expr, astArg ast.Expr) dst.Expr {
	isError := false
	isString := false

	if i.Pkg != nil && i.Pkg.TypesInfo != nil {
		if tv, ok := i.Pkg.TypesInfo.Types[astArg]; ok {
			if i.isErrorType(tv.Type) {
				isError = true
			} else if basic, ok := tv.Type.(*types.Basic); ok && basic.Info()&types.IsString != 0 {
				isString = true
			}
		}
	}

	if !isError && !isString {
		if lit, ok := astArg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			isString = true
		}
	}

	if isError {
		return dst.Clone(dstArg).(dst.Expr)
	}

	sel := &dst.SelectorExpr{
		X:   dst.NewIdent("fmt"),
		Sel: dst.NewIdent("Errorf"),
	}

	var args []dst.Expr
	if isString {
		args = []dst.Expr{
			&dst.BasicLit{Kind: token.STRING, Value: `"%s"`},
			dst.Clone(dstArg).(dst.Expr),
		}
	} else {
		args = []dst.Expr{
			&dst.BasicLit{Kind: token.STRING, Value: `"%v"`},
			dst.Clone(dstArg).(dst.Expr),
		}
	}

	return &dst.CallExpr{
		Fun:  sel,
		Args: args,
	}
}
