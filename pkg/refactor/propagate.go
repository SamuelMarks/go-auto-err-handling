package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"reflect"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/astgen"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/filter"
	"github.com/dave/dst"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// MainHandlerStrategy defines how errors should be handled in entry points like main or init.
type MainHandlerStrategy string

const (
	// HandlerLogFatal uses log.Fatal(err).
	HandlerLogFatal MainHandlerStrategy = "log-fatal"
	// HandlerOsExit uses fmt.Println(err) followed by os.Exit(1).
	HandlerOsExit MainHandlerStrategy = "os-exit"
	// HandlerPanic uses panic(err).
	HandlerPanic MainHandlerStrategy = "panic"
)

// DstProvider abstracts the management of DST files.
type DstProvider interface {
	Get(pkg *packages.Package, file *ast.File) (*dst.File, error)
	MarkModified(file *ast.File)
}

var (
	addErrorToSignatureDSTFn = AddErrorToSignatureDST
	patchSignatureFn         = PatchSignature
	patchVarTypeFn           = PatchVarType
	ensureNamedReturnsDSTFn  = EnsureNamedReturnsDST
	determineTraversalStepFn = determineTraversalStep
)

// PropagateCallers updates all call sites (and value assignments) of a modified object.
func PropagateCallers(pkgs []*packages.Package, provider DstProvider, initialTarget types.Object, strategy string) (int, error) {
	if initialTarget == nil {
		return 0, fmt.Errorf("target object is nil")
	}

	queue := []types.Object{initialTarget}
	visited := make(map[types.Object]bool)
	visited[initialTarget] = true

	totalUpdates := 0

	for len(queue) > 0 {
		target := queue[0]
		queue = queue[1:]

		for _, pkg := range pkgs {
			var uses []*ast.Ident
			for id, obj := range pkg.TypesInfo.Uses {
				if obj == target {
					uses = append(uses, id)
				}
			}

			// Sort? Order usually doesn't matter for independent AST nodes, but stable is nice.
			// Iteration order of map is random. Given complexity, we process as is.

			for _, id := range uses {
				file := findFile(pkg, id.Pos())
				if file == nil {
					continue
				}

				dstFile, err := provider.Get(pkg, file)
				if err != nil {
					return totalUpdates, fmt.Errorf("failed to get DST: %w", err)
				}

				path, _ := astutil.PathEnclosingInterval(file, id.Pos(), id.Pos())
				isCall := false
				var callExpr *ast.CallExpr

				for _, node := range path {
					if c, ok := node.(*ast.CallExpr); ok {
						if isIdentFunctionInCall(c, id) {
							isCall = true
							callExpr = c
							break
						}
					}
				}

				var nextObj types.Object
				var updated int

				if isCall {
					updated, nextObj, err = processCallSiteDST(pkg, file, dstFile, id, target, strategy)
					// Manually patch Types Info for call if updated so subsequent checks on this call see the tuple return
					if updated > 0 && callExpr != nil {
						if sig := getSignature(target); sig != nil {
							pkg.TypesInfo.Types[callExpr] = types.TypeAndValue{Type: sig, Value: nil}
						}
					}
				} else {
					updated, nextObj, err = processVarPropagationDST(pkg, provider, file, dstFile, id, target)
				}

				if err != nil {
					return totalUpdates, err
				}

				if updated > 0 {
					totalUpdates++
					provider.MarkModified(file)
				}

				if nextObj != nil {
					if !visited[nextObj] {
						visited[nextObj] = true
						queue = append(queue, nextObj)
					}
				}
			}
		}
	}

	return totalUpdates, nil
}

func processVarPropagationDST(pkg *packages.Package, provider DstProvider, astFile *ast.File, dstFile *dst.File, id *ast.Ident, target types.Object) (int, types.Object, error) {
	path, _ := astutil.PathEnclosingInterval(astFile, id.Pos(), id.Pos())

	var definedVar types.Object
	var definingIdent *ast.Ident
	var explicitTypeNode ast.Node

	// Check if this use maps to an assignment/definition of another variable
	// e.g. "var f = target" (ValueSpec) or "f := target" (AssignStmt)

	for _, node := range path {
		if vs, ok := node.(*ast.ValueSpec); ok {
			for i, val := range vs.Values {
				if containsNode(val, id) {
					if i < len(vs.Names) {
						if def := pkg.TypesInfo.ObjectOf(vs.Names[i]); def != nil {
							definedVar = def
							definingIdent = vs.Names[i]
							if vs.Type != nil {
								explicitTypeNode = vs.Type
							}
						}
					}
				}
			}
			break
		}
		if as, ok := node.(*ast.AssignStmt); ok {
			for i, rhs := range as.Rhs {
				if containsNode(rhs, id) {
					if i < len(as.Lhs) {
						// Need to handle if LHS is Ident
						if lhsId, ok := as.Lhs[i].(*ast.Ident); ok {
							if def := pkg.TypesInfo.ObjectOf(lhsId); def != nil {
								definedVar = def
								definingIdent = lhsId
							}
						}
					}
				}
			}
			break
		}
	}

	if definedVar == nil {
		return 0, nil, nil
	}

	targetSig := getSignature(target)
	if targetSig == nil {
		return 0, nil, fmt.Errorf("could not resolve signature for propagated target")
	}

	// 1. Rewrite explicit type if present.
	// This requires mapping to the *definition* file, which might be different from the *assignment* file.
	defFile := findFile(pkg, definedVar.Pos())
	if defFile != nil {
		// Get DST for definition file
		defDstFile, err := provider.Get(pkg, defFile)
		if err != nil {
			return 0, nil, err
		}

		// Locate type node within definition file if we identified it via ValueSpec
		// Note: explicitlyTypeNode collected above is from the *current* path traversal.
		// If definition is in current file (likely for ValueSpec/ShortAssign usage), we can use it.
		// If used in AssignStmt (=) where declared elsewhere, explicitTypeNode is nil here.
		if explicitTypeNode != nil && defFile == astFile {
			// Local modification
			dstTypeNode, _ := mapAstToDst(astFile, dstFile, explicitTypeNode)
			if dstFuncType, ok := dstTypeNode.(*dst.FuncType); ok {
				if !hasTrailingErrorReturnDST(dstFuncType) {
					changed, _ := AddErrorToFuncTypeDST(dstFuncType)
					if changed {
						provider.MarkModified(defFile)
					}
				}
			}
		} else if defFile != astFile {
			// Variable defined elsewhere (e.g. 'var f func()' in other file).
			// We need to look up the definition AST node.
			defIdent := findIdentForObj(defFile, definedVar)
			if defIdent != nil {
				// Find Decl
				declPath, _ := astutil.PathEnclosingInterval(defFile, defIdent.Pos(), defIdent.Pos())
				for _, n := range declPath {
					if vs, ok := n.(*ast.ValueSpec); ok && vs.Type != nil {
						// Map to proper DST
						dstTypeNode, _ := mapAstToDst(defFile, defDstFile, vs.Type)
						if dstFuncType, ok := dstTypeNode.(*dst.FuncType); ok {
							if !hasTrailingErrorReturnDST(dstFuncType) {
								changed, _ := AddErrorToFuncTypeDST(dstFuncType)
								if changed {
									provider.MarkModified(defFile)
								}
							}
						}
						break
					}
				}
			}
		}
	}

	// 2. Patch type object in go/types
	// Only if the current type differs from target type
	varSig := getSignature(definedVar)
	if !signaturesEquivalent(varSig, targetSig) {
		var newObj types.Object
		// Prefer updating the definition ident if it lives in a different file.
		if defFile != nil && defFile != astFile {
			if varIdent := findIdentForObj(defFile, definedVar); varIdent != nil {
				var err error
				newObj, err = patchVarTypeFn(pkg.TypesInfo, varIdent, targetSig)
				if err != nil {
					return 0, nil, err
				}
			}
		}
		if newObj == nil && definingIdent != nil {
			var err error
			newObj, err = patchVarTypeFn(pkg.TypesInfo, definingIdent, targetSig)
			if err != nil {
				return 0, nil, err
			}
		}

		if newObj != nil {
			return 1, newObj, nil
		}
	}

	return 0, nil, nil
}

func containsNode(root, target ast.Node) bool {
	found := false
	ast.Inspect(root, func(n ast.Node) bool {
		if n == target {
			found = true
			return false
		}
		return true
	})
	return found
}

func hasTrailingErrorReturnDST(fn *dst.FuncType) bool {
	if fn.Results == nil || len(fn.Results.List) == 0 {
		return false
	}
	lastField := fn.Results.List[len(fn.Results.List)-1]
	if id, ok := lastField.Type.(*dst.Ident); ok {
		return id.Name == "error"
	}
	return false
}

func processCallSiteDST(pkg *packages.Package, astFile *ast.File, dstFile *dst.File, id *ast.Ident, target types.Object, strategy string) (int, *types.Func, error) {
	if astFile == nil || dstFile == nil || id == nil {
		return 0, nil, fmt.Errorf("nil args")
	}

	path, _ := astutil.PathEnclosingInterval(astFile, id.Pos(), id.Pos())
	var call *ast.CallExpr
	var enclosingStmt ast.Stmt

	for _, node := range path {
		if c, ok := node.(*ast.CallExpr); ok && call == nil {
			if isIdentFunctionInCall(c, id) {
				call = c
			}
		}
		if stmt, ok := node.(ast.Stmt); ok && call != nil {
			enclosingStmt = stmt
			break
		}
	}

	if call == nil || enclosingStmt == nil {
		return 0, nil, nil
	}

	dstStmt, dstParent := mapAstToDst(astFile, dstFile, enclosingStmt)
	if dstStmt == nil {
		return 0, nil, fmt.Errorf("failed to map stmt")
	}

	sig, funcObj, decl := findEnclosingFuncDetails(path, pkg.TypesInfo)

	isTerminal := false
	testParam := ""

	if funcObj != nil {
		isTerminal = IsEntryPoint(funcObj)
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

	var nextTarget *types.Func
	var dstDecl *dst.FuncDecl

	// if this function isn't terminal and returns void, update its signature
	if decl != nil {
		res, _ := mapAstToDst(astFile, dstFile, decl)
		if f, ok := res.(*dst.FuncDecl); ok {
			dstDecl = f
		}
	}

	if !isTerminal && decl != nil && funcObj != nil {
		canReturn := canReturnError(sig)
		if !canReturn {
			if dstDecl != nil {
				changed, err := addErrorToSignatureDSTFn(dstDecl)
				if err != nil {
					return 0, nil, err
				}
				if changed {
					// Update AST signature object
					if err := patchSignatureFn(pkg.TypesInfo, decl, pkg.Types); err != nil {
						return 0, nil, err
					}
					// Update local context
					obj := pkg.TypesInfo.ObjectOf(decl.Name)
					if fn, ok := obj.(*types.Func); ok {
						funcObj = fn
						sig = fn.Type().(*types.Signature)
						nextTarget = fn
					}
				}
			}
		}
	}

	targetSig := getSignature(target)

	context := rewriteContext{
		dstFile:        dstFile,
		stmt:           dstStmt,
		parent:         dstParent,
		enclosingSig:   sig,
		enclosingFnDst: dstDecl,
		isTerminal:     isTerminal,
		strategy:       MainHandlerStrategy(strategy),
		testParam:      testParam,
		targetSig:      targetSig,
		scope:          getScope(pkg, enclosingStmt),
		pos:            enclosingStmt.Pos(),
	}

	if err := refactorCallSiteDST(context); err != nil {
		return 0, nil, err
	}

	return 1, nextTarget, nil
}

// HandleEntryPoint handles main/init errors.
func HandleEntryPoint(pkg *packages.Package, dstFile *dst.File, call *ast.CallExpr, stmt ast.Stmt, strategy string) error {
	astFile := findFile(pkg, stmt.Pos())
	if astFile == nil {
		return fmt.Errorf("could not locate AST file")
	}
	dstStmt, dstParent := mapAstToDst(astFile, dstFile, stmt)
	if dstStmt == nil {
		return fmt.Errorf("failed to map stmt")
	}
	ctx := rewriteContext{
		dstFile:    dstFile,
		stmt:       dstStmt,
		parent:     dstParent,
		isTerminal: true,
		strategy:   MainHandlerStrategy(strategy),
		scope:      getScope(pkg, stmt),
		pos:        stmt.Pos(),
	}
	return refactorCallSiteDST(ctx)
}

// HandleTestError handles errors in tests.
func HandleTestError(pkg *packages.Package, dstFile *dst.File, call *ast.CallExpr, stmt ast.Stmt, testParamName string) error {
	astFile := findFile(pkg, stmt.Pos())
	if astFile == nil {
		return fmt.Errorf("could not locate AST file")
	}
	dstStmt, dstParent := mapAstToDst(astFile, dstFile, stmt)
	if dstStmt == nil {
		return fmt.Errorf("failed to map stmt")
	}
	ctx := rewriteContext{
		dstFile:    dstFile,
		stmt:       dstStmt,
		parent:     dstParent,
		isTerminal: true,
		testParam:  testParamName,
		scope:      getScope(pkg, stmt),
		pos:        stmt.Pos(),
	}
	return refactorCallSiteDST(ctx)
}

type rewriteContext struct {
	dstFile        *dst.File
	stmt           dst.Node
	parent         dst.Node
	enclosingSig   *types.Signature
	enclosingFnDst *dst.FuncDecl
	isTerminal     bool
	strategy       MainHandlerStrategy
	testParam      string
	targetSig      *types.Signature
	scope          *types.Scope
	pos            token.Pos
}

func (r *rewriteContext) enclosing() *types.Signature { return r.enclosingSig }

func refactorCallSiteDST(ctx rewriteContext) error {
	stmt := ctx.stmt.(dst.Stmt)
	switch s := stmt.(type) {
	case *dst.ExprStmt:
		call := s.X
		// PASSTHROUGH OPTIMIZATION
		// If usage is tail call and signatures match, just return
		isTail := false
		if block, ok := ctx.parent.(*dst.BlockStmt); ok {
			for i, st := range block.List {
				if st == stmt {
					if i == len(block.List)-1 {
						isTail = true
					}
					break
				}
			}
		}
		if isTail && !ctx.isTerminal && ctx.enclosingSig != nil && ctx.targetSig != nil {
			if types.Identical(ctx.enclosingSig.Results(), ctx.targetSig.Results()) {
				// We can return directly
				ret := &dst.ReturnStmt{
					Results: []dst.Expr{dst.Clone(call).(dst.Expr)},
				}
				replaceInParent(ctx.parent, stmt, ret)
				return nil
			}
		}

		block := generateCheckBlock(call, ctx)
		replaceInParent(ctx.parent, stmt, block)
	case *dst.AssignStmt:
		// RHS call returns error now. Append err to LHS.
		s.Lhs = append(s.Lhs, dst.NewIdent("err"))
		check := generateBasicCheck(ctx)
		insertAfterInParent(ctx.parent, stmt, check)
	case *dst.GoStmt:
		return refactorGoStmt(ctx, s)
	case *dst.DeferStmt:
		return refactorDeferStmt(ctx, s)
	default:
		return fmt.Errorf("unsupported stmt type for refactor: %T", stmt)
	}
	return nil
}

func refactorGoStmt(ctx rewriteContext, gs *dst.GoStmt) error {
	// Wrap in closure: go func() { if err := call(); err != nil ... }()
	call := dst.Clone(gs.Call).(*dst.CallExpr)
	subCtx := ctx
	subCtx.isTerminal = true // Go routines always terminal handling
	// Don't pass testing params into goroutines (safety)
	if subCtx.testParam != "" {
		subCtx.testParam = ""
	}

	checkStmt := generateCheckBlock(call, subCtx)
	fnLit := &dst.FuncLit{
		Type: &dst.FuncType{
			Params:  &dst.FieldList{},
			Results: nil,
		},
		Body: &dst.BlockStmt{
			List: []dst.Stmt{checkStmt},
		},
	}
	newGo := &dst.GoStmt{
		Call: &dst.CallExpr{
			Fun: fnLit,
		},
	}
	replaceInParent(ctx.parent, gs, newGo)
	return nil
}

func refactorDeferStmt(ctx rewriteContext, ds *dst.DeferStmt) error {
	call := dst.Clone(ds.Call).(*dst.CallExpr)
	parentCanReturnErr := canReturnError(ctx.enclosing())

	var body *dst.BlockStmt

	// If parent returns error, we can try to join
	if parentCanReturnErr && ctx.enclosingFnDst != nil {
		_, err := ensureNamedReturnsDSTFn(ctx.enclosingFnDst)
		if err != nil {
			return err
		}
		errVar := getErrorReturnNameDST(ctx.enclosingFnDst.Type)
		if errVar == "" {
			return refactorDeferLogFallback(ctx, call, ds)
		}

		ensureImportDST(ctx.dstFile, "errors")
		// errors.Join(errVar, call())
		assign := &dst.AssignStmt{
			Lhs: []dst.Expr{dst.NewIdent(errVar)},
			Tok: token.ASSIGN,
			Rhs: []dst.Expr{
				&dst.CallExpr{
					Fun: &dst.SelectorExpr{
						X:   dst.NewIdent("errors"),
						Sel: dst.NewIdent("Join"),
					},
					Args: []dst.Expr{
						dst.NewIdent(errVar),
						call,
					},
				},
			},
		}
		body = &dst.BlockStmt{List: []dst.Stmt{assign}}
	} else {
		// Log fallback
		return refactorDeferLogFallback(ctx, call, ds)
	}

	fnLit := &dst.FuncLit{
		Type: &dst.FuncType{Params: &dst.FieldList{}},
		Body: body,
	}
	newDefer := &dst.DeferStmt{Call: &dst.CallExpr{Fun: fnLit}}
	replaceInParent(ctx.parent, ds, newDefer)
	return nil
}

func refactorDeferLogFallback(ctx rewriteContext, call *dst.CallExpr, ds *dst.DeferStmt) error {
	ensureImportDST(ctx.dstFile, "log")
	// if err := call(); err != nil { log.Printf(...) }
	assign := &dst.AssignStmt{
		Lhs: []dst.Expr{dst.NewIdent("err")},
		Tok: token.DEFINE,
		Rhs: []dst.Expr{call},
	}
	cond := &dst.BinaryExpr{
		X:  dst.NewIdent("err"),
		Op: token.NEQ,
		Y:  dst.NewIdent("nil"),
	}
	logCall := &dst.CallExpr{
		Fun: &dst.SelectorExpr{X: dst.NewIdent("log"), Sel: dst.NewIdent("Printf")},
		Args: []dst.Expr{
			&dst.BasicLit{Kind: token.STRING, Value: `"deferred error: %v"`},
			dst.NewIdent("err"),
		},
	}
	ifStmt := &dst.IfStmt{
		Init: assign,
		Cond: cond,
		Body: &dst.BlockStmt{List: []dst.Stmt{&dst.ExprStmt{X: logCall}}},
	}
	fnLit := &dst.FuncLit{
		Type: &dst.FuncType{Params: &dst.FieldList{}},
		Body: &dst.BlockStmt{List: []dst.Stmt{ifStmt}},
	}
	newDefer := &dst.DeferStmt{Call: &dst.CallExpr{Fun: fnLit}}
	replaceInParent(ctx.parent, ds, newDefer)
	return nil
}

func replaceInParent(parent, oldNode, newNode dst.Node) {
	if b, ok := parent.(*dst.BlockStmt); ok {
		for i, stmt := range b.List {
			if stmt == oldNode {
				b.List[i] = newNode.(dst.Stmt)
				return
			}
		}
	}
	// Note: Switch/If/etc parents handling omitted for brevity, logic usually inside Blocks
}

func insertAfterInParent(parent, target, toInsert dst.Node) {
	if b, ok := parent.(*dst.BlockStmt); ok {
		newList := make([]dst.Stmt, 0, len(b.List)+1)
		for _, stmt := range b.List {
			newList = append(newList, stmt)
			if stmt == target {
				newList = append(newList, toInsert.(dst.Stmt))
			}
		}
		b.List = newList
	}
}

func generateBasicCheck(ctx rewriteContext) *dst.IfStmt {
	cond := &dst.BinaryExpr{
		X:  dst.NewIdent("err"),
		Op: token.NEQ,
		Y:  dst.NewIdent("nil"),
	}
	var body *dst.BlockStmt
	if ctx.isTerminal {
		body = generateDstTerminalBody(ctx.strategy, ctx.testParam)
	} else {
		zeroCtx := astgen.ZeroCtx{
			IsNameSafe: func(name string, expected types.Object) bool {
				// Shadowing check
				if ctx.scope == nil {
					return true
				}
				_, obj := ctx.scope.LookupParent(name, ctx.pos)
				if obj == nil {
					return true
				}
				return obj == expected
			},
		}
		body = generateDstReturnBody(ctx.enclosingSig, zeroCtx)
	}
	return &dst.IfStmt{Cond: cond, Body: body}
}

func generateCheckBlock(callExpr dst.Expr, ctx rewriteContext) *dst.IfStmt {
	assign := &dst.AssignStmt{
		Lhs: []dst.Expr{dst.NewIdent("err")},
		Tok: token.DEFINE,
		Rhs: []dst.Expr{dst.Clone(callExpr).(dst.Expr)},
	}
	ifStmt := generateBasicCheck(ctx)
	ifStmt.Init = assign
	return ifStmt
}

func generateDstTerminalBody(strategy MainHandlerStrategy, testParam string) *dst.BlockStmt {
	var stmts []dst.Stmt
	arg := dst.NewIdent("err")

	if testParam != "" {
		stmts = append(stmts, &dst.ExprStmt{
			X: &dst.CallExpr{
				Fun: &dst.SelectorExpr{
					X:   dst.NewIdent(testParam),
					Sel: dst.NewIdent("Fatal"),
				},
				Args: []dst.Expr{arg},
			},
		})
	} else {
		switch strategy {
		case HandlerPanic:
			stmts = []dst.Stmt{&dst.ExprStmt{X: &dst.CallExpr{Fun: dst.NewIdent("panic"), Args: []dst.Expr{arg}}}}
		case HandlerOsExit:
			stmts = []dst.Stmt{
				&dst.ExprStmt{X: &dst.CallExpr{Fun: &dst.SelectorExpr{X: dst.NewIdent("fmt"), Sel: dst.NewIdent("Println")}, Args: []dst.Expr{arg}}},
				&dst.ExprStmt{X: &dst.CallExpr{Fun: &dst.SelectorExpr{X: dst.NewIdent("os"), Sel: dst.NewIdent("Exit")}, Args: []dst.Expr{&dst.BasicLit{Kind: token.INT, Value: "1"}}}},
			}
		default:
			stmts = []dst.Stmt{&dst.ExprStmt{X: &dst.CallExpr{Fun: &dst.SelectorExpr{X: dst.NewIdent("log"), Sel: dst.NewIdent("Fatal")}, Args: []dst.Expr{arg}}}}
		}
	}
	return &dst.BlockStmt{List: stmts}
}

func generateDstReturnBody(sig *types.Signature, zeroCtx astgen.ZeroCtx) *dst.BlockStmt {
	var results []dst.Expr
	if sig != nil {
		limit := sig.Results().Len()
		// If last is error, don't zero it, use "err"
		limit--
		for i := 0; i < limit; i++ {
			t := sig.Results().At(i).Type()
			z, err := astgen.ZeroExprDST(t, zeroCtx)
			if err != nil {
				z = dst.NewIdent("nil")
			}
			results = append(results, z)
		}
	}
	results = append(results, dst.NewIdent("err"))
	return &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{Results: results}}}
}

func mapAstToDst(astFile *ast.File, dstFile *dst.File, targetNode ast.Node) (dst.Node, dst.Node) {
	start := targetNode.Pos()
	end := targetNode.End()
	path, _ := astutil.PathEnclosingInterval(astFile, start, end)
	if len(path) == 0 || path[len(path)-1] != astFile || start < astFile.Pos() || end > astFile.End() {
		return nil, nil
	}
	startIndex := -1
	for i, n := range path {
		if n == targetNode {
			startIndex = i
			break
		}
	}
	if startIndex == -1 {
		return nil, nil
	}

	var currentDst dst.Node = dstFile
	var parentDst dst.Node = nil

	for i := len(path) - 2; i >= startIndex; i-- {
		step, err := determineTraversalStepFn(path[i+1], path[i])
		if err != nil {
			return nil, nil
		}
		nextDst, err := applyTraversalStep(currentDst, step)
		if err != nil {
			return nil, nil
		}
		parentDst = currentDst
		currentDst = nextDst
	}
	return currentDst, parentDst
}

type tStep struct {
	FieldName string
	Index     int
}

func determineTraversalStep(parent, child ast.Node) (tStep, error) {
	val := reflect.ValueOf(parent)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	for i := 0; i < val.NumField(); i++ {
		fieldVal := val.Field(i)
		fieldType := val.Type().Field(i)
		name := fieldType.Name
		if fieldType.PkgPath != "" {
			continue
		}
		if fieldVal.Kind() == reflect.Slice {
			for idx := 0; idx < fieldVal.Len(); idx++ {
				if fieldVal.Index(idx).Interface() == child {
					return tStep{FieldName: name, Index: idx}, nil
				}
			}
		}
		if fieldVal.Kind() == reflect.Ptr || fieldVal.Kind() == reflect.Interface {
			if !fieldVal.IsNil() && fieldVal.Interface() == child {
				return tStep{FieldName: name, Index: -1}, nil
			}
		}
	}
	return tStep{}, fmt.Errorf("child not found in parent")
}

func applyTraversalStep(node dst.Node, step tStep) (dst.Node, error) {
	val := reflect.ValueOf(node)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	fieldVal := val.FieldByName(step.FieldName)
	if !fieldVal.IsValid() {
		return nil, fmt.Errorf("invalid field %s", step.FieldName)
	}
	if step.Index >= 0 {
		if fieldVal.Kind() != reflect.Slice || step.Index >= fieldVal.Len() {
			return nil, fmt.Errorf("dst slice index out of bounds")
		}
		if res, ok := fieldVal.Index(step.Index).Interface().(dst.Node); ok {
			return res, nil
		}
	} else {
		if res, ok := fieldVal.Interface().(dst.Node); ok {
			return res, nil
		}
	}
	return nil, fmt.Errorf("failed to extract node")
}

// ------------------------------------------------------------------------------------------------
// Utilities
// ------------------------------------------------------------------------------------------------

func findFile(pkg *packages.Package, pos token.Pos) *ast.File {
	for _, f := range pkg.Syntax {
		if f.Pos() <= pos && pos < f.End() {
			return f
		}
	}
	return nil
}

func isIdentFunctionInCall(call *ast.CallExpr, id *ast.Ident) bool {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun == id
	case *ast.SelectorExpr:
		return fun.Sel == id
	}
	return false
}

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

// IsEntryPoint checks if the function is main() or init().
func IsEntryPoint(fn *types.Func) bool {
	if fn.Name() == "init" {
		return true
	}
	if fn.Name() == "main" && fn.Pkg() != nil && fn.Pkg().Name() == "main" {
		return true
	}
	return false
}

func canReturnError(sig *types.Signature) bool {
	if sig == nil || sig.Results().Len() == 0 {
		return false
	}
	last := sig.Results().At(sig.Results().Len() - 1)
	return last.Type().String() == "error" || last.Type().String() == "builtin.error"
}

func getErrorReturnNameDST(ft *dst.FuncType) string {
	if ft.Results == nil {
		return ""
	}
	for _, field := range ft.Results.List {
		if isErrorDstExprWrapper(field.Type) {
			for _, name := range field.Names {
				if name.Name == "err" {
					return "err"
				}
				return name.Name
			}
		}
	}
	return ""
}

func isErrorDstExprWrapper(expr dst.Expr) bool {
	if id, ok := expr.(*dst.Ident); ok {
		return id.Name == "error"
	}
	return false
}

func ensureImportDST(file *dst.File, path string) {
	pathStr := fmt.Sprintf(`"%s"`, path)
	for _, imp := range file.Imports {
		if imp.Path != nil && imp.Path.Value == pathStr {
			return
		}
	}
	spec := &dst.ImportSpec{Path: &dst.BasicLit{Kind: token.STRING, Value: pathStr}}
	decl := &dst.GenDecl{Tok: token.IMPORT, Specs: []dst.Spec{spec}}
	file.Decls = append([]dst.Decl{decl}, file.Decls...)
	file.Imports = append(file.Imports, spec)
}

func getScope(pkg *packages.Package, node ast.Node) *types.Scope {
	if pkg == nil || pkg.TypesInfo == nil {
		return nil
	}
	if s := pkg.TypesInfo.Scopes[node]; s != nil {
		return s
	}
	path, _ := astutil.PathEnclosingInterval(findFile(pkg, node.Pos()), node.Pos(), node.End())
	for _, n := range path {
		if s := pkg.TypesInfo.Scopes[n]; s != nil {
			return s
		}
	}
	return pkg.Types.Scope()
}

func getSignature(obj types.Object) *types.Signature {
	if f, ok := obj.(*types.Func); ok {
		if sig, ok := f.Type().(*types.Signature); ok {
			return sig
		}
	}
	if v, ok := obj.(*types.Var); ok {
		if sig, ok := v.Type().(*types.Signature); ok {
			return sig
		}
	}
	return nil
}

func signaturesEquivalent(a, b *types.Signature) bool {
	if a == nil || b == nil {
		return false
	}
	aRes := a.Results()
	bRes := b.Results()
	if aRes.Len() != bRes.Len() {
		return false
	}
	for i := 0; i < aRes.Len(); i++ {
		t1 := aRes.At(i).Type()
		t2 := bRes.At(i).Type()
		if t1.String() != t2.String() {
			return false
		}
	}
	return true
}

func findIdentForObj(f *ast.File, obj types.Object) *ast.Ident {
	var match *ast.Ident
	ast.Inspect(f, func(n ast.Node) bool {
		if match != nil {
			return false
		}
		if id, ok := n.(*ast.Ident); ok {
			if id.Pos() == obj.Pos() && id.Name == obj.Name() {
				match = id
				return false
			}
		}
		return true
	})
	return match
}
