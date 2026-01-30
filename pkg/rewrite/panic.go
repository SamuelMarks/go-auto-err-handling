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

// RewritePanics scans the provided DST file for explicit explicit panic calls (e.g., panic(err))
// and converts them into return statements with an error.
//
// Analysis is performed on the AST (for type safety), and transformations are applied
// to the DST (for comment preservation).
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
		mapRes, err := FindDstNode(i.Fset, dstFile, astFile, astFn)
		if err != nil {
			return applied, fmt.Errorf("failed to map function %s to DST: %w", astFn.Name.Name, err)
		}
		dstFn, ok := mapRes.Node.(*dst.FuncDecl)
		if !ok {
			continue
		}

		needsSigUpdate := !i.hasTrailingErrorReturnDST(dstFn)
		if needsSigUpdate {
			changed, err := refactor.AddErrorToSignatureDST(dstFn)
			if err != nil {
				return applied, err
			}
			if changed {
				applied = true
			}
		}

		for _, astPanic := range panics {
			panicMapRes, err := FindDstNode(i.Fset, dstFile, astFile, astPanic)
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
	}

	return applied, nil
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

	var results []dst.Expr
	resFields := fn.Type.Results.List
	totalReturns := 0
	for _, f := range resFields {
		if len(f.Names) > 0 {
			totalReturns += len(f.Names)
		} else {
			totalReturns++
		}
	}

	processedCount := 0
	targetCount := totalReturns - 1

	for _, f := range resFields {
		count := len(f.Names)
		if count == 0 {
			count = 1
		}
		for k := 0; k < count; k++ {
			if processedCount >= targetCount {
				break
			}
			z := guessZeroDST(f.Type)
			results = append(results, z)
			processedCount++
		}
	}

	errExpr := i.convertPanicArgToErrorDST(dstArg, astArg)
	results = append(results, errExpr)

	return &dst.ReturnStmt{Results: results}, nil
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
