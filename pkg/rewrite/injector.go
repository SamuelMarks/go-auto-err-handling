package rewrite

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/analysis"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/astgen"
	"github.com/dave/dst"
	"github.com/dave/dst/dstutil"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// Injector handles the rewriting of ASTs/DSTs to inject error handling logic.
type Injector struct {
	// Fset is the token file set.
	Fset *token.FileSet
	// Pkg is the package information containing type info.
	Pkg *packages.Package
	// ErrorTemplate is the string template for generating return statements.
	ErrorTemplate string
	// MainHandlerStrategy defines how to handle errors in entry points (main/init).
	MainHandlerStrategy string
	// NonErrorFallback defines how to handle errors when signature cannot be changed.
	NonErrorFallback string
	// RetainPanics indicates if panic calls should be kept in specific scenarios.
	RetainPanics bool
	// TestParam is the name of the testing parameter (e.g. "t") for the current injection context.
	// If set, errors are handled via t.Fatal(err) instead of being returned or logged.
	TestParam string
}

// resolveErrorVarFunc is a test hook for overriding resolveErrorVar behavior.
var resolveErrorVarFunc = func(i *Injector, point analysis.InjectionPoint, scope *types.Scope) (string, token.Token, *dst.DeclStmt) {
	return i.resolveErrorVar(point, scope)
}

// generateAssignmentDSTFunc is a test hook for overriding generateAssignmentDST behavior.
var generateAssignmentDSTFunc = func(i *Injector, point analysis.InjectionPoint, call *dst.CallExpr, errName string, tok token.Token) (dst.Stmt, error) {
	return i.generateAssignmentDST(point, call, errName, tok)
}

// liftCompositeLitFunc is a test hook for overriding liftCompositeLit behavior.
var liftCompositeLitFunc = func(i *Injector, stmt dst.Stmt, entry targetEntry, astFile *ast.File, dstFile *dst.File) ([]dst.Stmt, error) {
	return i.liftCompositeLit(stmt, entry, astFile, dstFile)
}

// targetEntry holds pre-resolved DST nodes for an injection point.
type targetEntry struct {
	// point is the original analysis injection point.
	point analysis.InjectionPoint
	// dstStmt is the resolved DST statement.
	dstStmt dst.Stmt
	// dstCall is the resolved DST call expression (can be nil).
	dstCall *dst.CallExpr // Can be nil if call resolution failed or not needed
}

// NewInjector creates a new Injector for the given package.
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
		NonErrorFallback:    "log",
		RetainPanics:        retainPanics,
	}
}

// RewriteFile applies specific injection points to a single file using DST transformations.
//
// dstFile: The DST file to rewrite.
// astFile: The AST file corresponding to the DST file.
// points: The list of injection points to handle.
//
// Returns true if any changes were applied.
func (i *Injector) RewriteFile(dstFile *dst.File, astFile *ast.File, points []analysis.InjectionPoint) (bool, error) {
	// 0. Pre-resolve AST -> DST mappings BEFORE any mutation.
	targetMap := make(map[dst.Stmt][]targetEntry)
	for _, p := range points {
		if p.Stmt == nil {
			continue
		}
		// Skip Defers in generic rewrite loop (handled by RewriteDefers logic)
		if _, isDefer := p.Stmt.(*ast.DeferStmt); isDefer {
			continue
		}

		res, err := findDstNodeFunc(i.Fset, dstFile, astFile, p.Stmt)
		if err != nil {
			continue
		}
		dstStmt, ok := res.Node.(dst.Stmt)
		if !ok {
			continue
		}

		// Resolve the CallExpr within DST as well
		var dstCall *dst.CallExpr
		if p.Call != nil {
			callRes, err := findDstNodeFunc(i.Fset, dstFile, astFile, p.Call)
			if err == nil {
				dstCall, _ = callRes.Node.(*dst.CallExpr)
			}
		}

		targetMap[dstStmt] = append(targetMap[dstStmt], targetEntry{
			point:   p,
			dstStmt: dstStmt,
			dstCall: dstCall,
		})
	}

	// 1. Rewrite defers first (Mutation Phase 1)
	defersApplied, deferErr := i.RewriteDefers(dstFile, astFile)
	if deferErr != nil {
		return false, deferErr
	}

	if len(targetMap) == 0 {
		return defersApplied, nil
	}

	applied := false
	var err error

	// 2. Traverse and Apply (Mutation Phase 2)
	dstutil.Apply(dstFile, func(c *dstutil.Cursor) bool {
		if err != nil {
			return false
		}

		node := c.Node()
		stmt, isStmt := node.(dst.Stmt)
		if !isStmt {
			return true
		}

		// Check if we have entries for this statement
		entries, exists := targetMap[stmt]

		// --- Control Structure Initialization Lifting ---
		// Warning: Only supports single injection point per control structure for now.
		if exists && len(entries) == 1 {
			entry := entries[0]
			// Case: IfStmt
			if ifStmt, ok := stmt.(*dst.IfStmt); ok {
				handled, replacements, liftErr := i.tryLiftIf(ifStmt, entry, astFile, dstFile)
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
				handled, replacements, liftErr := i.tryLiftSwitch(swStmt, entry, astFile, dstFile)
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
				handled, replacements, liftErr := i.tryLiftTypeSwitch(typeSw, entry, astFile, dstFile)
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
		}
		// --- End Control Structure Lifting ---

		if !exists {
			return true
		}

		// Prepare for multiple mutations
		var preambles []dst.Stmt
		var standardRewriteEntry *targetEntry

		// Iterate over all failures in this statement
		for idx := range entries {
			// Pointer to avoid copy
			entry := &entries[idx]

			if entry.dstCall != nil {
				isEmbedded := i.isCallEmbeddedInComposite(stmt, entry.dstCall)
				if isEmbedded {
					// Lift: Generates preamble statements and mutates stmt IN-PLACE
					lifts, liftErr := liftCompositeLitFunc(i, stmt, *entry, astFile, dstFile)
					if liftErr != nil {
						err = liftErr
						return false
					}
					preambles = append(preambles, lifts...)
					applied = true
					continue
				}
			}

			standardRewriteEntry = entry
		}

		if standardRewriteEntry != nil {
			entry := *standardRewriteEntry
			isTail := false
			if block, ok := c.Parent().(*dst.BlockStmt); ok {
				if c.Index() == len(block.List)-1 {
					isTail = true
				}
			}

			var newNodes []dst.Stmt
			var genErr error

			switch s := stmt.(type) {
			case *dst.GoStmt:
				var converted *dst.GoStmt
				converted, genErr = i.generateGoRewriteDST(entry.point, s, entry.dstCall, dstFile)
				if converted != nil {
					newNodes = []dst.Stmt{converted}
				}
			default:
				// Fallback generic generation
				astCtx := i.getEnclosingContext(entry.point)
				supportsErrReturn := i.supportsErrorReturn(astCtx.sig, astCtx.decl)
				// Pass collapsed=true for standard statements and isTail check
				newNodes, genErr = i.generateRewriteDST(entry.point, stmt, entry.dstCall, astCtx.sig, astCtx.decl, true, isTail, dstFile)
				if genErr == nil && len(newNodes) == 0 && i.TestParam == "" && !supportsErrReturn {
					var needsLogImport bool
					newNodes, needsLogImport, genErr = i.generateNonErrorFallbackDST(entry.point, stmt, i.NonErrorFallback)
					if genErr == nil && len(newNodes) > 0 && needsLogImport {
						i.addImportDST(dstFile, "log")
					}
				}
			}

			if genErr != nil {
				err = genErr
				return false
			}

			if len(newNodes) > 0 {
				i.transferTrivia(stmt, newNodes)
				combined := append(preambles, newNodes...)
				i.replaceCursor(c, stmt, combined)
				applied = true
			}
		} else if len(preambles) > 0 {
			i.transferTrivia(stmt, preambles)
			combined := append(preambles, stmt)
			i.replaceCursor(c, stmt, combined)
			applied = true
		}

		return true
	}, nil)

	return applied || defersApplied, err
}

// isCallEmbeddedInComposite checks if the target call is inside a CompositeLiteral
func (i *Injector) isCallEmbeddedInComposite(stmt dst.Stmt, dstCall *dst.CallExpr) bool {
	if dstCall == nil {
		return false
	}
	found := false
	dst.Inspect(stmt, func(n dst.Node) bool {
		if found {
			return false
		}
		if lit, ok := n.(*dst.CompositeLit); ok {
			for _, elt := range lit.Elts {
				if elt == dstCall {
					found = true
					return false
				}
			}
		}
		if kv, ok := n.(*dst.KeyValueExpr); ok {
			if kv.Value == dstCall {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// liftCompositeLit extracts deeply nested calls from composite literals into temporary variables.
//
// stmt: The statement containing the literal.
// entry: The target entry with resolved DST nodes.
// astFile: The AST file context.
// dstFile: The DST file context.
//
// Returns a slice of preamble statements.
func (i *Injector) liftCompositeLit(stmt dst.Stmt, entry targetEntry, astFile *ast.File, dstFile *dst.File) ([]dst.Stmt, error) {
	dstCall := entry.dstCall
	point := entry.point

	if dstCall == nil {
		return nil, fmt.Errorf("dst call not resolved")
	}

	astCtx := i.getEnclosingContext(point)
	scope := i.getScope(point.Pos, point.File)

	baseName := "val"
	dst.Inspect(stmt, func(n dst.Node) bool {
		if kv, ok := n.(*dst.KeyValueExpr); ok {
			if kv.Value == dstCall {
				if key, ok := kv.Key.(*dst.Ident); ok {
					baseName = strings.ToLower(key.Name)
				}
				return false
			}
		}
		return true
	})

	tempVarName := analysis.GenerateUniqueName(scope, baseName)
	replacementIdent := dst.NewIdent(tempVarName)

	replaced := false
	dst.Inspect(stmt, func(n dst.Node) bool {
		if replaced {
			return false
		}
		if lit, ok := n.(*dst.CompositeLit); ok {
			for idx, elt := range lit.Elts {
				if elt == dstCall {
					lit.Elts[idx] = replacementIdent
					replaced = true
					return false
				}
			}
		}
		if kv, ok := n.(*dst.KeyValueExpr); ok {
			if kv.Value == dstCall {
				kv.Value = replacementIdent
				replaced = true
				return false
			}
		}
		return true
	})

	if !replaced {
		return nil, fmt.Errorf("failed to swap call in composite literal")
	}

	errName, tok, declStmt := resolveErrorVarFunc(i, point, scope)
	funcName := i.resolveFuncName(point)

	dstCallClone := dst.Clone(dstCall).(*dst.CallExpr)
	astgen.ClearDecorations(dstCallClone)

	assign := &dst.AssignStmt{
		Lhs: []dst.Expr{dst.NewIdent(tempVarName), dst.NewIdent(errName)},
		Tok: token.DEFINE,
		Rhs: []dst.Expr{dstCallClone},
	}

	if tok == token.ASSIGN {
		assign.Tok = token.DEFINE
	}

	var handlerBlock *dst.BlockStmt
	if i.TestParam != "" {
		handlerBlock = i.generateTerminalHandlerDST(errName, dstFile)
	} else {
		zeroExprs, _ := i.generateZeroReturns(point, astCtx.sig, astCtx.decl)
		retExprs, imports, _ := RenderTemplateDST(i.ErrorTemplate, zeroExprs, errName, funcName)
		handlerBlock = &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{Results: retExprs}}}
		for _, imp := range imports {
			i.addImportDST(dstFile, imp)
		}
	}

	check := &dst.IfStmt{
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
	result = append(result, assign, check)
	return result, nil
}

func (i *Injector) tryLiftIf(ifStmt *dst.IfStmt, entry targetEntry, astFile *ast.File, dstFile *dst.File) (bool, []dst.Stmt, error) {
	astIf, ok := entry.point.Stmt.(*ast.IfStmt)
	if !ok || entry.point.Call == nil {
		return false, nil, nil
	}

	if astIf.Init != nil && astNodeContainsCall(astIf.Init, entry.point.Call) {
		if assign, ok := astIf.Init.(*ast.AssignStmt); ok {
			entry.point.Assign = assign
		}
		res, err := i.liftControlInit(ifStmt, ifStmt.Init.(dst.Stmt), entry, astFile, dstFile)
		return true, res, err
	}

	if astIf.Cond != nil && astNodeContainsCall(astIf.Cond, entry.point.Call) {
		res, err := i.liftControlExpr(ifStmt, ifStmt.Cond, entry, "cond", astFile, dstFile, func(n dst.Node, e dst.Expr) {
			n.(*dst.IfStmt).Cond = e
		})
		return true, res, err
	}

	return false, nil, nil
}

func (i *Injector) tryLiftSwitch(swStmt *dst.SwitchStmt, entry targetEntry, astFile *ast.File, dstFile *dst.File) (bool, []dst.Stmt, error) {
	astSw, ok := entry.point.Stmt.(*ast.SwitchStmt)
	if !ok || entry.point.Call == nil {
		return false, nil, nil
	}

	if astSw.Init != nil && astNodeContainsCall(astSw.Init, entry.point.Call) {
		if assign, ok := astSw.Init.(*ast.AssignStmt); ok {
			entry.point.Assign = assign
		}
		res, err := i.liftControlInit(swStmt, swStmt.Init.(dst.Stmt), entry, astFile, dstFile)
		return true, res, err
	}

	if astSw.Tag != nil && astNodeContainsCall(astSw.Tag, entry.point.Call) {
		res, err := i.liftControlExpr(swStmt, swStmt.Tag, entry, "tag", astFile, dstFile, func(n dst.Node, e dst.Expr) {
			n.(*dst.SwitchStmt).Tag = e
		})
		return true, res, err
	}

	return false, nil, nil
}

func (i *Injector) tryLiftTypeSwitch(ts *dst.TypeSwitchStmt, entry targetEntry, astFile *ast.File, dstFile *dst.File) (bool, []dst.Stmt, error) {
	astTs, ok := entry.point.Stmt.(*ast.TypeSwitchStmt)
	if !ok || entry.point.Call == nil {
		return false, nil, nil
	}

	if astTs.Init != nil && astNodeContainsCall(astTs.Init, entry.point.Call) {
		if assign, ok := astTs.Init.(*ast.AssignStmt); ok {
			entry.point.Assign = assign
		}
		res, err := i.liftControlInit(ts, ts.Init.(dst.Stmt), entry, astFile, dstFile)
		return true, res, err
	}

	if astTs.Assign != nil && astNodeContainsCall(astTs.Assign, entry.point.Call) {
		res, err := i.liftTypeSwitchAssign(ts, entry, astFile, dstFile)
		return true, res, err
	}

	return false, nil, nil
}

// LogFallback applies a fallback logging statement for unhandled errors.
//
// dstFile: The DST file.
// astFile: The AST file.
// point: The specific injection point.
//
// Returns true if changes were applied.
func (i *Injector) LogFallback(dstFile *dst.File, astFile *ast.File, point analysis.InjectionPoint) (bool, error) {
	if point.Stmt == nil {
		return false, nil
	}
	res, err := findDstNodeFunc(i.Fset, dstFile, astFile, point.Stmt)
	if err != nil {
		return false, err
	}
	dstStmt, ok := res.Node.(dst.Stmt)
	if !ok {
		return false, nil
	}

	applied := false
	var genErr error
	var needsLogImport bool

	dstutil.Apply(dstFile, func(c *dstutil.Cursor) bool {
		if c.Node() != dstStmt {
			return true
		}

		var stmts []dst.Stmt
		stmts, needsLogImport, genErr = i.generateNonErrorFallbackDST(point, dstStmt, i.NonErrorFallback)
		if genErr != nil {
			return false
		}

		if len(stmts) > 0 {
			i.transferTrivia(dstStmt, stmts)
			c.Replace(stmts[0])
			for k := len(stmts) - 1; k > 0; k-- {
				c.InsertAfter(stmts[k])
			}
			if needsLogImport {
				i.addImportDST(dstFile, "log")
			}
			applied = true
		}
		return false
	}, nil)

	return applied, genErr
}

func (i *Injector) liftControlInit(controlStmt dst.Node, initStmt dst.Stmt, entry targetEntry, astFile *ast.File, dstFile *dst.File) ([]dst.Stmt, error) {
	dstCall := entry.dstCall
	if dstCall == nil {
		return nil, fmt.Errorf("failed to map call in init")
	}

	astCtx := i.getEnclosingContext(entry.point)
	preamble, err := i.generateRewriteDST(entry.point, initStmt, dstCall, astCtx.sig, astCtx.decl, false, false, dstFile)
	if err != nil {
		return nil, err
	}

	clonedControl := dst.Clone(controlStmt).(dst.Stmt)
	switch s := clonedControl.(type) {
	case *dst.IfStmt:
		s.Init = nil
	case *dst.SwitchStmt:
		s.Init = nil
	case *dst.TypeSwitchStmt:
		s.Init = nil
	}

	block := &dst.BlockStmt{
		List: append(preamble, clonedControl),
	}

	src := controlStmt.(dst.Stmt)
	block.Decorations().Before = src.Decorations().Before
	block.Decorations().Start = src.Decorations().Start
	block.Decorations().End = src.Decorations().End
	block.Decorations().After = src.Decorations().After

	return []dst.Stmt{block}, nil
}

func (i *Injector) liftControlExpr(controlStmt dst.Node, expr dst.Expr, entry targetEntry, varNameHint string, astFile *ast.File, dstFile *dst.File, setter func(dst.Node, dst.Expr)) ([]dst.Stmt, error) {
	astCtx := i.getEnclosingContext(entry.point)
	scope := i.getScope(entry.point.Pos, entry.point.File)
	errName, _, _ := resolveErrorVarFunc(i, entry.point, scope)

	dstCall := entry.dstCall
	if dstCall == nil {
		return nil, fmt.Errorf("failed to map call in cond")
	}

	isSingleError := true
	if i.Pkg.TypesInfo != nil {
		if tv, ok := i.Pkg.TypesInfo.Types[entry.point.Call]; ok {
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
			Body: i.generateTerminalHandlerDST(errName, dstFile),
		}
	} else {
		zeroExprs, err := i.generateZeroReturns(entry.point, astCtx.sig, astCtx.decl)
		if err != nil {
			return nil, err
		}
		retExprs, imports, err := RenderTemplateDST(i.ErrorTemplate, zeroExprs, errName, i.resolveFuncName(entry.point))
		if err != nil {
			return nil, err
		}
		checkStmt = &dst.IfStmt{
			Cond: &dst.BinaryExpr{X: dst.NewIdent(errName), Op: token.NEQ, Y: dst.NewIdent("nil")},
			Body: &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{Results: retExprs}}},
		}
		for _, imp := range imports {
			i.addImportDST(dstFile, imp)
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

func (i *Injector) liftTypeSwitchAssign(ts *dst.TypeSwitchStmt, entry targetEntry, astFile *ast.File, dstFile *dst.File) ([]dst.Stmt, error) {
	dstCall := entry.dstCall
	if dstCall == nil {
		return nil, fmt.Errorf("failed to map call in type switch")
	}

	astCtx := i.getEnclosingContext(entry.point)
	scope := i.getScope(entry.point.Pos, entry.point.File)
	errName, _, _ := resolveErrorVarFunc(i, entry.point, scope)
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
			Body: i.generateTerminalHandlerDST(errName, dstFile),
		}
	} else {
		zeroExprs, _ := i.generateZeroReturns(entry.point, astCtx.sig, astCtx.decl)
		retExprs, imports, _ := RenderTemplateDST(i.ErrorTemplate, zeroExprs, errName, i.resolveFuncName(entry.point))
		checkStmt = &dst.IfStmt{
			Cond: &dst.BinaryExpr{X: dst.NewIdent(errName), Op: token.NEQ, Y: dst.NewIdent("nil")},
			Body: &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{Results: retExprs}}},
		}
		for _, imp := range imports {
			i.addImportDST(dstFile, imp)
		}
	}

	clonedTs := dst.Clone(ts).(*dst.TypeSwitchStmt)

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

func (i *Injector) generateRewriteDST(point analysis.InjectionPoint, dstStmt dst.Stmt, dstCall *dst.CallExpr, sig *types.Signature, decl *ast.FuncDecl, collapsed bool, isTail bool, dstFile *dst.File) ([]dst.Stmt, error) {
	useSig := i.supportsErrorReturn(sig, decl)

	if !useSig && i.TestParam == "" {
		return nil, nil
	}

	if dstCall == nil {
		dstCall = i.extractDstCall(dstStmt)
	}
	if dstCall == nil {
		return nil, fmt.Errorf("could not locate call in dst statement")
	}
	dstCallClone := dst.Clone(dstCall).(*dst.CallExpr)
	astgen.ClearDecorations(dstCallClone)

	if isTail && i.TestParam == "" && sig != nil && point.Call != nil && i.signaturesMatch(sig, point.Call) {
		if !i.isInsideLoop(point) {
			return []dst.Stmt{
				&dst.ReturnStmt{
					Results: []dst.Expr{dstCallClone},
				},
			}, nil
		}
	}

	scope := i.getScope(point.Pos, point.File)
	errName, tok, declStmt := resolveErrorVarFunc(i, point, scope)
	funcName := i.resolveFuncName(point)

	assignStmt, err := generateAssignmentDSTFunc(i, point, dstCallClone, errName, tok)
	if err != nil {
		return nil, err
	}

	var handlerBlock *dst.BlockStmt

	if i.TestParam != "" {
		handlerBlock = i.generateTerminalHandlerDST(errName, dstFile)
	} else {
		zeroExprs, err := i.generateZeroReturns(point, sig, decl)
		if err != nil {
			return nil, err
		}

		retExprs, imports, err := RenderTemplateDST(i.ErrorTemplate, zeroExprs, errName, funcName)
		if err != nil {
			return nil, err
		}
		retStmt := &dst.ReturnStmt{Results: retExprs}
		handlerBlock = &dst.BlockStmt{
			List: []dst.Stmt{retStmt},
		}
		for _, imp := range imports {
			i.addImportDST(dstFile, imp)
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

func (i *Injector) generateGoRewriteDST(point analysis.InjectionPoint, goStmt *dst.GoStmt, dstCall *dst.CallExpr, dstFile *dst.File) (*dst.GoStmt, error) {
	if dstCall == nil {
		dstCall = dst.Clone(goStmt.Call).(*dst.CallExpr)
	} else {
		dstCall = dst.Clone(dstCall).(*dst.CallExpr)
	}
	astgen.ClearDecorations(dstCall)

	errName := "err"
	tok := token.DEFINE

	assignStmt, err := generateAssignmentDSTFunc(i, point, dstCall, errName, tok)
	if err != nil {
		return nil, err
	}

	handlerBlock := i.generateTerminalHandlerDST(errName, dstFile)

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

func (i *Injector) generateZeroReturns(point analysis.InjectionPoint, sig *types.Signature, decl *ast.FuncDecl) ([]dst.Expr, error) {
	var zeroExprs []dst.Expr
	if sig != nil {
		limit := sig.Results().Len()
		if i.isErrorType(sig.Results().At(sig.Results().Len() - 1).Type()) {
			limit--
		}

		scope := i.getScope(point.Pos, point.File)
		ctx := astgen.ZeroCtx{
			IsNameSafe: func(name string, expected types.Object) bool {
				if scope == nil {
					return true
				}
				_, obj := scope.LookupParent(name, point.Pos)
				if obj == nil {
					return true
				}
				return obj == expected
			},
		}

		for idx := 0; idx < limit; idx++ {
			t := sig.Results().At(idx).Type()
			z, err := astgen.ZeroExprDST(t, ctx)
			if err != nil {
				return nil, err
			}
			zeroExprs = append(zeroExprs, z)
		}
	}
	return zeroExprs, nil
}

func (i *Injector) generateNonErrorFallbackDST(point analysis.InjectionPoint, dstStmt dst.Stmt, strategy string) ([]dst.Stmt, bool, error) {
	strategy = i.normalizeNonErrorFallback(strategy)
	scope := i.getScope(point.Pos, point.File)
	errName, tok, declStmt := resolveErrorVarFunc(i, point, scope)
	funcName := i.resolveFuncName(point)

	dstCall := i.extractDstCall(dstStmt)
	if dstCall == nil {
		return nil, false, fmt.Errorf("no call in stmt")
	}
	dstCallClone := dst.Clone(dstCall).(*dst.CallExpr)
	astgen.ClearDecorations(dstCallClone)

	assignStmt, err := generateAssignmentDSTFunc(i, point, dstCallClone, errName, tok)
	if err != nil {
		return nil, false, err
	}

	var handlerStmt dst.Stmt
	needsLogImport := false

	switch strategy {
	case "panic":
		handlerStmt = &dst.ExprStmt{
			X: &dst.CallExpr{
				Fun:  dst.NewIdent("panic"),
				Args: []dst.Expr{dst.NewIdent(errName)},
			},
		}
	case "fatal":
		needsLogImport = true
		handlerStmt = &dst.ExprStmt{
			X: &dst.CallExpr{
				Fun: &dst.SelectorExpr{
					X:   dst.NewIdent("log"),
					Sel: dst.NewIdent("Fatalf"),
				},
				Args: []dst.Expr{
					&dst.BasicLit{Kind: token.STRING, Value: fmt.Sprintf(`"unhandled error in %s: %%v"`, funcName)},
					dst.NewIdent(errName),
				},
			},
		}
	default: // log
		needsLogImport = true
		handlerStmt = &dst.ExprStmt{
			X: &dst.CallExpr{
				Fun: &dst.SelectorExpr{
					X:   dst.NewIdent("log"),
					Sel: dst.NewIdent("Printf"),
				},
				Args: []dst.Expr{
					&dst.BasicLit{Kind: token.STRING, Value: fmt.Sprintf(`"ignored error in %s: %%v"`, funcName)},
					dst.NewIdent(errName),
				},
			},
		}
	}

	checkStmt := &dst.IfStmt{
		Cond: &dst.BinaryExpr{
			X:  dst.NewIdent(errName),
			Op: token.NEQ,
			Y:  dst.NewIdent("nil"),
		},
		Body: &dst.BlockStmt{
			List: []dst.Stmt{
				handlerStmt,
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

	return result, needsLogImport, nil
}

func (i *Injector) generateLogRewriteDST(point analysis.InjectionPoint, dstStmt dst.Stmt) ([]dst.Stmt, error) {
	stmts, _, err := i.generateNonErrorFallbackDST(point, dstStmt, "log")
	return stmts, err
}

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

func (i *Injector) generateTerminalHandlerDST(errVar string, dstFile *dst.File) *dst.BlockStmt {
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
		i.addImportDST(dstFile, "fmt")
		i.addImportDST(dstFile, "os")
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
		i.addImportDST(dstFile, "log")
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
	if point.Assign != nil && len(point.Assign.Lhs) > 0 {
		lastIdx := len(point.Assign.Lhs) - 1
		if id, ok := point.Assign.Lhs[lastIdx].(*ast.Ident); ok && id.Name != "_" {
			return id.Name, point.Assign.Tok, nil
		}
	}

	targetName := "err"
	if scope == nil {
		return targetName, token.DEFINE, nil
	}

	_, obj := scope.LookupParent(targetName, point.Pos)
	if obj == nil {
		return targetName, token.DEFINE, nil
	}

	if i.isGlobal(obj) {
		return targetName, token.DEFINE, nil
	}
	if i.isInScope(obj, scope) {
		return targetName, token.ASSIGN, nil
	}
	if i.isVarUsedAfter(obj, point.Pos) {
		return targetName, token.ASSIGN, nil
	}
	return targetName, token.DEFINE, nil
}

func (i *Injector) isInScope(obj types.Object, scope *types.Scope) bool {
	return obj.Parent() == scope
}

func (i *Injector) isGlobal(obj types.Object) bool {
	if i.Pkg != nil && i.Pkg.Types != nil {
		return obj.Parent() == i.Pkg.Types.Scope()
	}
	if obj.Parent() != nil && obj.Parent().Parent() == types.Universe {
		return true
	}
	return false
}

func (i *Injector) isVarUsedAfter(obj types.Object, pos token.Pos) bool {
	if i.Pkg == nil || i.Pkg.TypesInfo == nil {
		return false
	}
	for id, usedObj := range i.Pkg.TypesInfo.Uses {
		if usedObj == obj {
			if id.Pos() > pos {
				return true
			}
		}
	}
	return false
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

func (i *Injector) supportsErrorReturn(sig *types.Signature, decl *ast.FuncDecl) bool {
	if sig != nil && sig.Results().Len() > 0 {
		last := sig.Results().At(sig.Results().Len() - 1)
		if i.isErrorType(last.Type()) {
			return true
		}
	} else if decl != nil && decl.Type.Results != nil {
		list := decl.Type.Results.List
		if len(list) > 0 {
			if i.isErrorExpr(list[len(list)-1].Type) {
				return true
			}
		}
	}
	return false
}

func (i *Injector) normalizeNonErrorFallback(strategy string) string {
	s := strings.ToLower(strings.TrimSpace(strategy))
	switch s {
	case "", "log":
		return "log"
	case "fatal", "log-fatal":
		return "fatal"
	case "panic":
		return "panic"
	default:
		return "log"
	}
}

func astNodeContainsCall(root ast.Node, call *ast.CallExpr) bool {
	if root == nil || call == nil {
		return false
	}
	found := false
	ast.Inspect(root, func(n ast.Node) bool {
		if n == call {
			found = true
			return false
		}
		return true
	})
	return found
}

func (i *Injector) addImportDST(file *dst.File, path string) {
	pathStr := fmt.Sprintf(`"%s"`, path)
	for _, imp := range file.Imports {
		if imp.Path != nil && imp.Path.Value == pathStr {
			return
		}
	}
	decl := &dst.GenDecl{
		Tok: token.IMPORT,
		Specs: []dst.Spec{
			&dst.ImportSpec{
				Path: &dst.BasicLit{Kind: token.STRING, Value: pathStr},
			},
		},
	}
	file.Decls = append([]dst.Decl{decl}, file.Decls...)
	file.Imports = append(file.Imports, decl.Specs[0].(*dst.ImportSpec))
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

	var callTuple *types.Tuple
	if t, ok := callRes.(*types.Tuple); ok {
		callTuple = t
	} else {
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

func (i *Injector) isInsideLoop(point analysis.InjectionPoint) bool {
	path, _ := astutil.PathEnclosingInterval(point.File, point.Pos, point.Pos)
	for _, n := range path {
		if _, ok := n.(*ast.FuncDecl); ok {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if _, ok := n.(*ast.ForStmt); ok {
			return true
		}
		if _, ok := n.(*ast.RangeStmt); ok {
			return true
		}
	}
	return false
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
