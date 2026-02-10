package astgen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

func TestZeroExpr_UnsafePointer(t *testing.T) {
	expr, err := ZeroExpr(types.Typ[types.UnsafePointer], ZeroCtx{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id, ok := expr.(*ast.Ident); !ok || id.Name != "nil" {
		t.Fatalf("expected nil ident, got %T", expr)
	}
}

func TestZeroExpr_InvalidOverride(t *testing.T) {
	ctx := ZeroCtx{Overrides: map[string]string{types.Typ[types.Int].String(): "***"}}
	_, err := ZeroExpr(types.Typ[types.Int], ctx)
	if err == nil {
		t.Fatal("expected error for invalid override")
	}
}

func TestZeroExpr_InvalidTypeString(t *testing.T) {
	bad := types.NewNamed(types.NewTypeName(token.NoPos, nil, "Bad!", nil), types.NewStruct(nil, nil), nil)
	_, err := ZeroExpr(bad, ZeroCtx{})
	if err == nil {
		t.Fatal("expected error for invalid type string")
	}
}

func TestZeroExprDST_InvalidOverride(t *testing.T) {
	ctx := ZeroCtx{Overrides: map[string]string{types.Typ[types.Int].String(): "***"}}
	_, err := ZeroExprDST(types.Typ[types.Int], ctx)
	if err == nil {
		t.Fatal("expected error for invalid override")
	}
}

func TestZeroExprDST_InvalidTypeString(t *testing.T) {
	bad := types.NewNamed(types.NewTypeName(token.NoPos, nil, "Bad!", nil), types.NewStruct(nil, nil), nil)
	_, err := ZeroExprDST(bad, ZeroCtx{})
	if err == nil {
		t.Fatal("expected error for invalid type string")
	}
}

func TestClearPositions_CoversNodes(t *testing.T) {
	src := `package p

import "fmt"

type S struct{ F int }
type I interface{ M() }
type A [2]int

func f(x interface{}) {
	var p *int
	_ = *p
	_ = []int{1, 2}
	_ = map[string]int{"a": 1}
	_ = struct{A int}{A: 1}
	_ = A{1, 2}
	_ = make(chan int)
	_ = make(map[string]int)
	_ = func(a ...int) { fmt.Println(a[0]) }
	_ = ([]int{1, 2})[0]
	_ = x.(int)
	_ = &S{}
	_ = (1 + 2)
	_ = -1
	_ = S{}.F
	_ = []int{1, 2}[0:1]
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ClearPositions(file)
}

func TestParseDstExprAndTypeErrors(t *testing.T) {
	if _, err := parseDstExpr("***"); err == nil {
		t.Fatal("expected parseDstExpr error")
	}
	if _, err := parseDstType("***"); err == nil {
		t.Fatal("expected parseDstType error")
	}
}
