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
	untypedInt := types.Typ[types.UntypedInt]
	untypedBool := types.Typ[types.UntypedBool]
	untypedString := types.Typ[types.UntypedString]

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

	pkgOther := types.NewPackage("example.com/other", "other")
	namedError := types.NewNamed(
		types.NewTypeName(token.NoPos, pkgOther, "error", nil),
		types.Typ[types.Int], nil,
	)

	tests := []struct {
		name     string
		typ      types.Type
		expected string
	}{
		{"Bool", boolType, "b"},
		{"Int", intType, "i"},
		{"String", stringType, "s"},
		{"UntypedInt", untypedInt, "i"},
		{"UntypedBool", untypedBool, "b"},
		{"UntypedString", untypedString, "s"},
		{"PointerString", types.NewPointer(stringType), "s"},
		{"SliceString", types.NewSlice(stringType), "s"},
		{"ArrayString", types.NewArray(stringType, 2), "s"},
		{"ResponseWriter", respWriter, "w"},
		{"PointerResponseWriter", types.NewPointer(respWriter), "w"},
		{"MyStruct", myStruct, "myStruct"},
		{"APIClient", apiClient, "apiClient"},
		{"NamedErrorNoPath", namedError, "err"},
		{"AnonStruct", types.NewStruct(nil, nil), "v"},
		{"AnonInterface", types.NewInterfaceType(nil, nil), "v"},
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

func TestNameForType_FullNameNoPathMap(t *testing.T) {
	defer func() {
		delete(defaultTypeMap, "MyType")
	}()
	defaultTypeMap["MyType"] = "mt"
	pkg := types.NewPackage("example.com/foo", "foo")
	named := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "MyType", nil), types.NewStruct(nil, nil), nil)
	if got := NameForType(named); got != "mt" {
		t.Errorf("NameForType() = %q, want %q", got, "mt")
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
		{"ArrayExpr", &ast.ArrayType{Elt: &ast.Ident{Name: "User"}}, "user"},
		{"NonIdent", &ast.FuncType{}, "v"},
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
		{"My-Type", "myType"},
		{"!!!", "v"},
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
