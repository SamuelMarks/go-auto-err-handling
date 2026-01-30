package rewrite

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/analysis"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/astgen"
	"github.com/dave/dst"
	"github.com/dave/dst/dstutil"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// Injector handles the rewriting of ASTs/DSTs to inject error handling logic.
type Injector struct {
	Fset                *token.FileSet
	Pkg                 *packages.Package
	ErrorTemplate       string
	MainHandlerStrategy string
}

// NewInjector creates a new Injector for the given package.
//
// pkg: The loaded package containing type info.
// errorTemplate: Template string for converting errors to returns (e.g. "{return-zero}, fmt.Errorf(...)").
// mainHandler: Strategy for main/init functions ("log-fatal", "panic", etc).
func NewInjector(pkg *packages.Package, errorTemplate, mainHandler string) *Injector {
	if errorTemplate == "" {
		errorTemplate = "{return-zero}, err"
	}
	if mainHandler == "" {
		mainHandler = "log-fatal"
	}
	return &Injector{
		Fset:                pkg.Fset,
		Pkg:                 pkg,
		ErrorTemplate:       errorTemplate,
		MainHandlerStrategy: mainHandler,
	}
}

// RewriteFile applies specific injection points to a single file using DST transformations.
//
// dstFile: The Decorated Syntax Tree to modify.
// astFile: The original AST file (used for type analysis and mapping).
// points: List of detected unhandled errors.
//
// Returns true if any modification was made.
func (i *Injector) RewriteFile(dstFile *dst.File, astFile *ast.File, points []analysis.InjectionPoint) (bool, error) {
	// 1. Rewrite defers first
	defersApplied, deferErr := i.RewriteDefers(dstFile, astFile)
	if deferErr != nil {
		return false, deferErr
	}

	if len(points) == 0 {
		return defersApplied, nil
	}

	// 2. Map ASTInjectionPoints to DST Stmts
	targetMap := make(map[dst.Stmt]analysis.InjectionPoint)
	for _, p := range points {
		if p.Stmt == nil {
			continue
		}
		// Skip Defers in generic rewrite loop (handled by RewriteDefers)
		if _, isDefer := p.Stmt.(*ast.DeferStmt); isDefer {
			continue
		}

		res, err := FindDstNode(i.Fset, dstFile, astFile, p.Stmt)
		if err != nil {
			continue
		}
		if dstStmt, ok := res.Node.(dst.Stmt); ok {
			targetMap[dstStmt] = p
		}
	}

	applied := false
	var err error

	// 3. Traverse and Apply
	dstutil.Apply(dstFile, func(c *dstutil.Cursor) bool {
		if err != nil {
			return false
		}

		node := c.Node()
		stmt, isStmt := node.(dst.Stmt)
		if !isStmt {
			return true
		}

		// Check if this statement is a target
		point, exists := targetMap[stmt]
		if !exists {
			return true
		}

		// Resolve Context
		astCtx := i.getEnclosingContext(point)
		sig := astCtx.sig
		decl := astCtx.decl

		// Handle specific rewrites
		var newNodes []dst.Stmt
		var genErr error

		switch s := stmt.(type) {
		case *dst.GoStmt:
			var converted *dst.GoStmt
			converted, genErr = i.generateGoRewriteDST(point, s)
			if converted != nil {
				newNodes = []dst.Stmt{converted}
			}
		default:
			// Pass the DST statement to help extract the call
			newNodes, genErr = i.generateRewriteDST(point, stmt, sig, decl)
		}

		if genErr != nil {
			err = genErr
			return false
		}

		if len(newNodes) > 0 {
			// Transfer Trivia
			i.transferTrivia(stmt, newNodes)

			// Replace logic
			c.Replace(newNodes[0])
			for k := len(newNodes) - 1; k > 0; k-- {
				c.InsertAfter(newNodes[k])
			}
			applied = true
		}

		return true
	}, nil)

	return applied || defersApplied, err
}

// LogFallback injects a logging statement for the given error instead of returning it.
func (i *Injector) LogFallback(dstFile *dst.File, astFile *ast.File, point analysis.InjectionPoint) (bool, error) {
	if point.Stmt == nil {
		return false, nil
	}
	res, err := FindDstNode(i.Fset, dstFile, astFile, point.Stmt)
	if err != nil {
		return false, err
	}
	dstStmt, ok := res.Node.(dst.Stmt)
	if !ok {
		return false, nil
	}

	applied := false
	var genErr error

	dstutil.Apply(dstFile, func(c *dstutil.Cursor) bool {
		if c.Node() != dstStmt {
			return true
		}

		var stmts []dst.Stmt
		stmts, genErr = i.generateLogRewriteDST(point, dstStmt)
		if genErr != nil {
			return false
		}

		if len(stmts) > 0 {
			i.transferTrivia(dstStmt, stmts)
			c.Replace(stmts[0])
			for k := len(stmts) - 1; k > 0; k-- {
				c.InsertAfter(stmts[k])
			}
			i.addImportDST(dstFile, "log")
			applied = true
		}
		return false
	}, nil)

	return applied, genErr
}

func (i *Injector) transferTrivia(src dst.Stmt, newStmts []dst.Stmt) {
	if len(newStmts) == 0 {
		return
	}

	first := newStmts[0]
	last := newStmts[len(newStmts)-1]

	first.Decorations().Before = src.Decorations().Before
	first.Decorations().Start = src.Decorations().Start
	first.Decorations().End = src.Decorations().End
	last.Decorations().After = src.Decorations().After

	if len(newStmts) > 1 {
		first.Decorations().After = dst.NewLine
	}
}

type contextInfo struct {
	sig  *types.Signature
	decl *ast.FuncDecl
}

func (i *Injector) getEnclosingContext(p analysis.InjectionPoint) contextInfo {
	path, _ := astutil.PathEnclosingInterval(p.File, p.Pos, p.Pos)
	for _, n := range path {
		if fn, ok := n.(*ast.FuncDecl); ok {
			if obj := i.Pkg.TypesInfo.ObjectOf(fn.Name); obj != nil {
				if sig, ok := obj.Type().(*types.Signature); ok {
					return contextInfo{sig: sig, decl: fn}
				}
			}
		}
		if lit, ok := n.(*ast.FuncLit); ok {
			if tv, ok := i.Pkg.TypesInfo.Types[lit]; ok {
				if sig, ok := tv.Type.(*types.Signature); ok {
					return contextInfo{sig: sig, decl: nil}
				}
			}
		}
	}
	return contextInfo{}
}

// generateRewriteDST creates the DST nodes for assignment and error checking.
func (i *Injector) generateRewriteDST(point analysis.InjectionPoint, dstStmt dst.Stmt, sig *types.Signature, decl *ast.FuncDecl) ([]dst.Stmt, error) {
	useSig := false
	if sig != nil && sig.Results().Len() > 0 {
		last := sig.Results().At(sig.Results().Len() - 1)
		if i.isErrorType(last.Type()) {
			useSig = true
		}
	} else if decl != nil && decl.Type.Results != nil {
		list := decl.Type.Results.List
		if len(list) > 0 {
			if i.isErrorExpr(list[len(list)-1].Type) {
				useSig = true
			}
		}
	}

	if !useSig {
		return nil, nil // Cannot inject return if signature doesn't support error
	}

	scope := i.getScope(point.Pos, point.File)
	errName, tok, declStmt := i.resolveErrorVar(point, scope)
	funcName := i.resolveFuncName(point)

	// Generate Returns
	var zeroExprs []dst.Expr
	if sig != nil {
		limit := sig.Results().Len()
		if i.isErrorType(sig.Results().At(sig.Results().Len() - 1).Type()) {
			limit--
		}
		for idx := 0; idx < limit; idx++ {
			t := sig.Results().At(idx).Type()
			z, err := astgen.ZeroExprDST(t, astgen.ZeroCtx{})
			if err != nil {
				return nil, err
			}
			zeroExprs = append(zeroExprs, z)
		}
	}

	retExprs, _, err := RenderTemplateDST(i.ErrorTemplate, zeroExprs, errName, funcName)
	if err != nil {
		return nil, err
	}

	retStmt := &dst.ReturnStmt{Results: retExprs}

	// Extract DST Call from DST Stmt
	dstCall := i.extractDstCall(dstStmt)
	if dstCall == nil {
		return nil, fmt.Errorf("could not locate call in dst statement")
	}
	// We must Clone it to move it
	dstCallClone := dst.Clone(dstCall).(*dst.CallExpr)
	astgen.ClearDecorations(dstCallClone)

	// Generate Assignment
	assignStmt, err := i.generateAssignmentDST(point, dstCallClone, errName, tok)
	if err != nil {
		return nil, err
	}

	// Check Block
	checkStmt := &dst.IfStmt{
		Cond: &dst.BinaryExpr{
			X:  dst.NewIdent(errName),
			Op: token.NEQ,
			Y:  dst.NewIdent("nil"),
		},
		Body: &dst.BlockStmt{
			List: []dst.Stmt{retStmt},
		},
	}

	var result []dst.Stmt
	if declStmt != nil {
		result = append(result, declStmt)
	}

	if as, ok := assignStmt.(*dst.AssignStmt); ok && declStmt == nil {
		checkStmt.Init = as
		result = append(result, checkStmt)
	} else {
		result = append(result, assignStmt, checkStmt)
	}

	return result, nil
}

func (i *Injector) generateGoRewriteDST(point analysis.InjectionPoint, goStmt *dst.GoStmt) (*dst.GoStmt, error) {
	call := dst.Clone(goStmt.Call).(*dst.CallExpr)
	astgen.ClearDecorations(call)

	errName := "err"
	tok := token.DEFINE

	// Assignment: err := call()
	assignStmt, err := i.generateAssignmentDST(point, call, errName, tok)
	if err != nil {
		return nil, err
	}

	handlerBlock := i.generateTerminalHandlerDST(errName)

	checkStmt := &dst.IfStmt{
		Cond: &dst.BinaryExpr{
			X:  dst.NewIdent(errName),
			Op: token.NEQ,
			Y:  dst.NewIdent("nil"),
		},
		Body: handlerBlock,
	}

	body := &dst.BlockStmt{
		List: []dst.Stmt{
			assignStmt,
			checkStmt,
		},
	}

	return &dst.GoStmt{
		Call: &dst.CallExpr{
			Fun: &dst.FuncLit{
				Type: &dst.FuncType{
					Params:  &dst.FieldList{},
					Results: nil,
				},
				Body: body,
			},
		},
	}, nil
}

func (i *Injector) generateLogRewriteDST(point analysis.InjectionPoint, dstStmt dst.Stmt) ([]dst.Stmt, error) {
	scope := i.getScope(point.Pos, point.File)
	errName, tok, declStmt := i.resolveErrorVar(point, scope)
	funcName := i.resolveFuncName(point)

	dstCall := i.extractDstCall(dstStmt)
	if dstCall == nil {
		return nil, fmt.Errorf("no call in stmt")
	}
	dstCallClone := dst.Clone(dstCall).(*dst.CallExpr)
	astgen.ClearDecorations(dstCallClone)

	assignStmt, err := i.generateAssignmentDST(point, dstCallClone, errName, tok)
	if err != nil {
		return nil, err
	}

	logCall := &dst.CallExpr{
		Fun: &dst.SelectorExpr{
			X:   dst.NewIdent("log"),
			Sel: dst.NewIdent("Printf"),
		},
		Args: []dst.Expr{
			&dst.BasicLit{Kind: token.STRING, Value: fmt.Sprintf(`"ignored error in %s: %%v"`, funcName)},
			dst.NewIdent(errName),
		},
	}

	checkStmt := &dst.IfStmt{
		Cond: &dst.BinaryExpr{
			X:  dst.NewIdent(errName),
			Op: token.NEQ,
			Y:  dst.NewIdent("nil"),
		},
		Body: &dst.BlockStmt{
			List: []dst.Stmt{
				&dst.ExprStmt{X: logCall},
			},
		},
	}

	var result []dst.Stmt
	if declStmt != nil {
		result = append(result, declStmt)
	}

	if as, ok := assignStmt.(*dst.AssignStmt); ok && declStmt == nil {
		checkStmt.Init = as
		result = append(result, checkStmt)
	} else {
		result = append(result, assignStmt, checkStmt)
	}

	return result, nil
}

// extractDstCall finds the CallExpr within a statement.
func (i *Injector) extractDstCall(stmt dst.Stmt) *dst.CallExpr {
	var call *dst.CallExpr
	dst.Inspect(stmt, func(n dst.Node) bool {
		if call != nil {
			return false
		}
		if c, ok := n.(*dst.CallExpr); ok {
			call = c
			return false
		}
		return true
	})
	return call
}

func (i *Injector) generateTerminalHandlerDST(errVar string) *dst.BlockStmt {
	var stmts []dst.Stmt
	switch i.MainHandlerStrategy {
	case "panic":
		stmts = []dst.Stmt{
			&dst.ExprStmt{
				X: &dst.CallExpr{
					Fun:  dst.NewIdent("panic"),
					Args: []dst.Expr{dst.NewIdent(errVar)},
				},
			},
		}
	case "os-exit":
		stmts = []dst.Stmt{
			&dst.ExprStmt{
				X: &dst.CallExpr{
					Fun:  &dst.SelectorExpr{X: dst.NewIdent("fmt"), Sel: dst.NewIdent("Println")},
					Args: []dst.Expr{dst.NewIdent(errVar)},
				},
			},
			&dst.ExprStmt{
				X: &dst.CallExpr{
					Fun:  &dst.SelectorExpr{X: dst.NewIdent("os"), Sel: dst.NewIdent("Exit")},
					Args: []dst.Expr{&dst.BasicLit{Kind: token.INT, Value: "1"}},
				},
			},
		}
	default: // log-fatal
		stmts = []dst.Stmt{
			&dst.ExprStmt{
				X: &dst.CallExpr{
					Fun:  &dst.SelectorExpr{X: dst.NewIdent("log"), Sel: dst.NewIdent("Fatal")},
					Args: []dst.Expr{dst.NewIdent(errVar)},
				},
			},
		}
	}
	return &dst.BlockStmt{List: stmts}
}

func (i *Injector) generateAssignmentDST(point analysis.InjectionPoint, call *dst.CallExpr, errName string, tok token.Token) (dst.Stmt, error) {
	if i.Pkg.TypesInfo == nil {
		return nil, fmt.Errorf("missing types info")
	}
	tv, ok := i.Pkg.TypesInfo.Types[point.Call]
	if !ok {
		return nil, fmt.Errorf("missing type info for call")
	}

	resultLen := 1
	if tuple, ok := tv.Type.(*types.Tuple); ok {
		resultLen = tuple.Len()
	}

	var lhs []dst.Expr

	// Reconstruct LHS
	if point.Assign != nil {
		for idx, expr := range point.Assign.Lhs {
			isLast := idx == len(point.Assign.Lhs)-1
			if isLast {
				lhs = append(lhs, dst.NewIdent(errName))
			} else {
				if id, ok := expr.(*ast.Ident); ok {
					lhs = append(lhs, dst.NewIdent(id.Name))
				} else {
					lhs = append(lhs, dst.NewIdent("_"))
				}
			}
		}
	} else {
		// ExprStmt -> AssignStmt
		for k := 0; k < resultLen-1; k++ {
			lhs = append(lhs, dst.NewIdent("_"))
		}
		lhs = append(lhs, dst.NewIdent(errName))
	}

	return &dst.AssignStmt{
		Lhs: lhs,
		Tok: tok,
		Rhs: []dst.Expr{call},
	}, nil
}

func (i *Injector) resolveErrorVar(point analysis.InjectionPoint, scope *types.Scope) (string, token.Token, *dst.DeclStmt) {
	candidate := "err"
	name := candidate

	if scope != nil {
		unique := analysis.GenerateUniqueName(scope, candidate)
		name = unique
	}

	tok := token.DEFINE
	if point.Assign != nil {
		tok = point.Assign.Tok
	}

	var existingVar *types.Var
	if scope != nil {
		_, obj := scope.LookupParent("err", token.NoPos)
		if v, ok := obj.(*types.Var); ok {
			if i.isErrorType(v.Type()) {
				existingVar = v
			}
		}
	}

	if existingVar != nil {
		name = "err"
		if tok == token.DEFINE {
			return name, token.DEFINE, nil
		}
		return name, token.ASSIGN, nil
	}

	return name, token.DEFINE, nil
}

func (i *Injector) resolveFuncName(point analysis.InjectionPoint) string {
	if point.Call == nil {
		return "func"
	}
	if id, ok := point.Call.Fun.(*ast.Ident); ok {
		return id.Name
	}
	if sel, ok := point.Call.Fun.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	return "func"
}

func (i *Injector) getScope(pos token.Pos, file *ast.File) *types.Scope {
	if i.Pkg.TypesInfo == nil {
		return nil
	}
	path, _ := astutil.PathEnclosingInterval(file, pos, pos)
	for _, n := range path {
		if s := i.Pkg.TypesInfo.Scopes[n]; s != nil {
			return s
		}
	}
	return i.Pkg.Types.Scope()
}

func (i *Injector) isErrorType(t types.Type) bool {
	return t.String() == "error" || t.String() == "builtin.error" ||
		types.Identical(t, types.Universe.Lookup("error").Type())
}

func (i *Injector) isErrorExpr(e ast.Expr) bool {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name == "error"
	}
	return false
}

func (i *Injector) addImportDST(file *dst.File, path string) {
	for _, imp := range file.Imports {
		if imp.Path != nil && imp.Path.Value == fmt.Sprintf(`"%s"`, path) {
			return
		}
	}
	decl := &dst.GenDecl{
		Tok: token.IMPORT,
		Specs: []dst.Spec{
			&dst.ImportSpec{
				Path: &dst.BasicLit{Kind: token.STRING, Value: fmt.Sprintf(`"%s"`, path)},
			},
		},
	}
	file.Decls = append([]dst.Decl{decl}, file.Decls...)
}
