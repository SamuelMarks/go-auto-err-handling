package rewrite

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/analysis"
	"github.com/dave/dst"
	"golang.org/x/tools/go/packages"
)

func TestInjectorHelpers_Basic(t *testing.T) {
	inj := &Injector{}
	if got := inj.normalizeNonErrorFallback(""); got != "log" {
		t.Fatalf("expected log, got %q", got)
	}
	if got := inj.normalizeNonErrorFallback(" log-fatal "); got != "fatal" {
		t.Fatalf("expected fatal, got %q", got)
	}
	if got := inj.normalizeNonErrorFallback("panic"); got != "panic" {
		t.Fatalf("expected panic, got %q", got)
	}
	if got := inj.normalizeNonErrorFallback("unknown"); got != "log" {
		t.Fatalf("expected default log, got %q", got)
	}

	if got := inj.resolveFuncName(analysis.InjectionPoint{}); got != "func" {
		t.Fatalf("expected func, got %q", got)
	}
	if got := inj.resolveFuncName(analysis.InjectionPoint{Call: &ast.CallExpr{Fun: ast.NewIdent("do")}}); got != "do" {
		t.Fatalf("expected do, got %q", got)
	}
	if got := inj.resolveFuncName(analysis.InjectionPoint{Call: &ast.CallExpr{Fun: &ast.SelectorExpr{X: ast.NewIdent("pkg"), Sel: ast.NewIdent("Run")}}}); got != "Run" {
		t.Fatalf("expected Run, got %q", got)
	}
	if got := inj.resolveFuncName(analysis.InjectionPoint{Call: &ast.CallExpr{Fun: &ast.FuncLit{}}}); got != "func" {
		t.Fatalf("expected func for non-ident, got %q", got)
	}

	if !inj.isErrorExpr(ast.NewIdent("error")) {
		t.Fatal("expected error expr")
	}
	if inj.isErrorExpr(&ast.SelectorExpr{X: ast.NewIdent("pkg"), Sel: ast.NewIdent("Err")}) {
		t.Fatal("unexpected error expr")
	}

	call := &ast.CallExpr{Fun: ast.NewIdent("f")}
	if astNodeContainsCall(nil, call) {
		t.Fatal("expected false for nil root")
	}
	if astNodeContainsCall(ast.NewIdent("x"), nil) {
		t.Fatal("expected false for nil call")
	}
	if !astNodeContainsCall(&ast.ExprStmt{X: call}, call) {
		t.Fatal("expected call to be found")
	}
}

func TestAddImportDST(t *testing.T) {
	file := &dst.File{Imports: []*dst.ImportSpec{{Path: &dst.BasicLit{Kind: token.STRING, Value: "\"fmt\""}}}}
	inj := &Injector{}
	inj.addImportDST(file, "fmt")
	if len(file.Imports) != 1 {
		t.Fatalf("unexpected import duplication")
	}
	inj.addImportDST(file, "log")
	if len(file.Imports) != 2 {
		t.Fatalf("expected new import added")
	}
}

func TestGenerateTerminalHandlerDST(t *testing.T) {
	file := &dst.File{}
	inj := &Injector{TestParam: "t"}
	blk := inj.generateTerminalHandlerDST("err", file)
	if len(blk.List) != 1 {
		t.Fatal("expected one stmt for test handler")
	}

	inj = &Injector{MainHandlerStrategy: "panic"}
	blk = inj.generateTerminalHandlerDST("err", file)
	if len(blk.List) != 1 {
		t.Fatal("expected panic handler")
	}

	inj = &Injector{MainHandlerStrategy: "os-exit"}
	blk = inj.generateTerminalHandlerDST("err", file)
	if len(blk.List) != 2 {
		t.Fatal("expected os-exit handler with 2 stmts")
	}

	inj = &Injector{MainHandlerStrategy: "log-fatal"}
	blk = inj.generateTerminalHandlerDST("err", file)
	if len(blk.List) != 1 {
		t.Fatal("expected log-fatal handler")
	}
}

func TestTransferTrivia(t *testing.T) {
	inj := &Injector{}
	inj.transferTrivia(&dst.EmptyStmt{}, nil)

	first := &dst.EmptyStmt{}
	second := &dst.EmptyStmt{}
	inj.transferTrivia(&dst.EmptyStmt{}, []dst.Stmt{first, second})
	if first.Decorations().After != dst.NewLine {
		t.Fatal("expected newline decoration for multi-stmt")
	}
}

func TestIsCallEmbeddedInComposite(t *testing.T) {
	inj := &Injector{}
	call := &dst.CallExpr{Fun: dst.NewIdent("f")}
	if inj.isCallEmbeddedInComposite(&dst.ExprStmt{X: call}, nil) {
		t.Fatal("expected false for nil call")
	}
	lit := &dst.CompositeLit{Elts: []dst.Expr{call}}
	if !inj.isCallEmbeddedInComposite(&dst.ExprStmt{X: lit}, call) {
		t.Fatal("expected call in composite literal")
	}
	kv := &dst.KeyValueExpr{Key: dst.NewIdent("F"), Value: call}
	lit = &dst.CompositeLit{Elts: []dst.Expr{kv}}
	if !inj.isCallEmbeddedInComposite(&dst.ExprStmt{X: lit}, call) {
		t.Fatal("expected call in key value expr")
	}
}

func TestGetScopeContextLoopAndSupport(t *testing.T) {
	src := `package main
func sub() error { return nil } 
func subLit() error { return nil } 
var _ = func() error { subLit(); return nil } 
func plain() { sub() } 
func run() error { 
  for { 
    sub() 
    break
  } 
  return nil
} 
func runRange() error { 
  for range []int{1} { 
    sub() 
  } 
  return nil
} 
`
	inj, _, astFile := setupInjectorTest(t, src)
	ptLoop := findPointCtx(t, astFile, "run", "sub")
	if !inj.isInsideLoop(ptLoop) {
		t.Fatal("expected inside loop")
	}
	ptRange := findPointCtx(t, astFile, "runRange", "sub")
	if !inj.isInsideLoop(ptRange) {
		t.Fatal("expected inside range loop")
	}
	ptPlain := findPointCtx(t, astFile, "plain", "sub")
	if inj.isInsideLoop(ptPlain) {
		t.Fatal("expected plain call outside loop")
	}
	if ctx := inj.getEnclosingContext(ptLoop); ctx.sig == nil || ctx.decl == nil {
		t.Fatal("expected func decl context")
	}
	ptLit := findPoint(t, astFile, "subLit", false)
	if ctx := inj.getEnclosingContext(ptLit); ctx.sig == nil || ctx.decl != nil {
		t.Fatal("expected func lit context")
	}
	if inj.isInsideLoop(ptLit) {
		t.Fatal("expected func lit call outside loop")
	}
	if ctx := inj.getEnclosingContext(analysis.InjectionPoint{File: astFile, Pos: token.NoPos}); ctx.sig != nil || ctx.decl != nil {
		t.Fatal("expected empty context outside functions")
	}

	if inj.getScope(token.NoPos, astFile) == nil {
		t.Fatal("expected scope from types info")
	}
	inj.Pkg.TypesInfo = nil
	if inj.getScope(token.NoPos, astFile) != nil {
		t.Fatal("expected nil scope without types info")
	}

	// supportsErrorReturn
	sig := inj.Pkg.Types.Scope().Lookup("sub").(*types.Func).Type().(*types.Signature)
	if !(&Injector{}).supportsErrorReturn(sig, nil) {
		t.Fatal("expected support with error signature")
	}
	decl := &ast.FuncDecl{Type: &ast.FuncType{Results: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("error")}}}}}
	if !(&Injector{}).supportsErrorReturn(nil, decl) {
		t.Fatal("expected support from decl")
	}
	if (&Injector{}).supportsErrorReturn(nil, &ast.FuncDecl{Type: &ast.FuncType{}}) {
		t.Fatal("unexpected support")
	}
}

func TestSignaturesMatch(t *testing.T) {
	inj := &Injector{Pkg: &packages.Package{}}
	if inj.signaturesMatch(nil, &ast.CallExpr{}) {
		t.Fatal("expected false with nil types info")
	}

	src := `package main
func a() error { return nil } 
func b() (int, error) { return 0, nil } 
func c() int { return 0 } 
func g() error { return a() } 
func h() (int, error) { return b() } 
func k() int { return c() } 
`
	inj, _, astFile := setupInjectorTest(t, src)
	callA := findCallInNode(astFile, "a")
	callB := findCallInNode(astFile, "b")
	callC := findCallInNode(astFile, "c")
	if callA == nil || callB == nil || callC == nil {
		t.Fatal("expected calls")
	}

	// Match
	obj := inj.Pkg.Types.Scope().Lookup("g").(*types.Func)
	if !inj.signaturesMatch(obj.Type().(*types.Signature), callA) {
		t.Fatal("expected signatures to match")
	}
	// Tuple match
	obj = inj.Pkg.Types.Scope().Lookup("h").(*types.Func)
	if !inj.signaturesMatch(obj.Type().(*types.Signature), callB) {
		t.Fatal("expected tuple signatures to match")
	}

	// Mismatch length
	if inj.signaturesMatch(obj.Type().(*types.Signature), callA) {
		t.Fatal("expected mismatch length")
	}

	// Mismatch type with same length
	obj = inj.Pkg.Types.Scope().Lookup("c").(*types.Func)
	if inj.signaturesMatch(obj.Type().(*types.Signature), callA) {
		t.Fatal("expected mismatch type")
	}

	// Call not in types info
	if inj.signaturesMatch(obj.Type().(*types.Signature), &ast.CallExpr{Fun: ast.NewIdent("missing")}) {
		t.Fatal("expected mismatch for missing call info")
	}
}

func TestGenerateZeroReturns(t *testing.T) {
	inj := &Injector{Pkg: &packages.Package{}}
	// nil sig
	zs, err := inj.generateZeroReturns(analysis.InjectionPoint{}, nil, nil)
	if err != nil || len(zs) != 0 {
		t.Fatal("expected empty zero returns")
	}

	sig := types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(
		types.NewVar(token.NoPos, nil, "", types.Typ[types.Int]),
		types.NewVar(token.NoPos, nil, "", types.Universe.Lookup("error").Type()),
	), false)
	zs, err = inj.generateZeroReturns(analysis.InjectionPoint{}, sig, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(zs) != 1 {
		t.Fatalf("expected one zero (excluding error), got %d", len(zs))
	}

	sig2 := types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(
		types.NewVar(token.NoPos, nil, "", types.Typ[types.Int]),
		types.NewVar(token.NoPos, nil, "", types.Typ[types.String]),
	), false)
	zs, err = inj.generateZeroReturns(analysis.InjectionPoint{}, sig2, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(zs) != 2 {
		t.Fatalf("expected two zeros, got %d", len(zs))
	}

	// Scope-aware generation
	src := `package main
func f() error { return nil } 
`
	inj2, _, astFile := setupInjectorTest(t, src)
	pt := analysis.InjectionPoint{File: astFile, Pos: astFile.Decls[0].Pos()}
	sig3 := inj2.Pkg.Types.Scope().Lookup("f").(*types.Func).Type().(*types.Signature)
	if _, err := inj2.generateZeroReturns(pt, sig3, nil); err != nil {
		t.Fatalf("unexpected scope-aware error: %v", err)
	}

	// Force ZeroExprDST error with tuple type
	badSig := types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(
		types.NewVar(token.NoPos, nil, "", types.NewTuple(types.NewVar(token.NoPos, nil, "", types.Typ[types.Int]))),
	), false)
	if _, err := inj.generateZeroReturns(analysis.InjectionPoint{}, badSig, nil); err == nil {
		t.Fatal("expected error from tuple type")
	}
}

func TestGenerateAssignmentDST(t *testing.T) {
	inj := &Injector{Pkg: &packages.Package{}}
	if _, err := inj.generateAssignmentDST(analysis.InjectionPoint{}, &dst.CallExpr{}, "err", token.DEFINE); err == nil {
		t.Fatal("expected missing types info error")
	}

	src := `package main
func f() (int, error) { return 0, nil } 
func g() error { var err error; s := struct{ X int }{}; s.X, err = f(); _ = err; return nil } 
func g2() error { var x int; var err error; x, err = f(); _ = x; _ = err; return nil } 
func h() error { f(); return nil } 
`
	inj, _, astFile := setupInjectorTest(t, src)

	// Missing type info for call
	if _, err := inj.generateAssignmentDST(analysis.InjectionPoint{Call: &ast.CallExpr{Fun: ast.NewIdent("missing")}}, &dst.CallExpr{}, "err", token.DEFINE); err == nil {
		t.Fatal("expected missing type info error")
	}

	// With Assign (non-ident LHS -> underscore)
	var assign *ast.AssignStmt
	var call *ast.CallExpr
	ast.Inspect(astFile, func(n ast.Node) bool {
		if a, ok := n.(*ast.AssignStmt); ok && len(a.Lhs) == 2 {
			if _, ok := a.Lhs[0].(*ast.SelectorExpr); ok {
				assign = a
				ast.Inspect(a, func(nn ast.Node) bool {
					if c, ok := nn.(*ast.CallExpr); ok {
						if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "f" {
							call = c
							return false
						}
					}
					return true
				})
				return false
			}
		}
		return true
	})
	if assign == nil || call == nil {
		t.Fatal("expected assign and call")
	}
	stmt, err := inj.generateAssignmentDST(analysis.InjectionPoint{Assign: assign, Call: call}, &dst.CallExpr{Fun: dst.NewIdent("f")}, "err", token.DEFINE)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	as := stmt.(*dst.AssignStmt)
	if len(as.Lhs) < 2 {
		t.Fatal("expected lhs with err")
	}
	if id, ok := as.Lhs[0].(*dst.Ident); !ok || id.Name != "_" {
		t.Fatal("expected non-ident lhs to become _")
	}

	// Ident LHS path
	var assignIdent *ast.AssignStmt
	var callIdent *ast.CallExpr
	ast.Inspect(astFile, func(n ast.Node) bool {
		if a, ok := n.(*ast.AssignStmt); ok && len(a.Lhs) == 2 {
			if id, ok := a.Lhs[0].(*ast.Ident); ok && id.Name == "x" {
				assignIdent = a
				ast.Inspect(a, func(nn ast.Node) bool {
					if c, ok := nn.(*ast.CallExpr); ok {
						if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "f" {
							callIdent = c
							return false
						}
					}
					return true
				})
				return false
			}
		}
		return true
	})
	if assignIdent == nil || callIdent == nil {
		t.Fatal("expected ident assign and call")
	}
	stmt, err = inj.generateAssignmentDST(analysis.InjectionPoint{Assign: assignIdent, Call: callIdent}, &dst.CallExpr{Fun: dst.NewIdent("f")}, "err", token.DEFINE)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	as = stmt.(*dst.AssignStmt)
	if id, ok := as.Lhs[0].(*dst.Ident); !ok || id.Name != "x" {
		t.Fatal("expected ident lhs to remain")
	}

	// ExprStmt path (no Assign)
	point := findPointCtx(t, astFile, "h", "f")
	stmt, err = inj.generateAssignmentDST(point, &dst.CallExpr{Fun: dst.NewIdent("f")}, "err", token.DEFINE)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	as = stmt.(*dst.AssignStmt)
	if len(as.Lhs) == 0 {
		t.Fatal("expected lhs entries")
	}
}

func TestGenerateNonErrorFallbackDST(t *testing.T) {
	src := `package main
func f() error { return nil } 
func g() { f() } 
`
	inj, dstFile, astFile := setupInjectorTest(t, src)
	pt := findPointCtx(t, astFile, "g", "f")
	resStmt, _ := FindDstNode(inj.Fset, dstFile, astFile, pt.Stmt)
	dstStmt := resStmt.Node.(dst.Stmt)
	origAssign := generateAssignmentDSTFunc
	origResolve := resolveErrorVarFunc
	t.Cleanup(func() {
		generateAssignmentDSTFunc = origAssign
		resolveErrorVarFunc = origResolve
	})

	// default (log)
	stmts, needsLog, err := inj.generateNonErrorFallbackDST(pt, dstStmt, "")
	if err != nil || len(stmts) == 0 || !needsLog {
		t.Fatal("expected log fallback")
	}

	// panic strategy
	stmts, needsLog, err = inj.generateNonErrorFallbackDST(pt, dstStmt, "panic")
	if err != nil || len(stmts) == 0 || needsLog {
		t.Fatal("expected panic fallback")
	}

	// fatal strategy
	stmts, needsLog, err = inj.generateNonErrorFallbackDST(pt, dstStmt, "fatal")
	if err != nil || len(stmts) == 0 || !needsLog {
		t.Fatal("expected fatal fallback")
	}

	// no call in stmt
	if _, _, err := inj.generateNonErrorFallbackDST(pt, &dst.ReturnStmt{}, "log"); err == nil {
		t.Fatal("expected error for stmt without call")
	}

	// force generateAssignmentDST error via hook
	generateAssignmentDSTFunc = func(_ *Injector, _ analysis.InjectionPoint, _ *dst.CallExpr, _ string, _ token.Token) (dst.Stmt, error) {
		return nil, errors.New("boom")
	}
	if _, _, err := inj.generateNonErrorFallbackDST(pt, dstStmt, "log"); err == nil {
		t.Fatal("expected assignment error")
	}
	generateAssignmentDSTFunc = origAssign

	// declStmt branch via hook
	resolveErrorVarFunc = func(_ *Injector, _ analysis.InjectionPoint, _ *types.Scope) (string, token.Token, *dst.DeclStmt) {
		return "err", token.DEFINE, &dst.DeclStmt{Decl: &dst.GenDecl{Tok: token.VAR}}
	}
	stmts, _, err = inj.generateNonErrorFallbackDST(pt, dstStmt, "log")
	if err != nil || len(stmts) < 2 {
		t.Fatal("expected declStmt in result")
	}
	resolveErrorVarFunc = origResolve

	// non-Assign branch via hook
	generateAssignmentDSTFunc = func(_ *Injector, _ analysis.InjectionPoint, _ *dst.CallExpr, _ string, _ token.Token) (dst.Stmt, error) {
		return &dst.ExprStmt{X: dst.NewIdent("noop")}, nil
	}
	if _, _, err := inj.generateNonErrorFallbackDST(pt, dstStmt, "log"); err != nil {
		t.Fatal("unexpected error in non-assign path")
	}
	generateAssignmentDSTFunc = origAssign
}

func TestGenerateLogRewriteDST(t *testing.T) {
	src := `package main
func f() error { return nil } 
func g() { f() } 
`
	inj, dstFile, astFile := setupInjectorTest(t, src)
	pt := findPointCtx(t, astFile, "g", "f")
	resStmt, _ := FindDstNode(inj.Fset, dstFile, astFile, pt.Stmt)
	if stmts, err := inj.generateLogRewriteDST(pt, resStmt.Node.(dst.Stmt)); err != nil || len(stmts) == 0 {
		t.Fatal("expected log rewrite")
	}
}

func TestGenerateRewriteDST(t *testing.T) {
	src := `package main
func sub() error { return nil } 
func sub2() (int, error) { return 0, nil } 
func tail() error { sub(); return nil } 
func loop() error { for { sub(); break }; return nil } 
func noerr() { sub() } 
`
	inj, dstFile, astFile := setupInjectorTest(t, src)
	origAssign := generateAssignmentDSTFunc
	origResolve := resolveErrorVarFunc
	t.Cleanup(func() {
		generateAssignmentDSTFunc = origAssign
		resolveErrorVarFunc = origResolve
	})

	// Early return when signature doesn't support error
	if stmts, err := inj.generateRewriteDST(analysis.InjectionPoint{}, &dst.ExprStmt{}, nil, nil, nil, true, true, nil); err != nil || len(stmts) != 0 {
		t.Fatal("expected no rewrite for non-error signature")
	}

	// Error when no call in statement
	ptTail := findPointCtx(t, astFile, "tail", "sub")
	resStmt, _ := FindDstNode(inj.Fset, dstFile, astFile, ptTail.Stmt)
	if _, err := inj.generateRewriteDST(ptTail, &dst.ReturnStmt{}, nil, types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", types.Universe.Lookup("error").Type())), false), nil, true, true, dstFile); err == nil {
		t.Fatal("expected error for missing call")
	}

	// Tail optimization
	resCall, _ := FindDstNode(inj.Fset, dstFile, astFile, ptTail.Call)
	ctx := inj.getEnclosingContext(ptTail)
	stmts, err := inj.generateRewriteDST(ptTail, resStmt.Node.(dst.Stmt), resCall.Node.(*dst.CallExpr), ctx.sig, ctx.decl, true, true, dstFile)
	if err != nil || len(stmts) != 1 {
		t.Fatalf("expected tail optimization, err=%v", err)
	}
	if _, ok := stmts[0].(*dst.ReturnStmt); !ok {
		t.Fatal("expected return stmt for tail optimization")
	}

	// Inside loop should avoid optimization
	ptLoop := findPointCtx(t, astFile, "loop", "sub")
	resStmt, _ = FindDstNode(inj.Fset, dstFile, astFile, ptLoop.Stmt)
	resCall, _ = FindDstNode(inj.Fset, dstFile, astFile, ptLoop.Call)
	ctx = inj.getEnclosingContext(ptLoop)
	stmts, err = inj.generateRewriteDST(ptLoop, resStmt.Node.(dst.Stmt), resCall.Node.(*dst.CallExpr), ctx.sig, ctx.decl, true, true, dstFile)
	if err != nil || len(stmts) == 0 {
		t.Fatalf("expected rewrite in loop, err=%v", err)
	}
	if _, ok := stmts[0].(*dst.ReturnStmt); ok {
		t.Fatal("did not expect tail return inside loop")
	}

	// TestParam path
	inj.TestParam = "t"
	stmts, err = inj.generateRewriteDST(ptTail, resStmt.Node.(dst.Stmt), resCall.Node.(*dst.CallExpr), ctx.sig, ctx.decl, false, false, dstFile)
	if err != nil || len(stmts) == 0 {
		t.Fatal("expected rewrite with TestParam")
	}
	inj.TestParam = ""

	// RenderTemplate error
	inj.ErrorTemplate = "{{"
	if _, err := inj.generateRewriteDST(ptTail, resStmt.Node.(dst.Stmt), resCall.Node.(*dst.CallExpr), ctx.sig, ctx.decl, true, false, dstFile); err == nil {
		t.Fatal("expected template error")
	}
	inj.ErrorTemplate = ""

	// declStmt via hook
	resolveErrorVarFunc = func(_ *Injector, _ analysis.InjectionPoint, _ *types.Scope) (string, token.Token, *dst.DeclStmt) {
		return "err", token.DEFINE, &dst.DeclStmt{Decl: &dst.GenDecl{Tok: token.VAR}}
	}
	stmts, err = inj.generateRewriteDST(ptTail, resStmt.Node.(dst.Stmt), resCall.Node.(*dst.CallExpr), ctx.sig, ctx.decl, false, false, dstFile)
	if err != nil || len(stmts) < 2 {
		t.Fatal("expected declStmt in rewrite")
	}
	resolveErrorVarFunc = origResolve

	// Non-Assign branch via hook
	generateAssignmentDSTFunc = func(_ *Injector, _ analysis.InjectionPoint, _ *dst.CallExpr, _ string, _ token.Token) (dst.Stmt, error) {
		return &dst.ExprStmt{X: dst.NewIdent("noop")}, nil
	}
	stmts, err = inj.generateRewriteDST(ptTail, resStmt.Node.(dst.Stmt), resCall.Node.(*dst.CallExpr), ctx.sig, ctx.decl, true, false, dstFile)
	if err != nil || len(stmts) < 2 {
		t.Fatal("expected non-assign path")
	}
	generateAssignmentDSTFunc = origAssign

	// generateAssignmentDST error via hook
	generateAssignmentDSTFunc = func(_ *Injector, _ analysis.InjectionPoint, _ *dst.CallExpr, _ string, _ token.Token) (dst.Stmt, error) {
		return nil, errors.New("boom")
	}
	if _, err := inj.generateRewriteDST(ptTail, resStmt.Node.(dst.Stmt), resCall.Node.(*dst.CallExpr), ctx.sig, ctx.decl, true, false, dstFile); err == nil {
		t.Fatal("expected assignment error")
	}
	generateAssignmentDSTFunc = origAssign

	// generateZeroReturns error branch
	badSig := types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(
		types.NewVar(token.NoPos, nil, "", types.NewTuple(types.NewVar(token.NoPos, nil, "", types.Typ[types.Int]))),
		types.NewVar(token.NoPos, nil, "", types.Universe.Lookup("error").Type()),
	), false)
	if _, err := inj.generateRewriteDST(ptTail, resStmt.Node.(dst.Stmt), resCall.Node.(*dst.CallExpr), badSig, nil, true, false, dstFile); err == nil {
		t.Fatal("expected generateZeroReturns error")
	}
}

func TestGenerateGoRewriteDST(t *testing.T) {
	src := `package main
func task() error { return nil } 
func main() { go task() } 
`
	inj, dstFile, astFile := setupInjectorTest(t, src)
	pt := findPointCtx(t, astFile, "main", "task")
	resStmt, _ := FindDstNode(inj.Fset, dstFile, astFile, pt.Stmt)
	goStmt := resStmt.Node.(*dst.GoStmt)

	// IMPORTANT: Resolve the call node before generating the rewrite.
	// generateGoRewriteDST uses addImportDST internally which mutates dstFile.Decls,
	// potentially invalidating AST-based index lookups used by FindDstNode if done afterwards.
	resCall, err := FindDstNode(inj.Fset, dstFile, astFile, pt.Call)
	if err != nil {
		t.Fatalf("findDstNode error: %v", err)
	}
	if resCall.Node == nil {
		t.Fatalf("findDstNode failed to return node result")
	}

	if _, err := inj.generateGoRewriteDST(pt, goStmt, nil, dstFile); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := inj.generateGoRewriteDST(pt, goStmt, resCall.Node.(*dst.CallExpr), dstFile); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	origAssign := generateAssignmentDSTFunc
	t.Cleanup(func() { generateAssignmentDSTFunc = origAssign })
	generateAssignmentDSTFunc = func(_ *Injector, _ analysis.InjectionPoint, _ *dst.CallExpr, _ string, _ token.Token) (dst.Stmt, error) {
		return nil, errors.New("boom")
	}
	if _, err := inj.generateGoRewriteDST(pt, goStmt, resCall.Node.(*dst.CallExpr), dstFile); err == nil {
		t.Fatal("expected assignment error")
	}
}

func TestTryLiftControls(t *testing.T) {
	src := `package main
func fail() error { return nil } 
func initCall() error { return nil } 
func iface() interface{} { return nil } 
func runIf() error { if x := fail(); x != nil { return nil }; return nil } 
func runIfCond() error { if fail() != nil { return nil }; return nil } 
func runSwitch() error { switch x := fail(); x { default: }; return nil } 
func runSwitchTag() error { switch fail() { default: }; return nil } 
func runType() error { switch v := iface().(type) { default: _ = v }; return nil } 
func runTypeExpr() error { switch iface().(type) { default: }; return nil } 
func runTypeInit() error { switch initCall(); v := iface().(type) { default: _ = v }; return nil } 
func runTypeInitAssign() error { switch v := initCall(); x := iface().(type) { default: _ = v; _ = x }; return nil } 
`
	inj, dstFile, astFile := setupInjectorTest(t, src)

	// If init
	pt := findPointCtx(t, astFile, "runIf", "fail")
	resStmt, _ := FindDstNode(inj.Fset, dstFile, astFile, pt.Stmt)
	resCall, _ := FindDstNode(inj.Fset, dstFile, astFile, pt.Call)
	entry := targetEntry{point: pt, dstStmt: resStmt.Node.(dst.Stmt), dstCall: resCall.Node.(*dst.CallExpr)}
	if handled, _, err := inj.tryLiftIf(resStmt.Node.(*dst.IfStmt), entry, astFile, dstFile); err != nil || !handled {
		t.Fatal("expected if init lift")
	}

	// If cond
	pt = findPointCtx(t, astFile, "runIfCond", "fail")
	resStmt, _ = FindDstNode(inj.Fset, dstFile, astFile, pt.Stmt)
	resCall, _ = FindDstNode(inj.Fset, dstFile, astFile, pt.Call)
	entry = targetEntry{point: pt, dstStmt: resStmt.Node.(dst.Stmt), dstCall: resCall.Node.(*dst.CallExpr)}
	if handled, _, err := inj.tryLiftIf(resStmt.Node.(*dst.IfStmt), entry, astFile, dstFile); err != nil || !handled {
		t.Fatal("expected if cond lift")
	}

	// Switch init
	pt = findPointCtx(t, astFile, "runSwitch", "fail")
	resStmt, _ = FindDstNode(inj.Fset, dstFile, astFile, pt.Stmt)
	resCall, _ = FindDstNode(inj.Fset, dstFile, astFile, pt.Call)
	entry = targetEntry{point: pt, dstStmt: resStmt.Node.(dst.Stmt), dstCall: resCall.Node.(*dst.CallExpr)}
	if handled, _, err := inj.tryLiftSwitch(resStmt.Node.(*dst.SwitchStmt), entry, astFile, dstFile); err != nil || !handled {
		t.Fatal("expected switch init lift")
	}

	// Switch tag
	pt = findPointCtx(t, astFile, "runSwitchTag", "fail")
	resStmt, _ = FindDstNode(inj.Fset, dstFile, astFile, pt.Stmt)
	resCall, _ = FindDstNode(inj.Fset, dstFile, astFile, pt.Call)
	entry = targetEntry{point: pt, dstStmt: resStmt.Node.(dst.Stmt), dstCall: resCall.Node.(*dst.CallExpr)}
	if handled, _, err := inj.tryLiftSwitch(resStmt.Node.(*dst.SwitchStmt), entry, astFile, dstFile); err != nil || !handled {
		t.Fatal("expected switch tag lift")
	}

	// Type switch assign
	pt = findPointCtx(t, astFile, "runType", "iface")
	resStmt, _ = FindDstNode(inj.Fset, dstFile, astFile, pt.Stmt)
	resCall, _ = FindDstNode(inj.Fset, dstFile, astFile, pt.Call)
	entry = targetEntry{point: pt, dstStmt: resStmt.Node.(dst.Stmt), dstCall: resCall.Node.(*dst.CallExpr)}
	if handled, _, err := inj.tryLiftTypeSwitch(resStmt.Node.(*dst.TypeSwitchStmt), entry, astFile, dstFile); err != nil || !handled {
		t.Fatal("expected type switch assign lift")
	}

	// Type switch expr
	pt = findPointCtx(t, astFile, "runTypeExpr", "iface")
	resStmt, _ = FindDstNode(inj.Fset, dstFile, astFile, pt.Stmt)
	resCall, _ = FindDstNode(inj.Fset, dstFile, astFile, pt.Call)
	entry = targetEntry{point: pt, dstStmt: resStmt.Node.(dst.Stmt), dstCall: resCall.Node.(*dst.CallExpr)}
	if handled, _, err := inj.tryLiftTypeSwitch(resStmt.Node.(*dst.TypeSwitchStmt), entry, astFile, dstFile); err != nil || !handled {
		t.Fatal("expected type switch expr lift")
	}

	// Type switch init
	pt = findPointCtx(t, astFile, "runTypeInit", "initCall")
	resStmt, _ = FindDstNode(inj.Fset, dstFile, astFile, pt.Stmt)
	resCall, _ = FindDstNode(inj.Fset, dstFile, astFile, pt.Call)
	entry = targetEntry{point: pt, dstStmt: resStmt.Node.(dst.Stmt), dstCall: resCall.Node.(*dst.CallExpr)}
	if handled, _, err := inj.tryLiftTypeSwitch(resStmt.Node.(*dst.TypeSwitchStmt), entry, astFile, dstFile); err != nil || !handled {
		t.Fatal("expected type switch init lift")
	}

	// Type switch init assign
	pt = findPointCtx(t, astFile, "runTypeInitAssign", "initCall")
	resStmt, _ = FindDstNode(inj.Fset, dstFile, astFile, pt.Stmt)
	resCall, _ = FindDstNode(inj.Fset, dstFile, astFile, pt.Call)
	entry = targetEntry{point: pt, dstStmt: resStmt.Node.(dst.Stmt), dstCall: resCall.Node.(*dst.CallExpr)}
	if handled, _, err := inj.tryLiftTypeSwitch(resStmt.Node.(*dst.TypeSwitchStmt), entry, astFile, dstFile); err != nil || !handled {
		t.Fatal("expected type switch init assign lift")
	}

	// Early returns
	if handled, _, err := inj.tryLiftIf(&dst.IfStmt{}, targetEntry{}, astFile, dstFile); err != nil || handled {
		t.Fatal("expected no lift for empty if entry")
	}
	if handled, _, err := inj.tryLiftSwitch(&dst.SwitchStmt{}, targetEntry{}, astFile, dstFile); err != nil || handled {
		t.Fatal("expected no lift for empty switch entry")
	}
	if handled, _, err := inj.tryLiftTypeSwitch(&dst.TypeSwitchStmt{}, targetEntry{}, astFile, dstFile); err != nil || handled {
		t.Fatal("expected no lift for empty type switch entry")
	}
}

func TestLiftTypeSwitchAssign_Errors(t *testing.T) {
	inj := &Injector{}
	if _, err := inj.liftTypeSwitchAssign(&dst.TypeSwitchStmt{}, targetEntry{}, nil, nil); err == nil {
		t.Fatal("expected error for nil dstCall")
	}
	// Force fallback inspect path (assign not a type assert)
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "main.go", "package main", 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	inj.Pkg = &packages.Package{TypesInfo: &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Scopes: map[ast.Node]*types.Scope{}}}
	entry := targetEntry{point: analysis.InjectionPoint{File: astFile, Pos: astFile.Pos()}, dstCall: &dst.CallExpr{Fun: dst.NewIdent("iface")}}
	ts := &dst.TypeSwitchStmt{Assign: &dst.ExprStmt{X: dst.NewIdent("x")}}
	if _, err := inj.liftTypeSwitchAssign(ts, entry, astFile, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLiftControlInitAndExpr_Errors(t *testing.T) {
	inj := &Injector{}
	if _, err := inj.liftControlInit(&dst.IfStmt{}, &dst.AssignStmt{}, targetEntry{}, nil, nil); err == nil {
		t.Fatal("expected error for nil dstCall")
	}

	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "main.go", "package main", 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	inj.Pkg = &packages.Package{TypesInfo: &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Scopes: map[ast.Node]*types.Scope{}}}
	entry := targetEntry{point: analysis.InjectionPoint{File: astFile, Pos: astFile.Pos()}}
	if _, err := inj.liftControlExpr(&dst.IfStmt{}, dst.NewIdent("x"), entry, "cond", astFile, nil, func(dst.Node, dst.Expr) {}); err == nil {
		t.Fatal("expected error for nil dstCall in cond")
	}
}

func TestLiftControlExpr_Tuple(t *testing.T) {
	src := `package main
func val() (int, error) { return 0, nil } 
func run() { _, _ = val() } 
`
	inj, dstFile, astFile := setupInjectorTest(t, src)
	pt := findPointCtx(t, astFile, "run", "val")
	resCall, _ := FindDstNode(inj.Fset, dstFile, astFile, pt.Call)
	entry := targetEntry{point: pt, dstCall: resCall.Node.(*dst.CallExpr)}
	ctrl := &dst.IfStmt{Cond: dst.NewIdent("cond"), Body: &dst.BlockStmt{}}
	inj.TestParam = "t"
	if _, err := inj.liftControlExpr(ctrl, ctrl.Cond, entry, "val", astFile, dstFile, func(n dst.Node, e dst.Expr) {
		n.(*dst.IfStmt).Cond = e
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	inj.TestParam = ""
}

func TestLiftCompositeLit_ErrorsAndAssign(t *testing.T) {
	src := `package main
func fail() error { return nil } 
type S struct { F error } 
func run() error { x := S{ F: fail() }; _ = x; return nil } 
`
	build := func() (*Injector, dst.Stmt, targetEntry, *ast.File) {
		inj, dstFile, astFile := setupInjectorTest(t, src)
		pt := findPointCtx(t, astFile, "run", "fail")
		var assignStmt dst.Stmt
		dst.Inspect(dstFile, func(n dst.Node) bool {
			if a, ok := n.(*dst.AssignStmt); ok {
				if inj.extractDstCall(a) != nil {
					assignStmt = a
					return false
				}
			}
			return true
		})
		if assignStmt == nil {
			t.Fatal("expected dst assign stmt")
		}
		entry := targetEntry{point: pt, dstStmt: assignStmt}
		entry.dstCall = inj.extractDstCall(assignStmt)
		if entry.dstCall == nil {
			t.Fatal("expected dst call in composite")
		}
		return inj, assignStmt, entry, astFile
	}

	inj, assignStmt, entry, astFile := build()

	// normal path with TestParam
	inj.TestParam = "t"
	if _, err := inj.liftCompositeLit(assignStmt, entry, astFile, &dst.File{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	inj.TestParam = ""

	// nil dstCall
	if _, err := inj.liftCompositeLit(assignStmt, targetEntry{}, astFile, nil); err == nil {
		t.Fatal("expected error for nil dstCall")
	}

	// call not in composite -> error
	if _, err := inj.liftCompositeLit(&dst.ExprStmt{X: dst.NewIdent("x")}, entry, astFile, nil); err == nil {
		t.Fatal("expected error for missing call in composite")
	}

	// Force tok assign branch via hook
	inj2, assignStmt2, entry2, astFile2 := build()
	origResolve := resolveErrorVarFunc
	t.Cleanup(func() { resolveErrorVarFunc = origResolve })
	resolveErrorVarFunc = func(_ *Injector, _ analysis.InjectionPoint, _ *types.Scope) (string, token.Token, *dst.DeclStmt) {
		return "err", token.ASSIGN, nil
	}
	if _, err := inj2.liftCompositeLit(assignStmt2, entry2, astFile2, &dst.File{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resolveErrorVarFunc = origResolve

	// declStmt branch
	inj3, assignStmt3, entry3, astFile3 := build()
	resolveErrorVarFunc = func(_ *Injector, _ analysis.InjectionPoint, _ *types.Scope) (string, token.Token, *dst.DeclStmt) {
		return "err", token.DEFINE, &dst.DeclStmt{Decl: &dst.GenDecl{Tok: token.VAR}}
	}
	if _, err := inj3.liftCompositeLit(assignStmt3, entry3, astFile3, &dst.File{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resolveErrorVarFunc = origResolve
}

func TestLiftControlInit_GenerateRewriteError(t *testing.T) {
	src := `package main
func f() error { return nil } 
func run() error { if f(); true { return nil }; return nil } 
`
	inj, dstFile, astFile := setupInjectorTest(t, src)
	pt := findPointCtx(t, astFile, "run", "f")
	resStmt, _ := FindDstNode(inj.Fset, dstFile, astFile, pt.Stmt)
	if resStmt.Node == nil {
		t.Fatal("failed to find if stmt")
	}
	resCall, _ := FindDstNode(inj.Fset, dstFile, astFile, pt.Call)
	if resCall.Node == nil {
		t.Fatal("failed to find call")
	}
	entry := targetEntry{point: pt, dstCall: resCall.Node.(*dst.CallExpr)}

	origAssign := generateAssignmentDSTFunc
	t.Cleanup(func() { generateAssignmentDSTFunc = origAssign })
	generateAssignmentDSTFunc = func(_ *Injector, _ analysis.InjectionPoint, _ *dst.CallExpr, _ string, _ token.Token) (dst.Stmt, error) {
		return nil, errors.New("boom")
	}
	if _, err := inj.liftControlInit(resStmt.Node.(dst.Stmt), resStmt.Node.(*dst.IfStmt).Init.(dst.Stmt), entry, astFile, dstFile); err == nil {
		t.Fatal("expected liftControlInit error")
	}
}

func TestLiftControlExpr_GenerateZeroReturnsError(t *testing.T) {
	src := `package main
func cond() bool { return true } 
func run() (int, error) { 
  if cond() { return 0, nil } 
  return 0, nil
} 
`
	inj, dstFile, astFile := setupInjectorTest(t, src)
	var runDecl *ast.FuncDecl
	ast.Inspect(astFile, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok && fn.Name.Name == "run" {
			runDecl = fn
			return false
		}
		return true
	})
	if runDecl == nil {
		t.Fatal("expected run decl")
	}
	badSig := types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(
		types.NewVar(token.NoPos, nil, "", types.NewTuple(types.NewVar(token.NoPos, nil, "", types.Typ[types.Int]))),
		types.NewVar(token.NoPos, nil, "", types.Universe.Lookup("error").Type()),
	), false)
	inj.Pkg.TypesInfo.Defs[runDecl.Name] = types.NewFunc(token.NoPos, nil, "run", badSig)

	pt := findPointCtx(t, astFile, "run", "cond")
	resStmt, _ := FindDstNode(inj.Fset, dstFile, astFile, pt.Stmt)
	resCall, _ := FindDstNode(inj.Fset, dstFile, astFile, pt.Call)
	entry := targetEntry{point: pt, dstCall: resCall.Node.(*dst.CallExpr)}
	ctrl := resStmt.Node.(*dst.IfStmt)
	if _, err := inj.liftControlExpr(ctrl, ctrl.Cond, entry, "cond", astFile, dstFile, func(n dst.Node, e dst.Expr) {
		n.(*dst.IfStmt).Cond = e
	}); err == nil {
		t.Fatal("expected generateZeroReturns error")
	}
}

func TestLiftControlExpr_RenderTemplateError(t *testing.T) {
	src := `package main
func cond() bool { return true } 
func run() (int, error) { 
  if cond() { return 0, nil } 
  return 0, nil
} 
`
	inj, dstFile, astFile := setupInjectorTest(t, src)
	inj.ErrorTemplate = "{{"
	pt := findPointCtx(t, astFile, "run", "cond")
	resStmt, _ := FindDstNode(inj.Fset, dstFile, astFile, pt.Stmt)
	resCall, _ := FindDstNode(inj.Fset, dstFile, astFile, pt.Call)
	entry := targetEntry{point: pt, dstCall: resCall.Node.(*dst.CallExpr)}
	ctrl := resStmt.Node.(*dst.IfStmt)
	if _, err := inj.liftControlExpr(ctrl, ctrl.Cond, entry, "cond", astFile, dstFile, func(n dst.Node, e dst.Expr) {
		n.(*dst.IfStmt).Cond = e
	}); err == nil {
		t.Fatal("expected template error")
	}
}

func TestLiftCompositeLit_NonKeyed(t *testing.T) {
	src := `package main
func fail() error { return nil } 
type S struct { F error } 
func run() error { x := S{ fail() }; _ = x; return nil } 
`
	inj, dstFile, astFile := setupInjectorTest(t, src)
	pt := findPointCtx(t, astFile, "run", "fail")
	var assignStmt dst.Stmt
	dst.Inspect(dstFile, func(n dst.Node) bool {
		if a, ok := n.(*dst.AssignStmt); ok {
			if inj.extractDstCall(a) != nil {
				assignStmt = a
				return false
			}
		}
		return true
	})
	if assignStmt == nil {
		t.Fatal("expected dst assign stmt")
	}
	entry := targetEntry{point: pt, dstStmt: assignStmt}
	entry.dstCall = inj.extractDstCall(assignStmt)
	if entry.dstCall == nil {
		t.Fatal("expected dst call in composite")
	}
	if _, err := inj.liftCompositeLit(assignStmt, entry, astFile, dstFile); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
