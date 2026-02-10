package astgen

import (
	"go/ast"
	"go/token"
	"go/types"
	"testing"

	"github.com/dave/dst"
)

func newTypeParam(name string) *types.TypeParam {
	iface := types.NewInterfaceType(nil, nil)
	iface.Complete()
	return types.NewTypeParam(types.NewTypeName(token.NoPos, nil, name, nil), iface)
}

func newBadNamedType() *types.Named {
	return types.NewNamed(types.NewTypeName(token.NoPos, nil, "Bad!", nil), types.NewStruct(nil, nil), nil)
}

func newUnionType() types.Type {
	term := types.NewTerm(false, types.Typ[types.Int])
	return types.NewUnion([]*types.Term{term})
}

func TestBasicZeroASTVariants(t *testing.T) {
	cases := []struct {
		name string
		typ  *types.Basic
		want string
		kind token.Token
	}{
		{name: "bool", typ: types.Typ[types.Bool], want: "false"},
		{name: "int", typ: types.Typ[types.Int], want: "0", kind: token.INT},
		{name: "string", typ: types.Typ[types.String], want: `""`, kind: token.STRING},
		{name: "unsafe", typ: types.Typ[types.UnsafePointer], want: "nil"},
		{name: "default", typ: types.Typ[types.UntypedNil], want: "nil"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expr, err := basicZeroAST(tc.typ)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			switch lit := expr.(type) {
			case *ast.BasicLit:
				if lit.Kind != tc.kind || lit.Value != tc.want {
					t.Fatalf("unexpected literal: %v %q", lit.Kind, lit.Value)
				}
			case *ast.Ident:
				if lit.Name != tc.want {
					t.Fatalf("unexpected ident: %q", lit.Name)
				}
			default:
				t.Fatalf("unexpected expr type: %T", expr)
			}
		})
	}
}

func TestBasicZeroDSTVariants(t *testing.T) {
	cases := []struct {
		name string
		typ  *types.Basic
		want string
		kind token.Token
	}{
		{name: "bool", typ: types.Typ[types.Bool], want: "false"},
		{name: "int", typ: types.Typ[types.Int], want: "0", kind: token.INT},
		{name: "string", typ: types.Typ[types.String], want: `""`, kind: token.STRING},
		{name: "unsafe", typ: types.Typ[types.UnsafePointer], want: "nil"},
		{name: "default", typ: types.Typ[types.UntypedNil], want: "nil"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expr, err := basicZeroDST(tc.typ)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			switch lit := expr.(type) {
			case *dst.BasicLit:
				if lit.Kind != tc.kind || lit.Value != tc.want {
					t.Fatalf("unexpected literal: %v %q", lit.Kind, lit.Value)
				}
			case *dst.Ident:
				if lit.Name != tc.want {
					t.Fatalf("unexpected ident: %q", lit.Name)
				}
			default:
				t.Fatalf("unexpected expr type: %T", expr)
			}
		})
	}
}

func TestZeroExpr_Variants(t *testing.T) {
	ctx := ZeroCtx{MakeMapsAndChans: true}
	mapType := types.NewMap(types.Typ[types.String], types.Typ[types.Int])

	expr, err := ZeroExpr(mapType, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if call, ok := expr.(*ast.CallExpr); !ok || call.Fun.(*ast.Ident).Name != "make" {
		t.Fatalf("expected make call, got %T", expr)
	}

	tuple := types.NewTuple(types.NewVar(token.NoPos, nil, "", types.Typ[types.Int]))
	if _, err := ZeroExpr(tuple, ZeroCtx{}); err == nil {
		t.Fatal("expected tuple error")
	}

	if _, err := ZeroExpr(newUnionType(), ZeroCtx{}); err == nil {
		t.Fatal("expected unsupported type error")
	}

	tp := newTypeParam("T")
	gen, err := ZeroExpr(tp, ZeroCtx{})
	if err != nil {
		t.Fatalf("unexpected generic error: %v", err)
	}
	if _, ok := gen.(*ast.StarExpr); !ok {
		t.Fatalf("expected generic new(T) expression, got %T", gen)
	}
}

func TestZeroExpr_OverrideAndShadow(t *testing.T) {
	ctx := ZeroCtx{Overrides: map[string]string{types.Typ[types.Int].String(): "1"}}
	expr, err := ZeroExpr(types.Typ[types.Int], ctx)
	if err != nil {
		t.Fatalf("unexpected override error: %v", err)
	}
	if lit, ok := expr.(*ast.BasicLit); !ok || lit.Value != "1" {
		t.Fatalf("unexpected override expr: %T", expr)
	}

	shadowed := types.NewNamed(types.NewTypeName(token.NoPos, nil, "Shadowed", nil), types.NewStruct(nil, nil), nil)
	ctx = ZeroCtx{
		IsNameSafe: func(string, types.Object) bool { return false },
	}
	if _, err := ZeroExpr(shadowed, ctx); err == nil {
		t.Fatal("expected shadowing error")
	}
}

func TestZeroExprDST_Variants(t *testing.T) {
	ctx := ZeroCtx{MakeMapsAndChans: true}
	mapType := types.NewMap(types.Typ[types.String], types.Typ[types.Int])

	expr, err := ZeroExprDST(mapType, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if call, ok := expr.(*dst.CallExpr); !ok || call.Fun.(*dst.Ident).Name != "make" {
		t.Fatalf("expected make call, got %T", expr)
	}

	tuple := types.NewTuple(types.NewVar(token.NoPos, nil, "", types.Typ[types.Int]))
	if _, err := ZeroExprDST(tuple, ZeroCtx{}); err == nil {
		t.Fatal("expected tuple error")
	}

	if _, err := ZeroExprDST(newUnionType(), ZeroCtx{}); err == nil {
		t.Fatal("expected unsupported type error")
	}

	tp := newTypeParam("T")
	gen, err := ZeroExprDST(tp, ZeroCtx{})
	if err != nil {
		t.Fatalf("unexpected generic error: %v", err)
	}
	if _, ok := gen.(*dst.StarExpr); !ok {
		t.Fatalf("expected generic new(T) expression, got %T", gen)
	}
}

func TestZeroExprDST_OverrideAndShadow(t *testing.T) {
	ctx := ZeroCtx{Overrides: map[string]string{types.Typ[types.Int].String(): "1"}}
	expr, err := ZeroExprDST(types.Typ[types.Int], ctx)
	if err != nil {
		t.Fatalf("unexpected override error: %v", err)
	}
	if lit, ok := expr.(*dst.BasicLit); !ok || lit.Value != "1" {
		t.Fatalf("unexpected override expr: %T", expr)
	}

	shadowed := types.NewNamed(types.NewTypeName(token.NoPos, nil, "Shadowed", nil), types.NewStruct(nil, nil), nil)
	ctx = ZeroCtx{
		IsNameSafe: func(string, types.Object) bool { return false },
	}
	if _, err := ZeroExprDST(shadowed, ctx); err == nil {
		t.Fatal("expected shadowing error")
	}
}

func TestGenericAndMakeErrors(t *testing.T) {
	if _, err := genericZeroAST(newTypeParam("Bad!"), nil); err == nil {
		t.Fatal("expected genericZeroAST error")
	}
	if _, err := makeInitializedAST(newBadNamedType(), nil); err == nil {
		t.Fatal("expected makeInitializedAST error")
	}
	if _, err := genericZeroDST(newTypeParam("Bad!"), nil); err == nil {
		t.Fatal("expected genericZeroDST error")
	}
	if _, err := makeInitializedDST(newBadNamedType(), nil); err == nil {
		t.Fatal("expected makeInitializedDST error")
	}
}

func TestParseDstExtractionFailures(t *testing.T) {
	orig := decoratorParse
	defer func() { decoratorParse = orig }()

	decoratorParse = func(interface{}) (*dst.File, error) {
		return &dst.File{}, nil
	}

	if _, err := parseDstExpr("1"); err == nil {
		t.Fatal("expected parseDstExpr extraction error")
	}
	if _, err := parseDstType("int"); err == nil {
		t.Fatal("expected parseDstType extraction error")
	}
}
