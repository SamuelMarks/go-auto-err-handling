package rewrite

import (
	"errors"
	"go/ast"
	"go/token"
	"go/types"
	"testing"

	"github.com/dave/dst"
	"golang.org/x/tools/go/packages"
)

func TestIsTerminating_Cases(t *testing.T) {
	inj := &Injector{}
	cases := []struct {
		name string
		stmt dst.Stmt
		want bool
	}{
		{"return", &dst.ReturnStmt{}, true},
		{"block-empty", &dst.BlockStmt{}, false},
		{"block-return", &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{}}}, true},
		{"if-no-else", &dst.IfStmt{Body: &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{}}}}, false},
		{"if-else", &dst.IfStmt{Body: &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{}}}, Else: &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{}}}}, true},
		{"for-infinite", &dst.ForStmt{Cond: nil}, true},
		{"for-conditional", &dst.ForStmt{Cond: dst.NewIdent("ok")}, false},
		{"switch-default", &dst.SwitchStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.CaseClause{List: []dst.Expr{dst.NewIdent("1")}, Body: []dst.Stmt{&dst.ReturnStmt{}}},
			&dst.CaseClause{List: nil, Body: []dst.Stmt{&dst.ReturnStmt{}}},
		}}}, true},
		{"switch-default-empty-case", &dst.SwitchStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.CaseClause{List: nil, Body: []dst.Stmt{&dst.ReturnStmt{}}},
			&dst.CaseClause{List: []dst.Expr{dst.NewIdent("2")}, Body: nil},
		}}}, false},
		{"switch-non-case", &dst.SwitchStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.EmptyStmt{},
		}}}, false},
		{"switch-case-non-term", &dst.SwitchStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.CaseClause{List: []dst.Expr{dst.NewIdent("1")}, Body: []dst.Stmt{&dst.ExprStmt{X: dst.NewIdent("x")}}},
			&dst.CaseClause{List: nil, Body: []dst.Stmt{&dst.ReturnStmt{}}},
		}}}, false},
		{"switch-empty-case", &dst.SwitchStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.CaseClause{List: []dst.Expr{dst.NewIdent("1")}, Body: nil},
		}}}, false},
		{"select-default", &dst.SelectStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.CommClause{Comm: nil, Body: []dst.Stmt{&dst.ReturnStmt{}}},
		}}}, true},
		{"select-non-comm", &dst.SelectStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.EmptyStmt{},
		}}}, false},
		{"select-body-non-term", &dst.SelectStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.CommClause{Comm: nil, Body: []dst.Stmt{&dst.ExprStmt{X: dst.NewIdent("x")}}},
		}}}, false},
		{"select-empty", &dst.SelectStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.CommClause{Comm: nil, Body: nil},
		}}}, false},
		{"panic-expr", &dst.ExprStmt{X: &dst.CallExpr{Fun: dst.NewIdent("panic")}}, true},
		{"expr-false", &dst.ExprStmt{X: dst.NewIdent("x")}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inj.isTerminating(tc.stmt); got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func TestRewritePanics_Branches(t *testing.T) {
	t.Run("funcLitSkip", func(t *testing.T) {
		src := `package main
func f() { func(){ panic("x") }() }
`
		inj, astFile, dstFile := setupDstEnv(t, src, false)
		changed, err := inj.RewritePanics(dstFile, astFile)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if changed {
			t.Fatal("expected no changes for closure panic")
		}
	})

	t.Run("funcMapErr", func(t *testing.T) {
		src := `package main
func f() { panic("x") }
`
		inj, astFile, dstFile := setupDstEnv(t, src, false)
		origFind := findDstNodeFunc
		t.Cleanup(func() { findDstNodeFunc = origFind })
		findDstNodeFunc = func(fset *token.FileSet, df *dst.File, af *ast.File, node ast.Node) (DstMapResult, error) {
			if _, ok := node.(*ast.FuncDecl); ok {
				return DstMapResult{}, errors.New("boom")
			}
			return origFind(fset, df, af, node)
		}
		if _, err := inj.RewritePanics(dstFile, astFile); err == nil {
			t.Fatal("expected func mapping error")
		}
	})

	t.Run("funcMapNotOk", func(t *testing.T) {
		src := `package main
func f() { panic("x") }
`
		inj, astFile, dstFile := setupDstEnv(t, src, false)
		origFind := findDstNodeFunc
		t.Cleanup(func() { findDstNodeFunc = origFind })
		findDstNodeFunc = func(fset *token.FileSet, df *dst.File, af *ast.File, node ast.Node) (DstMapResult, error) {
			if _, ok := node.(*ast.FuncDecl); ok {
				return DstMapResult{Node: &dst.GenDecl{}}, nil
			}
			return origFind(fset, df, af, node)
		}
		changed, err := inj.RewritePanics(dstFile, astFile)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if changed {
			t.Fatal("expected no changes for non-func mapping")
		}
	})

	t.Run("addSigErr", func(t *testing.T) {
		src := `package main
func f() { panic("x") }
`
		inj, astFile, dstFile := setupDstEnv(t, src, false)
		origAdd := addErrorToSignatureDST
		t.Cleanup(func() { addErrorToSignatureDST = origAdd })
		addErrorToSignatureDST = func(*dst.FuncDecl) (bool, error) {
			return false, errors.New("boom")
		}
		if _, err := inj.RewritePanics(dstFile, astFile); err == nil {
			t.Fatal("expected signature error")
		}
	})

	t.Run("panicMapErr", func(t *testing.T) {
		src := `package main
func f() { panic("x") }
`
		inj, astFile, dstFile := setupDstEnv(t, src, false)
		origFind := findDstNodeFunc
		t.Cleanup(func() { findDstNodeFunc = origFind })
		findDstNodeFunc = func(fset *token.FileSet, df *dst.File, af *ast.File, node ast.Node) (DstMapResult, error) {
			if call, ok := node.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "panic" {
					return DstMapResult{}, errors.New("boom")
				}
			}
			return origFind(fset, df, af, node)
		}
		if _, err := inj.RewritePanics(dstFile, astFile); err == nil {
			t.Fatal("expected panic mapping error")
		}
	})

	t.Run("panicMapNotCall", func(t *testing.T) {
		src := `package main
func f() error { panic("x") }
`
		inj, astFile, dstFile := setupDstEnv(t, src, false)
		origFind := findDstNodeFunc
		t.Cleanup(func() { findDstNodeFunc = origFind })
		findDstNodeFunc = func(fset *token.FileSet, df *dst.File, af *ast.File, node ast.Node) (DstMapResult, error) {
			if call, ok := node.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "panic" {
					return DstMapResult{Node: &dst.GenDecl{}}, nil
				}
			}
			return origFind(fset, df, af, node)
		}
		changed, err := inj.RewritePanics(dstFile, astFile)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if changed {
			t.Fatal("expected no changes for non-call mapping")
		}
	})

	t.Run("ensureTerminalErr", func(t *testing.T) {
		src := `package main
func f() { panic("x") }
`
		inj, astFile, dstFile := setupDstEnv(t, src, false)
		origEnsure := ensureTerminalReturnFunc
		t.Cleanup(func() { ensureTerminalReturnFunc = origEnsure })
		ensureTerminalReturnFunc = func(_ *Injector, _ *dst.FuncDecl, _ *ast.FuncDecl) error {
			return errors.New("boom")
		}
		if _, err := inj.RewritePanics(dstFile, astFile); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestEnsureTerminalReturn_Branches(t *testing.T) {
	inj := &Injector{}

	// Nil body
	if err := inj.ensureTerminalReturn(&dst.FuncDecl{Body: nil}, nil); err != nil {
		t.Fatalf("nil body: %v", err)
	}

	// Terminating body
	fnTerm := &dst.FuncDecl{Body: &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{}}}}
	if err := inj.ensureTerminalReturn(fnTerm, nil); err != nil {
		t.Fatalf("terminating: %v", err)
	}
	if len(fnTerm.Body.List) != 1 {
		t.Fatal("unexpected append on terminating body")
	}

	// No results
	fnNoRes := &dst.FuncDecl{Body: &dst.BlockStmt{List: nil}, Type: &dst.FuncType{Results: nil}}
	if err := inj.ensureTerminalReturn(fnNoRes, nil); err != nil {
		t.Fatalf("no results: %v", err)
	}
	if len(fnNoRes.Body.List) != 0 {
		t.Fatal("unexpected append on void fn")
	}

	// AST nil fallback -> generateZerosFromDST
	fnFallback := &dst.FuncDecl{Body: &dst.BlockStmt{List: nil}, Type: &dst.FuncType{Results: &dst.FieldList{List: []*dst.Field{
		{Type: dst.NewIdent("int")},
		{Type: dst.NewIdent("error")},
	}}}}
	if err := inj.ensureTerminalReturn(fnFallback, nil); err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if len(fnFallback.Body.List) != 1 {
		t.Fatal("expected appended return")
	}

	// TypesInfo nil -> use nil for ast types
	astFn := &ast.FuncDecl{Type: &ast.FuncType{Results: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("error")}}}}}
	fnNilTypes := &dst.FuncDecl{Body: &dst.BlockStmt{}, Type: &dst.FuncType{Results: &dst.FieldList{List: []*dst.Field{{Type: dst.NewIdent("error")}}}}}
	if err := inj.ensureTerminalReturn(fnNilTypes, astFn); err != nil {
		t.Fatalf("nil types: %v", err)
	}
	if len(fnNilTypes.Body.List) != 1 {
		t.Fatal("expected appended return")
	}

	// Force ZeroExprDST error -> guessZeroDSTFromExpr
	ident := ast.NewIdent("T")
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{ident: {Type: types.NewTuple(types.NewVar(token.NoPos, nil, "", types.Typ[types.Int]))}}}
	injErr := &Injector{Pkg: &packages.Package{TypesInfo: info}}
	astFnErr := &ast.FuncDecl{Type: &ast.FuncType{Results: &ast.FieldList{List: []*ast.Field{{Type: ident}}}}}
	fnErr := &dst.FuncDecl{Body: &dst.BlockStmt{}, Type: &dst.FuncType{Results: &dst.FieldList{List: []*dst.Field{{Type: dst.NewIdent("T")}}}}}
	if err := injErr.ensureTerminalReturn(fnErr, astFnErr); err != nil {
		t.Fatalf("error branch: %v", err)
	}
	ret, ok := fnErr.Body.List[len(fnErr.Body.List)-1].(*dst.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		t.Fatal("expected return with one result")
	}
	if lit, ok := ret.Results[0].(*dst.BasicLit); !ok || lit.Value != "0" {
		t.Fatalf("expected guessed zero literal, got %#v", ret.Results[0])
	}
}

func TestGuessZeroHelpers(t *testing.T) {
	if lit, ok := guessZeroDST(dst.NewIdent("int")).(*dst.BasicLit); !ok || lit.Value != "0" {
		t.Fatal("expected int zero")
	}
	if id, ok := guessZeroDST(dst.NewIdent("bool")).(*dst.Ident); !ok || id.Name != "false" {
		t.Fatal("expected bool zero")
	}
	if lit, ok := guessZeroDST(dst.NewIdent("string")).(*dst.BasicLit); !ok || lit.Value != `""` {
		t.Fatal("expected string zero")
	}
	if id, ok := guessZeroDST(&dst.StarExpr{X: dst.NewIdent("int")}).(*dst.Ident); !ok || id.Name != "nil" {
		t.Fatal("expected nil zero")
	}
	if id, ok := guessZeroDST(dst.NewIdent("custom")).(*dst.Ident); !ok || id.Name != "nil" {
		t.Fatal("expected default nil")
	}

	if lit, ok := guessZeroDSTFromExpr(ast.NewIdent("string")).(*dst.BasicLit); !ok || lit.Value != `""` {
		t.Fatal("expected string zero from expr")
	}
	if id, ok := guessZeroDSTFromExpr(ast.NewIdent("bool")).(*dst.Ident); !ok || id.Name != "false" {
		t.Fatal("expected bool zero from expr")
	}
	if lit, ok := guessZeroDSTFromExpr(ast.NewIdent("int")).(*dst.BasicLit); !ok || lit.Value != "0" {
		t.Fatal("expected int zero from expr")
	}
	if id, ok := guessZeroDSTFromExpr(&ast.StarExpr{X: ast.NewIdent("int")}).(*dst.Ident); !ok || id.Name != "nil" {
		t.Fatal("expected nil from expr")
	}
	if id, ok := guessZeroDSTFromExpr(&ast.SelectorExpr{X: ast.NewIdent("pkg"), Sel: ast.NewIdent("T")}).(*dst.Ident); !ok || id.Name != "nil" {
		t.Fatal("expected default nil from expr")
	}
}

func TestConvertPanicArgToErrorDST(t *testing.T) {
	// error type
	argErr := ast.NewIdent("errVar")
	infoErr := &types.Info{Types: map[ast.Expr]types.TypeAndValue{argErr: {Type: types.Universe.Lookup("error").Type()}}}
	injErr := &Injector{Pkg: &packages.Package{TypesInfo: infoErr}}
	res := injErr.convertPanicArgToErrorDST(dst.NewIdent("errVar"), argErr, &dst.File{})
	if _, ok := res.(*dst.Ident); !ok {
		t.Fatal("expected ident for error arg")
	}

	// string type
	argStr := ast.NewIdent("s")
	infoStr := &types.Info{Types: map[ast.Expr]types.TypeAndValue{argStr: {Type: types.Typ[types.String]}}}
	injStr := &Injector{Pkg: &packages.Package{TypesInfo: infoStr}}
	res = injStr.convertPanicArgToErrorDST(dst.NewIdent("s"), argStr, &dst.File{})
	if call, ok := res.(*dst.CallExpr); !ok || call.Args[0].(*dst.BasicLit).Value != `"%s"` {
		t.Fatalf("expected %%s format for string, got %#v", res)
	}

	// non-string type
	argInt := ast.NewIdent("i")
	infoInt := &types.Info{Types: map[ast.Expr]types.TypeAndValue{argInt: {Type: types.Typ[types.Int]}}}
	injInt := &Injector{Pkg: &packages.Package{TypesInfo: infoInt}}
	res = injInt.convertPanicArgToErrorDST(dst.NewIdent("i"), argInt, &dst.File{})
	if call, ok := res.(*dst.CallExpr); !ok || call.Args[0].(*dst.BasicLit).Value != `"%v"` {
		t.Fatalf("expected %%v format for non-string, got %#v", res)
	}

	// string literal without types info
	injNil := &Injector{}
	res = injNil.convertPanicArgToErrorDST(&dst.BasicLit{Kind: token.STRING, Value: `"hi"`}, &ast.BasicLit{Kind: token.STRING, Value: `"hi"`}, &dst.File{})
	if call, ok := res.(*dst.CallExpr); !ok || call.Args[0].(*dst.BasicLit).Value != `"%s"` {
		t.Fatalf("expected %%s format for string literal, got %#v", res)
	}
}

func TestPanicCallAndErrorArg(t *testing.T) {
	// TypesInfo nil
	inj := &Injector{Pkg: &packages.Package{}}
	panicCall := &ast.CallExpr{Fun: ast.NewIdent("panic")}
	if !inj.isPanicCall(panicCall) {
		t.Fatal("expected panic call")
	}
	if inj.isPanicCall(&ast.CallExpr{Fun: ast.NewIdent("boom")}) {
		t.Fatal("unexpected panic call")
	}
	if inj.isErrorArg(ast.NewIdent("err")) {
		t.Fatal("expected false without types info")
	}

	// With TypesInfo (builtin panic)
	src := `package main
func panic() {}
func boom() {}
func f() { panic(); boom() }
`
	inj2, _, astFile := setupInjectorTest(t, src)

	var userPanic, userBoom *ast.CallExpr
	ast.Inspect(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok {
				switch id.Name {
				case "panic":
					userPanic = c
				case "boom":
					userBoom = c
				}
			}
		}
		return true
	})
	if userPanic == nil || userBoom == nil {
		t.Fatal("expected calls")
	}
	if !inj2.isPanicCall(userPanic) {
		t.Fatal("expected user panic call")
	}
	if inj2.isPanicCall(userBoom) {
		t.Fatal("did not expect boom to be panic")
	}

	// isErrorArg with types info
	argErr := ast.NewIdent("err")
	inj2.Pkg.TypesInfo.Types[argErr] = types.TypeAndValue{Type: types.Universe.Lookup("error").Type()}
	if !inj2.isErrorArg(argErr) {
		t.Fatal("expected error arg")
	}
}

func TestHasTrailingErrorReturnDST(t *testing.T) {
	inj := &Injector{}
	fn := &dst.FuncDecl{Type: &dst.FuncType{}}
	if inj.hasTrailingErrorReturnDST(fn) {
		t.Fatal("expected false for nil results")
	}
	fn2 := &dst.FuncDecl{Type: &dst.FuncType{Results: &dst.FieldList{List: []*dst.Field{{Type: &dst.SelectorExpr{X: dst.NewIdent("pkg"), Sel: dst.NewIdent("Err")}}}}}}
	if inj.hasTrailingErrorReturnDST(fn2) {
		t.Fatal("expected false for non-ident type")
	}
	fn3 := &dst.FuncDecl{Type: &dst.FuncType{Results: &dst.FieldList{List: []*dst.Field{{Type: dst.NewIdent("error")}}}}}
	if !inj.hasTrailingErrorReturnDST(fn3) {
		t.Fatal("expected true for error ident")
	}
}

func TestRewritePanics_NoCandidates(t *testing.T) {
	src := `package main
func ok() { }
`
	inj, astFile, dstFile := setupDstEnv(t, src, false)
	changed, err := inj.RewritePanics(dstFile, astFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Fatal("expected no changes for no panics")
	}
}

func TestRewritePanics_GoStmtSkip(t *testing.T) {
	src := `package main
func f() { go panic("x") }
`
	inj, astFile, dstFile := setupDstEnv(t, src, false)
	changed, err := inj.RewritePanics(dstFile, astFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatal("expected change due to panic") // signature update may still occur
	}
}

func TestGenerateReturnFromPanicDST_EmptyResults(t *testing.T) {
	inj := &Injector{}
	fn := &dst.FuncDecl{Type: &dst.FuncType{Results: nil}}
	panicCall := &dst.CallExpr{Args: []dst.Expr{dst.NewIdent("err")}}
	astPanic := &ast.CallExpr{Args: []ast.Expr{ast.NewIdent("err")}}
	ret, err := inj.generateReturnFromPanicDST(fn, panicCall, astPanic, &dst.File{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ret.Results) != 1 {
		t.Fatalf("expected single result, got %d", len(ret.Results))
	}
}
