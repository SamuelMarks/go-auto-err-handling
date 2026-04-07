package refactor

import (
	"go/ast"
	"go/types"
	"testing"
)

func TestNameForType_EdgeCases(t *testing.T) {
	// 1. Complex types (fall through to "v")
	c64 := types.Typ[types.Complex64]
	if got := NameForType(c64); got != "v" {
		t.Errorf("Complex64: got %q, want v", got)
	}

	// 2. Channels (fall through to "v")
	chanType := types.NewChan(types.SendRecv, types.Typ[types.Int])
	if got := NameForType(chanType); got != "v" {
		t.Errorf("Chan: got %q, want v", got)
	}

	// 3. ToVariableName keywords coverage
	keywords := []string{
		"break", "case", "chan", "const", "continue", "default", "defer",
		"else", "fallthrough", "for", "func", "go", "goto", "if", "import",
		"interface", "map", "package", "range", "return", "select", "struct",
		"switch", "type", "var",
	}
	for _, kw := range keywords {
		want := string(kw[0])
		if got := toVariableName(kw); got != want {
			t.Errorf("Keyword %q: got %q, want %q", kw, got, want)
		}
	}

	// 4. Special cleaning regex
	if got := toVariableName("User-ID"); got != "userID" {
		t.Errorf("Cleaning: got %q, want userID", got)
	}
	if got := toVariableName("---"); got != "v" {
		t.Errorf("All invalid chain: got %q, want v", got)
	}
}

func TestNameForExpr_Fallbacks(t *testing.T) {
	// ArrayType unwrapping logic in NameForExpr -> unwrapExpr
	arr := &ast.ArrayType{Elt: &ast.Ident{Name: "Item"}}
	if got := NameForExpr(arr); got != "item" {
		t.Errorf("Array: got %q, want item", got)
	}

	// Invalid selector fallthrough
	sel := &ast.SelectorExpr{X: &ast.BasicLit{}, Sel: &ast.Ident{Name: "Foo"}}
	if got := NameForExpr(sel); got != "foo" {
		t.Errorf("Selector: got %q, want foo", got)
	}
}
