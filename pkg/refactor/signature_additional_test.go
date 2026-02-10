package refactor

import (
	"go/ast"
	"go/token"
	"testing"

	"github.com/dave/dst"
)

func TestSignatureHelpers(t *testing.T) {
	if !isErrorExpr(&ast.Ident{Name: "error"}) {
		t.Error("expected error ident to be detected")
	}
	if isErrorExpr(&ast.Ident{Name: "other"}) {
		t.Error("unexpected error ident match")
	}
	if !isErrorDstExpr(dst.NewIdent("error")) {
		t.Error("expected dst error ident to be detected")
	}
	if isErrorDstExpr(dst.NewIdent("other")) {
		t.Error("unexpected dst error ident match")
	}
	if got := nameForDstExpr(&dst.BasicLit{Kind: token.INT, Value: "1"}); got != "v" {
		t.Errorf("nameForDstExpr basic lit = %q, want %q", got, "v")
	}
	if got := nameForDstExpr(&dst.SelectorExpr{X: dst.NewIdent("context"), Sel: dst.NewIdent("Context")}); got != "ctx" {
		t.Errorf("nameForDstExpr selector = %q, want %q", got, "ctx")
	}
	if got := unwrapDstExpr(&dst.ArrayType{Elt: dst.NewIdent("int")}); got == nil {
		t.Error("unwrapDstExpr returned nil")
	}
}
