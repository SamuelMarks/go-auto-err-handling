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
	"github.com/dave/dst/decorator"
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

// PropagateCallers updates all call sites of a modified function to match its new signature
// (assuming the signature acquired an extra 'error' return value).
//
// It implements a Recursive Bubble-Up Strategy using a DST-safe approach:
// 1. Identify callers using type information.
// 2. Load the Caller's file as a Decorator Syntax Tree (DST).
// 3. Apply rewrites to the DST to handle the new return value.
// 4. Updates are applied in-memory to the DST; saving is handled by the runner.
//
// It respects entry points (`main`, `init`) and Test functions, treating them as terminals
// where errors are handled (log/panic) instead of bubbled.
//
// pkgs: The set of packages to search for callers.
// initialTarget: The function object whose signature was initially modified.
// strategy: The strategy to use for terminal handlers.
//
// Returns the total number of call sites updated and any error encountered.
func PropagateCallers(pkgs []*packages.Package, initialTarget *types.Func, strategy string) (int, error) {
	if initialTarget == nil {
		return 0, fmt.Errorf("target function is nil")
	}

	queue := []*types.Func{initialTarget}
	visited := make(map[*types.Func]bool)
	visited[initialTarget] = true

	totalUpdates := 0

	// We maintain a cache of decorated files to avoid re-parsing the same file multiple times
	// during a single propagation wave.
	// Map: File path -> *dst.File
	dstCache := make(map[string]*dst.File)

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

				// Get or Load DST
				filename := pkg.Fset.Position(file.Pos()).Filename
				var dstFile *dst.File
				if cached, ok := dstCache[filename]; ok {
					dstFile = cached
				} else {
					var err error
					dstFile, err = decorator.NewDecorator(pkg.Fset).DecorateFile(file)
					if err != nil {
						return totalUpdates, fmt.Errorf("failed to decorate file %s: %w", filename, err)
					}
					// WARNING: In a real runner, we might need to sync with the runner's DST view.
					dstCache[filename] = dstFile
				}

				updates, newTarget, err := processCallSiteDST(pkg, file, dstFile, id, target, strategy)
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

// processCallSiteDST handles the refactoring for a single usage using DST.
//
// pkg: The package containing the call.
// astFile: The AST file containing the call.
// dstFile: The DST file associated with the AST.
// id: The identifier in the AST representing the function call name.
// target: The types.Func object of the function being called.
// strategy: The main handler strategy string.
//
// Returns 1 if updated, and the new function object if recursion is needed.
func processCallSiteDST(pkg *packages.Package, astFile *ast.File, dstFile *dst.File, id *ast.Ident, target *types.Func, strategy string) (int, *types.Func, error) {
	if astFile == nil {
		return 0, nil, fmt.Errorf("astFile is nil")
	}
	if dstFile == nil {
		return 0, nil, fmt.Errorf("dstFile is nil")
	}
	if id == nil {
		return 0, nil, fmt.Errorf("identifier is nil")
	}

	// Find the components in AST first to understand context
	path, _ := astutil.PathEnclosingInterval(astFile, id.Pos(), id.Pos())

	// Identify AST components
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

	// ---------------------------------------------------------
	// DST Mapping
	// Use local implementation of matching logic to avoid circular import with rewrite
	dstStmt, dstParent := mapAstToDst(astFile, dstFile, enclosingStmt)
	if dstStmt == nil {
		return 0, nil, fmt.Errorf("failed to map AST stmt to DST stmt")
	}
	// ---------------------------------------------------------

	// Analyze enclosing function context
	sig, funcObj, decl := findEnclosingFuncDetails(path, pkg.TypesInfo)

	isTerminal := false
	testParam := ""

	// Determine if terminal
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

	// Decision: Bubble or Handle?
	var nextTarget *types.Func
	var dstDecl *dst.FuncDecl

	// Map decl to DST if present, as it's needed for signature updates or defer logic
	// If decl is nil (e.g. main/init logic or literal), funcObj might be nil or valid.
	if decl != nil {
		res, _ := mapAstToDst(astFile, dstFile, decl)
		if f, ok := res.(*dst.FuncDecl); ok {
			dstDecl = f
		}
	}

	if !isTerminal && decl != nil && funcObj != nil {
		canReturn := canReturnError(sig)

		// Upgrade Signature if needed
		if !canReturn {
			if dstDecl != nil {
				changed, err := AddErrorToSignatureDST(dstDecl) // Refactor package helper
				if err != nil {
					return 0, nil, err
				}
				if changed {
					// Patch AST/Types so 'sig' and 'funcObj' reflect the new reality for semantic checks
					if err := PatchSignature(pkg.TypesInfo, decl, pkg.Types); err != nil {
						return 0, nil, err
					}
					// Reload objects from patched info to get the NEW sig
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

	// Perform DST Rewrite of the call site
	context := rewriteContext{
		dstFile:        dstFile,
		stmt:           dstStmt,
		parent:         dstParent,
		enclosingSig:   sig,
		enclosingFnDst: dstDecl,
		isTerminal:     isTerminal,
		strategy:       MainHandlerStrategy(strategy),
		testParam:      testParam,
		targetSig:      target.Type().(*types.Signature), // Pass the called function's new signature
	}

	if err := refactorCallSiteDST(context); err != nil {
		return 0, nil, err
	}

	return 1, nextTarget, nil
}

// HandleEntryPoint injects terminal error handling for a call site within an entry point (main/init) using DST.
//
// pkg: The package info.
// dstFile: The DST file to modify.
// call: The AST call expression target.
// stmt: The AST statement definition.
// strategy: The handling strategy (log, panic, etc).
func HandleEntryPoint(pkg *packages.Package, dstFile *dst.File, call *ast.CallExpr, stmt ast.Stmt, strategy string) error {
	// Need AST file to map. We rely on finding it in pkg.
	astFile := findFile(pkg, stmt.Pos())
	if astFile == nil {
		return fmt.Errorf("could not locate AST file for stmt")
	}

	dstStmt, dstParent := mapAstToDst(astFile, dstFile, stmt)
	if dstStmt == nil {
		return fmt.Errorf("failed to locate entry point statement in DST")
	}

	ctx := rewriteContext{
		dstFile:      dstFile,
		stmt:         dstStmt,
		parent:       dstParent,
		enclosingSig: nil,
		isTerminal:   true,
		strategy:     MainHandlerStrategy(strategy),
		testParam:    "",
	}
	return refactorCallSiteDST(ctx)
}

// HandleTestError injects terminal error handling for a call site within a test function or subtest closure.
// It uses `testParamName` (e.g. "t") to invoke `t.Fatal(err)`.
//
// pkg: The package info.
// dstFile: The DST file to modify.
// call: The AST call expression target.
// stmt: The AST statement definition.
// testParamName: The name of the testing parameter (e.g. "t" or "b").
func HandleTestError(pkg *packages.Package, dstFile *dst.File, call *ast.CallExpr, stmt ast.Stmt, testParamName string) error {
	astFile := findFile(pkg, stmt.Pos())
	if astFile == nil {
		return fmt.Errorf("could not locate AST file for stmt")
	}

	dstStmt, dstParent := mapAstToDst(astFile, dstFile, stmt)
	if dstStmt == nil {
		return fmt.Errorf("failed to locate test statement in DST")
	}

	ctx := rewriteContext{
		dstFile:      dstFile,
		stmt:         dstStmt,
		parent:       dstParent,
		enclosingSig: nil,
		isTerminal:   true,
		strategy:     "",
		testParam:    testParamName,
	}
	return refactorCallSiteDST(ctx)
}

// rewriteContext aggregates arguments for refactoring to reduce signature complexity.
type rewriteContext struct {
	dstFile        *dst.File
	stmt           dst.Node
	parent         dst.Node
	enclosingSig   *types.Signature // Signature of the function containing the call
	enclosingFnDst *dst.FuncDecl    // DST of function containing the call (if applicable)
	isTerminal     bool
	strategy       MainHandlerStrategy
	testParam      string
	targetSig      *types.Signature // Signature of the function being called
}

// enclosing returns enclosingSig alias to match struct field.
func (r *rewriteContext) enclosing() *types.Signature {
	return r.enclosingSig
}

// refactorCallSiteDST modifies the DST to handle the extra error return.
// It handles Expression, Assignment, Go, and Defer statements.
func refactorCallSiteDST(ctx rewriteContext) error {
	stmt := ctx.stmt.(dst.Stmt)

	// Identify Statement Type
	switch s := stmt.(type) {
	case *dst.ExprStmt:
		// Convert ExprStmt to Assign + Check
		// call() -> if err := call(); err != nil ...
		call := s.X

		// Passthrough Optimization
		isTail := false
		if block, ok := ctx.parent.(*dst.BlockStmt); ok {
			// Find index
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
				// Optimization: return call()
				// Note: call() in ExprStmt context ignores results (though in this case it likely returned nothing before rewrite).
				// We assume after rewrite it returns full tuple.
				ret := &dst.ReturnStmt{
					Results: []dst.Expr{dst.Clone(call).(dst.Expr)},
				}
				replaceInParent(ctx.parent, stmt, ret)
				return nil
			}
		}

		// Generate Check Block
		block := generateCheckBlock(call, ctx.enclosing(), ctx.isTerminal, ctx.strategy, ctx.testParam)

		replaceInParent(ctx.parent, stmt, block)

	case *dst.AssignStmt:
		// x := call() -> x, err := call(); if err != nil ...
		// Append 'err' to LHS
		s.Lhs = append(s.Lhs, dst.NewIdent("err"))

		// Check invalidation of `_` handled implicitly by valid Go assignment

		// 2. Construct Check
		check := generateBasicCheck(ctx.enclosing(), ctx.isTerminal, ctx.strategy, ctx.testParam)

		// 3. Insert Check After Assignment
		insertAfterInParent(ctx.parent, stmt, check)

	case *dst.GoStmt:
		// go call() -> go func(){ if err := call(); err != nil { handle } }()
		return refactorGoStmt(ctx, s)

	case *dst.DeferStmt:
		// defer call() -> defer func(){ err = errors.Join(err, call()) }()
		return refactorDeferStmt(ctx, s)

	default:
		return fmt.Errorf("unsupported statement type for propagation: %T", stmt)
	}
	return nil
}

// refactorGoStmt transforms a 'go call()' where call() now returns an error
// into an anonymous closure that handles the error terminally (log usually).
//
// ctx: The rewrite context.
// gs: The GoStmt node.
func refactorGoStmt(ctx rewriteContext, gs *dst.GoStmt) error {
	// Construct the body of the closure:
	// if err := call(); err != nil { log.Fatal(err) }
	// Important: We cannot return from a go routine to the parent, so we MUST use terminal strategy.

	// Extract the call
	call := dst.Clone(gs.Call).(*dst.CallExpr)

	// We use logic similar to `generateCheckBlock` but ensure terminal logic
	// If the user specified 'panic', we panic. If 'log-fatal', we log.
	// We force isTerminal=true for the context of the goroutine body.
	checkStmt := generateCheckBlock(call, nil, true, ctx.strategy, "")

	// Create closure: func() { <body> }
	fnLit := &dst.FuncLit{
		Type: &dst.FuncType{
			Params:  &dst.FieldList{},
			Results: nil,
		},
		Body: &dst.BlockStmt{
			List: []dst.Stmt{checkStmt},
		},
	}

	// Create new go statement: go func(){}()
	newGo := &dst.GoStmt{
		Call: &dst.CallExpr{
			Fun:  fnLit,
			Args: nil,
		},
	}

	// Replace
	replaceInParent(ctx.parent, gs, newGo)
	return nil
}

// refactorDeferStmt transforms a 'defer call()' where call() now returns an error.
//
// If the enclosing function returns an error, it attempts to capture the deferred
// error using 'errors.Join' (requires named returns).
// If the enclosing function does not return an error (or is main), it logs the error.
//
// ctx: The rewrite context.
// ds: The DeferStmt node.
func refactorDeferStmt(ctx rewriteContext, ds *dst.DeferStmt) error {
	call := dst.Clone(ds.Call).(*dst.CallExpr)
	parentCanReturnErr := canReturnError(ctx.enclosing())

	var body *dst.BlockStmt

	if parentCanReturnErr && ctx.enclosingFnDst != nil {
		// Strategy: Capture.
		// 1. Ensure named returns exist on the enclosing function so we can reference 'err'.
		_, err := EnsureNamedReturnsDST(ctx.enclosingFnDst)
		if err != nil {
			return err
		}
		// If changed, the caller might need to know, but since we are modifying DST in place, fine.

		// 2. Find the name of the error return variable.
		errVar := getErrorReturnNameDST(ctx.enclosingFnDst.Type)
		if errVar == "" {
			// Fallback if failed to find name
			return refactorDeferLogFallback(ctx, call, ds)
		}

		// 3. Construct: err = errors.Join(err, call())
		ensureImportDST(ctx.dstFile, "errors") // Require invalid 'errors' import

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

		body = &dst.BlockStmt{
			List: []dst.Stmt{assign},
		}
	} else {
		// Strategy: Log Fallback.
		// Enclosing function is void, main, or we couldn't resolve details.
		return refactorDeferLogFallback(ctx, call, ds)
	}

	// Wrap in closure
	fnLit := &dst.FuncLit{
		Type: &dst.FuncType{Params: &dst.FieldList{}},
		Body: body,
	}

	newDefer := &dst.DeferStmt{
		Call: &dst.CallExpr{Fun: fnLit},
	}

	replaceInParent(ctx.parent, ds, newDefer)
	return nil
}

// refactorDeferLogFallback generates a defer wrapper that logs the error.
// defer func() { if err := call(); err != nil { log.Printf("deferred error: %v", err) } }()
func refactorDeferLogFallback(ctx rewriteContext, call *dst.CallExpr, ds *dst.DeferStmt) error {
	ensureImportDST(ctx.dstFile, "log")

	// Assign: err := call()
	assign := &dst.AssignStmt{
		Lhs: []dst.Expr{dst.NewIdent("err")},
		Tok: token.DEFINE,
		Rhs: []dst.Expr{call},
	}

	// Check: if err != nil
	cond := &dst.BinaryExpr{
		X:  dst.NewIdent("err"),
		Op: token.NEQ,
		Y:  dst.NewIdent("nil"),
	}

	// Log stmt
	logCall := &dst.CallExpr{
		Fun: &dst.SelectorExpr{
			X:   dst.NewIdent("log"),
			Sel: dst.NewIdent("Printf"),
		},
		Args: []dst.Expr{
			&dst.BasicLit{Kind: token.STRING, Value: `"deferred error: %v"`},
			dst.NewIdent("err"),
		},
	}

	ifStmt := &dst.IfStmt{
		Init: assign,
		Cond: cond,
		Body: &dst.BlockStmt{
			List: []dst.Stmt{
				&dst.ExprStmt{X: logCall},
			},
		},
	}

	fnLit := &dst.FuncLit{
		Type: &dst.FuncType{Params: &dst.FieldList{}},
		Body: &dst.BlockStmt{List: []dst.Stmt{ifStmt}},
	}

	newDefer := &dst.DeferStmt{
		Call: &dst.CallExpr{Fun: fnLit},
	}

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

func generateBasicCheck(sig *types.Signature, isTerminal bool, strategy MainHandlerStrategy, testParam string) *dst.IfStmt {
	cond := &dst.BinaryExpr{
		X:  dst.NewIdent("err"),
		Op: token.NEQ,
		Y:  dst.NewIdent("nil"),
	}

	var body *dst.BlockStmt
	if isTerminal {
		body = generateDstTerminalBody(strategy, testParam)
	} else {
		body = generateDstReturnBody(sig)
	}

	return &dst.IfStmt{
		Cond: cond,
		Body: body,
	}
}

func generateCheckBlock(callExpr dst.Expr, sig *types.Signature, isTerminal bool, strategy MainHandlerStrategy, testParam string) *dst.IfStmt {
	// Call expression needs to be cloned to be moved
	assign := &dst.AssignStmt{
		Lhs: []dst.Expr{dst.NewIdent("err")},
		Tok: token.DEFINE,
		Rhs: []dst.Expr{dst.Clone(callExpr).(dst.Expr)},
	}

	ifStmt := generateBasicCheck(sig, isTerminal, strategy, testParam)
	// Collapse: if err := call(); err != nil
	ifStmt.Init = assign

	return ifStmt
}

func generateDstTerminalBody(strategy MainHandlerStrategy, testParam string) *dst.BlockStmt {
	var stmts []dst.Stmt
	arg := dst.NewIdent("err")

	if testParam != "" {
		// t.Fatal(err)
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
			stmts = []dst.Stmt{
				&dst.ExprStmt{
					X: &dst.CallExpr{
						Fun:  dst.NewIdent("panic"),
						Args: []dst.Expr{arg},
					},
				},
			}
		case HandlerOsExit:
			// fmt.Println(err); os.Exit(1)
			stmts = []dst.Stmt{
				&dst.ExprStmt{
					X: &dst.CallExpr{
						Fun:  &dst.SelectorExpr{X: dst.NewIdent("fmt"), Sel: dst.NewIdent("Println")},
						Args: []dst.Expr{arg},
					},
				},
				&dst.ExprStmt{
					X: &dst.CallExpr{
						Fun:  &dst.SelectorExpr{X: dst.NewIdent("os"), Sel: dst.NewIdent("Exit")},
						Args: []dst.Expr{&dst.BasicLit{Kind: token.INT, Value: "1"}},
					},
				},
			}
		default: // logs
			stmts = []dst.Stmt{
				&dst.ExprStmt{
					X: &dst.CallExpr{
						Fun:  &dst.SelectorExpr{X: dst.NewIdent("log"), Sel: dst.NewIdent("Fatal")},
						Args: []dst.Expr{arg},
					},
				},
			}
		}
	}
	return &dst.BlockStmt{List: stmts}
}

func generateDstReturnBody(sig *types.Signature) *dst.BlockStmt {
	var results []dst.Expr
	if sig != nil {
		limit := sig.Results().Len()
		// If last is error, exclude it from zero-generation
		limit--
		for i := 0; i < limit; i++ {
			t := sig.Results().At(i).Type()
			z, _ := astgen.ZeroExprDST(t, astgen.ZeroCtx{}) // ignoring error in propagation for simplicity
			results = append(results, z)
		}
	}
	results = append(results, dst.NewIdent("err"))
	return &dst.BlockStmt{
		List: []dst.Stmt{
			&dst.ReturnStmt{Results: results},
		},
	}
}

// mapAstToDst finds the DST node corresponding to an AST node.
// It duplicates logic from pkg/rewrite/mapper.go to avoid import cycle.
func mapAstToDst(astFile *ast.File, dstFile *dst.File, targetNode ast.Node) (dst.Node, dst.Node) {
	path, _ := astutil.PathEnclosingInterval(astFile, targetNode.Pos(), targetNode.End())
	if len(path) == 0 || path[len(path)-1] != astFile {
		return nil, nil
	}

	// Find the startIndex of targetNode in the path
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

	// Traverse down from file root
	for i := len(path) - 2; i >= startIndex; i-- {
		astParent := path[i+1]
		astChild := path[i]

		step, err := determineTraversalStep(astParent, astChild)
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

// Local duplicate of rewrite.traversalStep
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
			continue // unexported
		}

		if fieldVal.Kind() == reflect.Slice {
			for idx := 0; idx < fieldVal.Len(); idx++ {
				if !fieldVal.Index(idx).CanInterface() {
					continue
				}
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
		// Use literal error description "dst slice index out of bounds" to match test expectations
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
//
// fn: The function type object.
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
	var lastErrName string
	for _, field := range ft.Results.List {
		if isErrorDstExprWrapper(field.Type) {
			for _, name := range field.Names {
				if name.Name == "err" {
					return "err"
				}
				lastErrName = name.Name
			}
		}
	}
	return lastErrName
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
	spec := &dst.ImportSpec{
		Path: &dst.BasicLit{Kind: token.STRING, Value: pathStr},
	}
	decl := &dst.GenDecl{
		Tok:   token.IMPORT,
		Specs: []dst.Spec{spec},
	}
	// Append to top of file
	file.Decls = append([]dst.Decl{decl}, file.Decls...)
	// Synchronize Imports slice to avoid duplicates if called multiple times in same pass
	file.Imports = append(file.Imports, spec)
}
