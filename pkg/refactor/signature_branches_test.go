package refactor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/dave/dst"
)

func TestAddErrorToSignature_AnonymizeNoUsage(t *testing.T) {
	fset, decl := parseFuncDecl(t, "func A() (res int) { return 1 }")
	_, err := AddErrorToSignature(fset, decl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := render(fset, decl)
	want := "func A() (int, error) { return 1, nil }"
	if normalize(got) != normalize(want) {
		t.Errorf("got %s want %s", got, want)
	}
}

func TestAddErrorToSignature_ConflictingErrName(t *testing.T) {
	fset, decl := parseFuncDecl(t, "func A(err int) (x int) { return }")
	_, err := AddErrorToSignature(fset, decl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := render(fset, decl)
	want := "func A(err int) (x int, err1 error) { return }"
	if normalize(got) != normalize(want) {
		t.Errorf("got %s want %s", got, want)
	}
}

func TestAddErrorToSignatureDST_ConflictingErrName(t *testing.T) {
	decl := parseDstFuncDecl(t, "func A(err int) (x int) { return }")
	_, err := AddErrorToSignatureDST(decl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := renderDst(decl)
	want := "func A(err int) (x int, err1 error) { return }"
	if normalize(got) != normalize(want) {
		t.Errorf("got %s want %s", got, want)
	}
}

func TestAddErrorToFuncTypeDST_Nil(t *testing.T) {
	if _, err := AddErrorToFuncTypeDST(nil); err == nil {
		t.Error("expected error for nil func type")
	}
}

func TestEnsureNamedReturns_WithTypeInfoAndDuplicates(t *testing.T) {
	src := "package p\nfunc A() (int, int, error) { return 1, 2, nil }"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var decl *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			decl = fd
		}
	}
	if decl == nil {
		t.Fatal("decl not found")
	}
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
	}
	for i, field := range decl.Type.Results.List {
		if i == len(decl.Type.Results.List)-1 {
			info.Types[field.Type] = types.TypeAndValue{Type: types.Universe.Lookup("error").Type()}
		} else {
			info.Types[field.Type] = types.TypeAndValue{Type: types.Typ[types.Int]}
		}
	}
	changed, err := EnsureNamedReturns(fset, decl, info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected change")
	}
	got := render(fset, decl)
	want := "func A() (i int, i1 int, err error) { return 1, 2, nil }"
	if normalize(got) != normalize(want) {
		t.Errorf("got %s want %s", got, want)
	}
}

func TestEnsureNamedReturns_FallbackToExpr(t *testing.T) {
	src := "package p\nfunc A() interface{} { return nil }"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
	}
	var decl *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			decl = fd
		}
	}
	info.Types[decl.Type.Results.List[0].Type] = types.TypeAndValue{Type: types.NewInterfaceType(nil, nil)}
	changed, err := EnsureNamedReturns(fset, decl, info)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected change")
	}
	got := render(fset, decl)
	want := "func A() (v interface{}) { return nil }"
	if normalize(got) != normalize(want) {
		t.Errorf("got %s want %s", got, want)
	}
}

func TestEnsureNamedReturns_AlreadyNamed(t *testing.T) {
	fset, decl := parseFuncDecl(t, "func A() (x int) { return x }")
	changed, err := EnsureNamedReturns(fset, decl, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Error("expected no change for already named returns")
	}
}

func TestEnsureNamedReturnsDST_Duplicates(t *testing.T) {
	decl := parseDstFuncDecl(t, "func A() (int, int, error) { return 1, 2, nil }")
	changed, err := EnsureNamedReturnsDST(decl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected change")
	}
	got := renderDst(decl)
	want := "func A() (i int, i1 int, err error) { return 1, 2, nil }"
	if normalize(got) != normalize(want) {
		t.Errorf("got %s want %s", got, want)
	}
}

func TestScanForNakedReturnsAndNameUsed(t *testing.T) {
	if scanForNakedReturns(nil) {
		t.Error("expected false for nil body")
	}
	body := &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{}}}
	if !scanForNakedReturns(body) {
		t.Error("expected true for naked return")
	}
	bodyWithLit := &ast.BlockStmt{List: []ast.Stmt{
		&ast.ExprStmt{X: &ast.FuncLit{Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{}}}}},
	}}
	if scanForNakedReturns(bodyWithLit) {
		t.Error("expected false when naked return is inside func lit")
	}
	bodyWithDecl := &ast.BlockStmt{List: []ast.Stmt{
		&ast.DeclStmt{Decl: &ast.FuncDecl{Name: ast.NewIdent("inner"), Type: &ast.FuncType{}, Body: &ast.BlockStmt{}}},
	}}
	if scanForNakedReturns(bodyWithDecl) {
		t.Error("expected false when naked return is inside func decl")
	}
	if isNameUsed(nil, "x") {
		t.Error("expected false for nil body")
	}
	bodyUsed := &ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: &ast.Ident{Name: "x"}}}}
	if !isNameUsed(bodyUsed, "x") {
		t.Error("expected true for used name")
	}
	bodyIgnored := &ast.BlockStmt{List: []ast.Stmt{
		&ast.ExprStmt{X: &ast.FuncLit{Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: &ast.Ident{Name: "x"}}}}}},
	}}
	if isNameUsed(bodyIgnored, "x") {
		t.Error("expected false for name used inside func lit only")
	}
	bodyIgnoredDecl := &ast.BlockStmt{List: []ast.Stmt{
		&ast.DeclStmt{Decl: &ast.FuncDecl{Name: ast.NewIdent("inner"), Type: &ast.FuncType{}, Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: &ast.Ident{Name: "x"}}}}}},
	}}
	if isNameUsed(bodyIgnoredDecl, "x") {
		t.Error("expected false for name used inside func decl only")
	}
}

func TestScanForNakedReturnsDSTAndNameUsedDST(t *testing.T) {
	if scanForNakedReturnsDST(nil) {
		t.Error("expected false for nil body")
	}
	body := &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{}}}
	if !scanForNakedReturnsDST(body) {
		t.Error("expected true for naked return")
	}
	bodyWithLit := &dst.BlockStmt{List: []dst.Stmt{
		&dst.ExprStmt{X: &dst.FuncLit{Body: &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{}}}}},
	}}
	if scanForNakedReturnsDST(bodyWithLit) {
		t.Error("expected false when naked return is inside func lit")
	}
	if isNameUsedDST(nil, "x") {
		t.Error("expected false for nil body")
	}
	bodyUsed := &dst.BlockStmt{List: []dst.Stmt{&dst.ExprStmt{X: dst.NewIdent("x")}}}
	if !isNameUsedDST(bodyUsed, "x") {
		t.Error("expected true for used name")
	}
	bodyIgnored := &dst.BlockStmt{List: []dst.Stmt{
		&dst.ExprStmt{X: &dst.FuncLit{Body: &dst.BlockStmt{List: []dst.Stmt{&dst.ExprStmt{X: dst.NewIdent("x")}}}}},
	}}
	if isNameUsedDST(bodyIgnored, "x") {
		t.Error("expected false for name used inside func lit only")
	}
}

func TestSignatureTypeHelpers(t *testing.T) {
	if got := nameForDstExpr(nil); got != "v" {
		t.Errorf("nameForDstExpr nil = %q, want %q", got, "v")
	}
	if !isErrorExpr(&ast.Ident{Name: "error"}) {
		t.Error("expected true for error ident")
	}
	if isErrorExpr(&ast.SelectorExpr{}) {
		t.Error("expected false for non-ident error expr")
	}
	if !isErrorDstExpr(dst.NewIdent("error")) {
		t.Error("expected true for dst error ident")
	}
	if isErrorDstExpr(&dst.SelectorExpr{}) {
		t.Error("expected false for dst non-ident error expr")
	}
	if got := nameForDstExpr(dst.NewIdent("int")); got != "i" {
		t.Errorf("nameForDstExpr int = %q, want %q", got, "i")
	}
	if got := nameForDstExpr(dst.NewIdent("User")); got != "user" {
		t.Errorf("nameForDstExpr User = %q, want %q", got, "user")
	}
	if got := unwrapDstExpr(&dst.StarExpr{X: dst.NewIdent("int")}); got == nil {
		t.Error("unwrapDstExpr returned nil")
	}
}

func TestAddErrorToSignature_NestedFuncs(t *testing.T) {
	decl := &ast.FuncDecl{
		Name: ast.NewIdent("A"),
		Type: &ast.FuncType{
			Params: &ast.FieldList{List: []*ast.Field{
				{Names: []*ast.Ident{ast.NewIdent("err")}, Type: ast.NewIdent("int")},
			}},
			Results: &ast.FieldList{List: []*ast.Field{
				{Names: []*ast.Ident{ast.NewIdent("x")}, Type: ast.NewIdent("int")},
			}},
		},
		Body: &ast.BlockStmt{List: []ast.Stmt{
			&ast.ExprStmt{X: &ast.FuncLit{Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{}}}}},
			&ast.DeclStmt{Decl: &ast.FuncDecl{Name: ast.NewIdent("inner"), Type: &ast.FuncType{}, Body: &ast.BlockStmt{}}},
			&ast.ReturnStmt{},
		}},
	}
	_, err := AddErrorToSignature(token.NewFileSet(), decl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	last := decl.Type.Results.List[len(decl.Type.Results.List)-1]
	if len(last.Names) == 0 || last.Names[0].Name != "err1" {
		t.Errorf("expected err1 return name, got %#v", last.Names)
	}
}

func TestAddErrorToSignatureDST_NestedFuncLit(t *testing.T) {
	decl := &dst.FuncDecl{
		Name: dst.NewIdent("A"),
		Type: &dst.FuncType{},
		Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.ExprStmt{X: &dst.FuncLit{Body: &dst.BlockStmt{}}},
		}},
	}
	if _, err := AddErrorToSignatureDST(decl); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
