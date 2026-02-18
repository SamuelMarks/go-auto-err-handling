package rewrite

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
	"github.com/dave/dst/dstutil"
)

func TestHasAnonymousReturnsDST(t *testing.T) {
	if hasAnonymousReturnsDST(&dst.FuncType{}) {
		t.Fatal("expected false for nil results")
	}
	withAnon := &dst.FuncType{Results: &dst.FieldList{List: []*dst.Field{{Type: dst.NewIdent("int")}}}}
	if !hasAnonymousReturnsDST(withAnon) {
		t.Fatal("expected anonymous returns")
	}
	withNames := &dst.FuncType{Results: &dst.FieldList{List: []*dst.Field{{Names: []*dst.Ident{dst.NewIdent("v")}, Type: dst.NewIdent("int")}}}}
	if hasAnonymousReturnsDST(withNames) {
		t.Fatal("expected named returns")
	}
}

func TestGetErrorReturnNameDST(t *testing.T) {
	if got := (&Injector{}).getErrorReturnNameDST(&dst.FuncType{}); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	ft := &dst.FuncType{Results: &dst.FieldList{List: []*dst.Field{
		{Names: []*dst.Ident{dst.NewIdent("e1")}, Type: dst.NewIdent("error")},
		{Names: []*dst.Ident{dst.NewIdent("err")}, Type: dst.NewIdent("error")},
	}}}
	if got := (&Injector{}).getErrorReturnNameDST(ft); got != "err" {
		t.Fatalf("expected err, got %q", got)
	}
	ft2 := &dst.FuncType{Results: &dst.FieldList{List: []*dst.Field{
		{Names: []*dst.Ident{dst.NewIdent("e1")}, Type: dst.NewIdent("error")},
		{Names: []*dst.Ident{dst.NewIdent("e2")}, Type: dst.NewIdent("error")},
	}}}
	if got := (&Injector{}).getErrorReturnNameDST(ft2); got != "e2" {
		t.Fatalf("expected last error name, got %q", got)
	}
}

func TestIsErrorDstExpr(t *testing.T) {
	if !isErrorDstExpr(dst.NewIdent("error")) {
		t.Fatal("expected error ident")
	}
	if isErrorDstExpr(dst.NewIdent("int")) {
		t.Fatal("unexpected error ident")
	}
	if isErrorDstExpr(&dst.SelectorExpr{X: dst.NewIdent("pkg"), Sel: dst.NewIdent("Err")}) {
		t.Fatal("expected false for non-ident")
	}
}

func TestNormalizeToNakedReturns_Edges(t *testing.T) {
	inj := &Injector{}
	inj.normalizeToNakedReturns(nil, nil)

	// No names -> no change
	body := &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{Results: []dst.Expr{dst.NewIdent("x")}}}}
	results := &dst.FieldList{List: []*dst.Field{{Type: dst.NewIdent("int")}}}
	inj.normalizeToNakedReturns(body, results)
	if ret, ok := body.List[0].(*dst.ReturnStmt); !ok || len(ret.Results) == 0 {
		t.Fatal("expected explicit return to remain")
	}

	// Naked return -> no change
	body2 := &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{}}}
	results2 := &dst.FieldList{List: []*dst.Field{{Names: []*dst.Ident{dst.NewIdent("r")}, Type: dst.NewIdent("int")}}}
	inj.normalizeToNakedReturns(body2, results2)
	if ret, ok := body2.List[0].(*dst.ReturnStmt); !ok || len(ret.Results) != 0 {
		t.Fatal("expected naked return to remain")
	}

	// Nested FuncLit should be skipped
	innerRet := &dst.ReturnStmt{Results: []dst.Expr{dst.NewIdent("x")}}
	inner := &dst.FuncLit{Body: &dst.BlockStmt{List: []dst.Stmt{innerRet}}}
	body3 := &dst.BlockStmt{List: []dst.Stmt{&dst.ExprStmt{X: inner}}}
	inj.normalizeToNakedReturns(body3, results2)
	if len(innerRet.Results) == 0 {
		t.Fatal("expected inner return to remain explicit")
	}
}

func TestIsErrorReturningCall(t *testing.T) {
	src := `package main
func errFunc() error { return nil }
func intFunc() int { return 0 }
func main() { errFunc(); intFunc() }
`
	inj, _, astFile := setupInjectorTest(t, src)

	var errCall, intCall *ast.CallExpr
	ast.Inspect(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok {
				switch id.Name {
				case "errFunc":
					errCall = c
				case "intFunc":
					intCall = c
				}
			}
		}
		return true
	})
	if errCall == nil || intCall == nil {
		t.Fatal("expected calls")
	}

	if !inj.isErrorReturningCall(errCall) {
		t.Fatal("expected error-returning call")
	}
	if inj.isErrorReturningCall(intCall) {
		t.Fatal("expected non-error call")
	}

	inj.Pkg.TypesInfo = nil
	if inj.isErrorReturningCall(errCall) {
		t.Fatal("expected false with missing types info")
	}

	// Missing type info for call
	inj.Pkg.TypesInfo = &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}
	if inj.isErrorReturningCall(&ast.CallExpr{Fun: ast.NewIdent("missing")}) {
		t.Fatal("expected false for missing call type info")
	}
}

func TestRewriteDefersInDST_Skips(t *testing.T) {
	src := `package main
func Close() error { return nil }
func Do() (err error) {
  defer Close()
  return nil
}
`
	inj, astFile, dstFile := setupDstEnv(t, src, false)

	var astDefer *ast.DeferStmt
	ast.Inspect(astFile, func(n ast.Node) bool {
		if d, ok := n.(*ast.DeferStmt); ok {
			astDefer = d
			return false
		}
		return true
	})
	if astDefer == nil {
		t.Fatal("expected defer stmt")
	}

	// Find the function body in DST for calling rewriteDefersInDST
	var dstBody *dst.BlockStmt
	dst.Inspect(dstFile, func(n dst.Node) bool {
		if fn, ok := n.(*dst.FuncDecl); ok {
			dstBody = fn.Body
			return false
		}
		return true
	})
	if dstBody == nil {
		t.Fatal("expected dst body")
	}

	// 1) FindDstNode error path (defer not in file)
	bogus := &ast.DeferStmt{Call: &ast.CallExpr{Fun: ast.NewIdent("bogus")}}
	_ = inj.rewriteDefersInDST(dstBody, []*ast.DeferStmt{bogus}, astFile, dstFile, "err")

	// 2) Non-defer mapping path
	// Replace the defer stmt with a different node to force type mismatch
	dstutil.Apply(dstBody, func(c *dstutil.Cursor) bool {
		if _, ok := c.Node().(*dst.DeferStmt); ok {
			c.Replace(&dst.ExprStmt{X: dst.NewIdent("noop")})
			return false
		}
		return true
	}, nil)

	_ = inj.rewriteDefersInDST(dstBody, []*ast.DeferStmt{astDefer}, astFile, dstFile, "err")
}

func TestRewriteDefers_Branches(t *testing.T) {
	// FindDstNode error for FuncDecl
	src := `package main
func Close() error { return nil }
func Do() (err error) { defer Close(); return nil }
`
	inj, astFile, _ := setupDstEnv(t, src, false)
	fset := inj.Fset
	mismatchAst, err := parser.ParseFile(fset, "other.go", "package main", 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mismatchDst, err := decorator.NewDecorator(fset).DecorateFile(mismatchAst)
	if err != nil {
		t.Fatalf("decorate: %v", err)
	}
	if _, err := inj.RewriteDefers(mismatchDst, astFile); err == nil {
		t.Fatal("expected FindDstNode error for func decl")
	}

	// dstDecl not ok
	inj2, astFile2, dstFile2 := setupDstEnv(t, src, false)
	replaced := false
	for idx, decl := range dstFile2.Decls {
		if fn, ok := decl.(*dst.FuncDecl); ok && fn.Name.Name == "Do" {
			dstFile2.Decls[idx] = &dst.GenDecl{Tok: token.VAR}
			replaced = true
			break
		}
	}
	if !replaced {
		t.Fatal("expected to find Do decl")
	}
	if _, err := inj2.RewriteDefers(dstFile2, astFile2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// errName == "" branch
	srcNoErr := `package main
func Close() error { return nil }
func Do() int { defer Close(); return 1 }
`
	inj3, astFile3, dstFile3 := setupDstEnv(t, srcNoErr, false)
	if _, err := inj3.RewriteDefers(dstFile3, astFile3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	foundFuncLit := false
	dst.Inspect(dstFile3, func(n dst.Node) bool {
		if d, ok := n.(*dst.DeferStmt); ok {
			if _, ok := d.Call.Fun.(*dst.FuncLit); ok {
				foundFuncLit = true
				return false
			}
		}
		return true
	})
	if foundFuncLit {
		t.Fatal("expected defer to remain a direct call when no error return")
	}

	// FuncLit errName == "" branch
	srcLitNoErr := `package main
func Close() error { return nil }
var _ = func() int { defer Close(); return 1 }
`
	inj3b, astFile3b, dstFile3b := setupDstEnv(t, srcLitNoErr, false)
	if _, err := inj3b.RewriteDefers(dstFile3b, astFile3b); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	foundFuncLit = false
	dst.Inspect(dstFile3b, func(n dst.Node) bool {
		if d, ok := n.(*dst.DeferStmt); ok {
			if _, ok := d.Call.Fun.(*dst.FuncLit); ok {
				foundFuncLit = true
				return false
			}
		}
		return true
	})
	if foundFuncLit {
		t.Fatal("expected func lit defer to remain direct without error return")
	}

	// FuncLit changed branch
	srcLit := `package main
func Close() error { return nil }
var _ = func() (int, error) { defer Close(); return 1, nil }
`
	inj4, astFile4, dstFile4 := setupDstEnv(t, srcLit, false)
	if changed, err := inj4.RewriteDefers(dstFile4, astFile4); err != nil || !changed {
		t.Fatal("expected changes for func lit")
	}

	// FuncLit dstLit not ok
	inj5, astFile5, dstFile5 := setupDstEnv(t, srcLit, false)
	origFind := findDstNodeFunc
	findDstNodeFunc = func(_ *token.FileSet, _ *dst.File, _ *ast.File, _ ast.Node) (DstMapResult, error) {
		return DstMapResult{Node: &dst.GenDecl{}}, nil
	}
	if _, err := inj5.RewriteDefers(dstFile5, astFile5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	findDstNodeFunc = origFind

	// FindDstNode error for FuncLit
	inj6, astFile6, _ := setupDstEnv(t, srcLit, false)
	mismatchDst2, err := decorator.NewDecorator(inj6.Fset).DecorateFile(mismatchAst)
	if err != nil {
		t.Fatalf("decorate: %v", err)
	}
	if _, err := inj6.RewriteDefers(mismatchDst2, astFile6); err == nil {
		t.Fatal("expected FindDstNode error for func lit")
	}

	// EnsureNamedReturnsDST error for FuncDecl
	inj7, astFile7, dstFile7 := setupDstEnv(t, src, false)
	origEnsure := ensureNamedReturnsDST
	t.Cleanup(func() { ensureNamedReturnsDST = origEnsure })
	ensureNamedReturnsDST = func(_ *dst.FuncDecl) (bool, error) {
		return false, fmt.Errorf("boom")
	}
	if _, err := inj7.RewriteDefers(dstFile7, astFile7); err == nil {
		t.Fatal("expected ensureNamedReturnsDST error")
	}
	ensureNamedReturnsDST = origEnsure

	// EnsureNamedReturnsDST error for FuncLit
	inj8, astFile8, dstFile8 := setupDstEnv(t, srcLit, false)
	ensureNamedReturnsDST = func(_ *dst.FuncDecl) (bool, error) {
		return false, fmt.Errorf("boom")
	}
	if _, err := inj8.RewriteDefers(dstFile8, astFile8); err == nil {
		t.Fatal("expected ensureNamedReturnsDST error for func lit")
	}
	ensureNamedReturnsDST = origEnsure
}
