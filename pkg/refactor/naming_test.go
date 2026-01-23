package refactor

import (
	"go/ast"
	"go/token"
	"go/types"
	"testing"
)

func TestNameForType(t *testing.T) {
	// Mocks
	boolType := types.Typ[types.Bool]
	intType := types.Typ[types.Int]
	stringType := types.Typ[types.String]

	pkgHttp := types.NewPackage("net/http", "http")
	respWriter := types.NewNamed(
		types.NewTypeName(token.NoPos, pkgHttp, "ResponseWriter", nil),
		nil, nil,
	)

	pkgMy := types.NewPackage("example.com/foo", "foo")
	myStruct := types.NewNamed(
		types.NewTypeName(token.NoPos, pkgMy, "MyStruct", nil),
		types.NewStruct(nil, nil), nil,
	)

	apiClient := types.NewNamed(
		types.NewTypeName(token.NoPos, pkgMy, "APIClient", nil),
		types.NewStruct(nil, nil), nil,
	)

	tests := []struct {
		name     string
		typ      types.Type
		expected string
	}{
		{"Bool", boolType, "b"},
		{"Int", intType, "i"},
		{"String", stringType, "s"},
		{"PointerString", types.NewPointer(stringType), "s"},
		{"SliceString", types.NewSlice(stringType), "s"},
		{"ResponseWriter", respWriter, "w"},
		{"PointerResponseWriter", types.NewPointer(respWriter), "w"},
		{"MyStruct", myStruct, "myStruct"},
		{"APIClient", apiClient, "apiClient"},
		{"Nil", nil, "v"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NameForType(tt.typ); got != tt.expected {
				t.Errorf("NameForType() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestNameForExpr(t *testing.T) {
	tests := []struct {
		name     string
		expr     ast.Expr
		expected string
	}{
		{"IdentInt", &ast.Ident{Name: "int"}, "i"},
		{"IdentString", &ast.Ident{Name: "string"}, "s"},
		{"IdentUser", &ast.Ident{Name: "User"}, "user"},
		{"IdentAPI", &ast.Ident{Name: "APIID"}, "apiid"}, // Heuristic limitation check
		{"SelectorCtx", &ast.SelectorExpr{X: &ast.Ident{Name: "context"}, Sel: &ast.Ident{Name: "Context"}}, "ctx"},
		{"SelectorTx", &ast.SelectorExpr{X: &ast.Ident{Name: "sql"}, Sel: &ast.Ident{Name: "Tx"}}, "tx"},
		{"StarExpr", &ast.StarExpr{X: &ast.Ident{Name: "User"}}, "user"},
		{"Unknown", nil, "v"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NameForExpr(tt.expr); got != tt.expected {
				t.Errorf("NameForExpr() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestToVariableName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"User", "user"},
		{"userID", "userID"},
		{"APIClient", "apiClient"},
		{"JSONParser", "jsonParser"},
		{"HTTPServer", "httpServer"},
		{"MyHTTPServer", "myHTTPServer"},
		{"return", "r"}, // Keyword handling
		{"", "v"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := toVariableName(tt.input); got != tt.expected {
				t.Errorf("toVariableName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
