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
	RetainPanics        bool
	// TestParam is the name of the testing parameter (e.g. "t") for the current injection context.
	// If set, errors are handled via t.Fatal(err) instead of being returned or logged.
	TestParam string
}

// NewInjector creates a new Injector for the given package.
//
// pkg: The loaded package containing type info.
// errorTemplate: Template string for converting errors to returns (e.g. "{return-zero}, fmt.Errorf(...)").
// mainHandler: Strategy for main/init functions ("log-fatal", "panic", etc).
// retainPanics: If true, limits panic rewriting to error types only (preserving string assertions).
func NewInjector(pkg *packages.Package, errorTemplate, mainHandler string, retainPanics bool) *Injector {
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
		RetainPanics:        retainPanics,
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

		// --- Control Structure Initialization Lifting ---

		// Case: IfStmt
		if ifStmt, ok := stmt.(*dst.IfStmt); ok {
			handled, replacements, liftErr := i.tryLiftIf(dstFile, ifStmt, targetMap, astFile)
			if liftErr != nil {
				err = liftErr
				return false
			}
			if handled {
				i.replaceCursor(c, stmt, replacements)
				applied = true
				return false
			}
		}

		// Case: SwitchStmt
		if swStmt, ok := stmt.(*dst.SwitchStmt); ok {
			handled, replacements, liftErr := i.tryLiftSwitch(dstFile, swStmt, targetMap, astFile)
			if liftErr != nil {
				err = liftErr
				return false
			}
			if handled {
				i.replaceCursor(c, stmt, replacements)
				applied = true
				return false
			}
		}

		// Case: TypeSwitchStmt (switch x := y.(type))
		if typeSw, ok := stmt.(*dst.TypeSwitchStmt); ok {
			handled, replacements, liftErr := i.tryLiftTypeSwitch(dstFile, typeSw, targetMap, astFile)
			if liftErr != nil {
				err = liftErr
				return false
			}
			if handled {
				i.replaceCursor(c, stmt, replacements)
				applied = true
				return false
			}
		}

		// --- End Control Structure Lifting ---

		// Standard Simple Rewrite
		point, exists := targetMap[stmt]
		if !exists {
			return true
		}

		// Detect Tail Position for Optimization
		isTail := false
		if block, ok := c.Parent().(*dst.BlockStmt); ok {
			// Check if this is the last statement in the list
			if c.Index() == len(block.List)-1 {
				isTail = true
			}
		}

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
			// Resolve call
			callRes, cErr := FindDstNode(i.Fset, dstFile, astFile, point.Call)
			if cErr != nil {
				// Fallback generic generation
				newNodes, genErr = i.generateRewriteDST(point, stmt, nil, nil, nil, true, false)
			} else {
				if dstCall, ok := callRes.Node.(*dst.CallExpr); ok {
					astCtx := i.getEnclosingContext(point)
					// Pass collapsed=true for standard statements and isTail check
					newNodes, genErr = i.generateRewriteDST(point, stmt, dstCall, astCtx.sig, astCtx.decl, true, isTail)
				}
			}
		}

		if genErr != nil {
			err = genErr
			return false
		}

		if len(newNodes) > 0 {
			i.transferTrivia(stmt, newNodes)
			i.replaceCursor(c, stmt, newNodes)
			applied = true
		}

		return true
	}, nil)

	return applied || defersApplied, err
}

// Helpers for Control Lifting

func (i *Injector) tryLiftIf(dstFile *dst.File, ifStmt *dst.IfStmt, targetMap map[dst.Stmt]analysis.InjectionPoint, astFile *ast.File) (bool, []dst.Stmt, error) {
	// A: Init
	if ifStmt.Init != nil {
		if pt, exists := targetMap[ifStmt.Init.(dst.Stmt)]; exists {
			res, err := i.liftControlInit(dstFile, ifStmt, ifStmt.Init.(dst.Stmt), pt, astFile)
			return true, res, err
		}
	}
	// B: Cond
	if pt, exists := targetMap[ifStmt]; exists {
		res, err := i.liftControlExpr(dstFile, ifStmt, ifStmt.Cond, pt, "cond", astFile, func(n dst.Node, e dst.Expr) {
			n.(*dst.IfStmt).Cond = e
		})
		return true, res, err
	}
	return false, nil, nil
}

func (i *Injector) tryLiftSwitch(dstFile *dst.File, swStmt *dst.SwitchStmt, targetMap map[dst.Stmt]analysis.InjectionPoint, astFile *ast.File) (bool, []dst.Stmt, error) {
	// A: Init
	if swStmt.Init != nil {
		if pt, exists := targetMap[swStmt.Init.(dst.Stmt)]; exists {
			res, err := i.liftControlInit(dstFile, swStmt, swStmt.Init.(dst.Stmt), pt, astFile)
			return true, res, err
		}
	}
	// B: Tag
	if swStmt.Tag != nil {
		if pt, exists := targetMap[swStmt]; exists {
			res, err := i.liftControlExpr(dstFile, swStmt, swStmt.Tag, pt, "tag", astFile, func(n dst.Node, e dst.Expr) {
				n.(*dst.SwitchStmt).Tag = e
			})
			return true, res, err
		}
	}
	return false, nil, nil
}

func (i *Injector) tryLiftTypeSwitch(dstFile *dst.File, ts *dst.TypeSwitchStmt, targetMap map[dst.Stmt]analysis.InjectionPoint, astFile *ast.File) (bool, []dst.Stmt, error) {
	// A: Init
	if ts.Init != nil {
		if pt, exists := targetMap[ts.Init.(dst.Stmt)]; exists {
			res, err := i.liftControlInit(dstFile, ts, ts.Init.(dst.Stmt), pt, astFile)
			return true, res, err
		}
	}
	// B: Assign
	if pt, exists := targetMap[ts]; exists {
		res, err := i.liftTypeSwitchAssign(dstFile, ts, pt, astFile)
		return true, res, err
	}
	return false, nil, nil
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

// liftControlInit lifts...
func (i *Injector) liftControlInit(dstFile *dst.File, controlStmt dst.Node, initStmt dst.Stmt, point analysis.InjectionPoint, astFile *ast.File) ([]dst.Stmt, error) {
	// 1. Locate Call
	callRes, err := FindDstNode(i.Fset, dstFile, astFile, point.Call)
	if err != nil {
		return nil, fmt.Errorf("failed to map call in init: %w", err)
	}
	dstCall, ok := callRes.Node.(*dst.CallExpr)
	if !ok {
		return nil, fmt.Errorf("mapped call is not a CallExpr")
	}

	astCtx := i.getEnclosingContext(point)
	// Pass collapsed=false to ensure assignment remains separate from check
	// This ensures variables declared in Init are visible to the subsequent control statement.
	preamble, err := i.generateRewriteDST(point, initStmt, dstCall, astCtx.sig, astCtx.decl, false, false)
	if err != nil {
		return nil, err
	}

	// 2. Clone Control
	clonedControl := dst.Clone(controlStmt).(dst.Stmt)
	switch s := clonedControl.(type) {
	case *dst.IfStmt:
		s.Init = nil
	case *dst.SwitchStmt:
		s.Init = nil
	case *dst.TypeSwitchStmt:
		s.Init = nil
	}

	// 3. Construct Block
	block := &dst.BlockStmt{
		List: append(preamble, clonedControl),
	}

	// Transfer decorations from original to block
	src := controlStmt.(dst.Stmt)
	block.Decorations().Before = src.Decorations().Before
	block.Decorations().Start = src.Decorations().Start
	block.Decorations().End = src.Decorations().End
	block.Decorations().After = src.Decorations().After

	return []dst.Stmt{block}, nil
}

func (i *Injector) liftControlExpr(dstFile *dst.File, controlStmt dst.Node, expr dst.Expr, point analysis.InjectionPoint, varNameHint string, astFile *ast.File, setter func(dst.Node, dst.Expr)) ([]dst.Stmt, error) {
	astCtx := i.getEnclosingContext(point)
	scope := i.getScope(point.Pos, point.File)
	errName, _, _ := i.resolveErrorVar(point, scope)

	callRes, err := FindDstNode(i.Fset, dstFile, astFile, point.Call)
	if err != nil {
		return nil, fmt.Errorf("failed to map call in cond: %w", err)
	}
	dstCall, ok := callRes.Node.(*dst.CallExpr)
	if !ok {
		return nil, fmt.Errorf("mapped cond call is not CallExpr")
	}

	isSingleError := true
	if i.Pkg.TypesInfo != nil {
		if tv, ok := i.Pkg.TypesInfo.Types[point.Call]; ok {
			if tuple, ok := tv.Type.(*types.Tuple); ok && tuple.Len() > 1 {
				isSingleError = false
			}
		}
	}

	var lhs []dst.Expr
	var conditionReplace dst.Expr

	if isSingleError {
		lhs = []dst.Expr{dst.NewIdent(errName)}
		conditionReplace = dst.NewIdent(errName)
	} else {
		valName := varNameHint
		if scope != nil {
			valName = analysis.GenerateUniqueName(scope, varNameHint)
		}
		lhs = []dst.Expr{dst.NewIdent(valName), dst.NewIdent(errName)}
		conditionReplace = dst.NewIdent(valName)
	}

	assign := &dst.AssignStmt{
		Lhs: lhs,
		Tok: token.DEFINE,
		Rhs: []dst.Expr{dst.Clone(dstCall).(*dst.CallExpr)},
	}
	astgen.ClearDecorations(assign)

	var checkStmt *dst.IfStmt
	if i.TestParam != "" {
		checkStmt = &dst.IfStmt{
			Cond: &dst.BinaryExpr{X: dst.NewIdent(errName), Op: token.NEQ, Y: dst.NewIdent("nil")},
			Body: i.generateTerminalHandlerDST(errName),
		}
	} else {
		zeroExprs, err := i.generateZeroReturns(astCtx.sig, astCtx.decl)
		if err != nil {
			return nil, err
		}
		retExprs, _, err := RenderTemplateDST(i.ErrorTemplate, zeroExprs, errName, i.resolveFuncName(point))
		if err != nil {
			return nil, err
		}
		checkStmt = &dst.IfStmt{
			Cond: &dst.BinaryExpr{X: dst.NewIdent(errName), Op: token.NEQ, Y: dst.NewIdent("nil")},
			Body: &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{Results: retExprs}}},
		}
	}

	clonedControl := dst.Clone(controlStmt).(dst.Stmt)
	setter(clonedControl, conditionReplace)

	block := &dst.BlockStmt{
		List: []dst.Stmt{assign, checkStmt, clonedControl},
	}
	src := controlStmt.(dst.Stmt)
	block.Decorations().Before = src.Decorations().Before
	block.Decorations().Start = src.Decorations().Start
	block.Decorations().End = src.Decorations().End
	block.Decorations().After = src.Decorations().After

	return []dst.Stmt{block}, nil
}

func (i *Injector) liftTypeSwitchAssign(dstFile *dst.File, ts *dst.TypeSwitchStmt, point analysis.InjectionPoint, astFile *ast.File) ([]dst.Stmt, error) {
	callRes, err := FindDstNode(i.Fset, dstFile, astFile, point.Call)
	if err != nil {
		return nil, fmt.Errorf("failed to map call in type switch: %w", err)
	}
	dstCall, ok := callRes.Node.(*dst.CallExpr)
	if !ok {
		return nil, fmt.Errorf("mapped call not expr")
	}

	astCtx := i.getEnclosingContext(point)
	scope := i.getScope(point.Pos, point.File)
	errName, _, _ := i.resolveErrorVar(point, scope)
	valName := analysis.GenerateUniqueName(scope, "val")

	assign := &dst.AssignStmt{
		Lhs: []dst.Expr{dst.NewIdent(valName), dst.NewIdent(errName)},
		Tok: token.DEFINE,
		Rhs: []dst.Expr{dst.Clone(dstCall).(*dst.CallExpr)},
	}
	astgen.ClearDecorations(assign)

	var checkStmt *dst.IfStmt
	if i.TestParam != "" {
		checkStmt = &dst.IfStmt{
			Cond: &dst.BinaryExpr{X: dst.NewIdent(errName), Op: token.NEQ, Y: dst.NewIdent("nil")},
			Body: i.generateTerminalHandlerDST(errName),
		}
	} else {
		zeroExprs, _ := i.generateZeroReturns(astCtx.sig, astCtx.decl)
		retExprs, _, _ := RenderTemplateDST(i.ErrorTemplate, zeroExprs, errName, i.resolveFuncName(point))
		checkStmt = &dst.IfStmt{
			Cond: &dst.BinaryExpr{X: dst.NewIdent(errName), Op: token.NEQ, Y: dst.NewIdent("nil")},
			Body: &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{Results: retExprs}}},
		}
	}

	clonedTs := dst.Clone(ts).(*dst.TypeSwitchStmt)

	// Update cloned switch
	replaced := false
	if assignStmt, ok := clonedTs.Assign.(*dst.AssignStmt); ok {
		if ta, ok := assignStmt.Rhs[0].(*dst.TypeAssertExpr); ok {
			ta.X = dst.NewIdent(valName)
			replaced = true
		}
	} else if exprStmt, ok := clonedTs.Assign.(*dst.ExprStmt); ok {
		if ta, ok := exprStmt.X.(*dst.TypeAssertExpr); ok {
			ta.X = dst.NewIdent(valName)
			replaced = true
		}
	}

	if !replaced {
		// Fallback structural search if direct cast failed (unlikely for valid type switches)
		dst.Inspect(clonedTs.Assign, func(n dst.Node) bool {
			if replaced {
				return false
			}
			if ta, ok := n.(*dst.TypeAssertExpr); ok {
				ta.X = dst.NewIdent(valName)
				replaced = true
				return false
			}
			return true
		})
	}

	block := &dst.BlockStmt{List: []dst.Stmt{assign, checkStmt, clonedTs}}
	block.Decorations().Before = ts.Decorations().Before
	block.Decorations().Start = ts.Decorations().Start

	return []dst.Stmt{block}, nil
}

func (i *Injector) replaceCursor(c *dstutil.Cursor, oldNode dst.Stmt, newNodes []dst.Stmt) {
	c.Replace(newNodes[0])
	for k := len(newNodes) - 1; k > 0; k-- {
		c.InsertAfter(newNodes[k])
	}
}

// transferTrivia copies decorations from the source statement to the beginning and end
// of the replacement list to preserve comments and spacing.
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
// if collapsed is true, assignment is put into Init of IfStmt (if compatible).
func (i *Injector) generateRewriteDST(point analysis.InjectionPoint, dstStmt dst.Stmt, dstCall *dst.CallExpr, sig *types.Signature, decl *ast.FuncDecl, collapsed bool, isTail bool) ([]dst.Stmt, error) {
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

	if !useSig && i.TestParam == "" {
		return nil, nil // Cannot inject return if signature doesn't support error AND not in test
	}

	// Extract DST Call from DST Stmt if not provided
	if dstCall == nil {
		dstCall = i.extractDstCall(dstStmt)
	}
	if dstCall == nil {
		return nil, fmt.Errorf("could not locate call in dst statement")
	}
	dstCallClone := dst.Clone(dstCall).(*dst.CallExpr)
	astgen.ClearDecorations(dstCallClone)

	// --- PASSTHROUGH OPTIMIZATION ---
	// If this is a tail call and signatures match exactly, we inject 'return call()'
	// Note: Passthrough optimization only valid if NOT in test context (where we want t.Fatal)
	if isTail && i.TestParam == "" && sig != nil && point.Call != nil && i.signaturesMatch(sig, point.Call) {
		return []dst.Stmt{
			&dst.ReturnStmt{
				Results: []dst.Expr{dstCallClone},
			},
		}, nil
	}
	// -------------------------------

	scope := i.getScope(point.Pos, point.File)
	errName, tok, declStmt := i.resolveErrorVar(point, scope)
	funcName := i.resolveFuncName(point)

	// Generate Assignment
	assignStmt, err := i.generateAssignmentDST(point, dstCallClone, errName, tok)
	if err != nil {
		return nil, err
	}

	// Check Block
	var handlerBlock *dst.BlockStmt

	if i.TestParam != "" {
		handlerBlock = i.generateTerminalHandlerDST(errName)
	} else {
		// Generate Returns
		zeroExprs, err := i.generateZeroReturns(sig, decl)
		if err != nil {
			return nil, err
		}

		retExprs, _, err := RenderTemplateDST(i.ErrorTemplate, zeroExprs, errName, funcName)
		if err != nil {
			return nil, err
		}
		retStmt := &dst.ReturnStmt{Results: retExprs}
		handlerBlock = &dst.BlockStmt{
			List: []dst.Stmt{retStmt},
		}
	}

	checkStmt := &dst.IfStmt{
		Cond: &dst.BinaryExpr{
			X:  dst.NewIdent(errName),
			Op: token.NEQ,
			Y:  dst.NewIdent("nil"),
		},
		Body: handlerBlock,
	}

	var result []dst.Stmt
	if declStmt != nil {
		result = append(result, declStmt)
	}

	if collapsed {
		if as, ok := assignStmt.(*dst.AssignStmt); ok && declStmt == nil {
			checkStmt.Init = as
			result = append(result, checkStmt)
		} else {
			result = append(result, assignStmt, checkStmt)
		}
	} else {
		result = append(result, assignStmt, checkStmt)
	}

	return result, nil
}

func (i *Injector) generateZeroReturns(sig *types.Signature, decl *ast.FuncDecl) ([]dst.Expr, error) {
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
	return zeroExprs, nil
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

	if i.TestParam != "" {
		// t.Fatal(err)
		stmts = []dst.Stmt{
			&dst.ExprStmt{
				X: &dst.CallExpr{
					Fun: &dst.SelectorExpr{
						X:   dst.NewIdent(i.TestParam),
						Sel: dst.NewIdent("Fatal"),
					},
					Args: []dst.Expr{dst.NewIdent(errVar)},
				},
			},
		}
		return &dst.BlockStmt{List: stmts}
	}

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
	// If the error return is already being assigned to a variable (not _), use that name
	if point.Assign != nil && len(point.Assign.Lhs) > 0 {
		lastIdx := len(point.Assign.Lhs) - 1
		if id, ok := point.Assign.Lhs[lastIdx].(*ast.Ident); ok && id.Name != "_" {
			return id.Name, point.Assign.Tok, nil
		}
	}

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

func (i *Injector) signaturesMatch(sig *types.Signature, call *ast.CallExpr) bool {
	if i.Pkg.TypesInfo == nil {
		return false
	}
	callTV, ok := i.Pkg.TypesInfo.Types[call]
	if !ok {
		return false
	}

	callRes := callTV.Type
	sigRes := sig.Results()

	// Normalize call results to tuple
	var callTuple *types.Tuple
	if t, ok := callRes.(*types.Tuple); ok {
		callTuple = t
	} else {
		// Single return
		callTuple = types.NewTuple(types.NewVar(token.NoPos, nil, "", callRes))
	}

	if sigRes.Len() != callTuple.Len() {
		return false
	}

	for idx := 0; idx < sigRes.Len(); idx++ {
		if !types.Identical(sigRes.At(idx).Type(), callTuple.At(idx).Type()) {
			return false
		}
	}
	return true
}
