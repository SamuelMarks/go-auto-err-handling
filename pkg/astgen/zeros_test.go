package astgen

import (
	"bytes"
	"go/printer"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// TestZeroExpr verifies standard generation.
func TestZeroExpr(t *testing.T) {
	boolType := types.Typ[types.Bool]
	intType := types.Typ[types.Int]
	stringType := types.Typ[types.String]
	floatType := types.Typ[types.Float64]

	namedM := types.NewNamed(
		types.NewTypeName(token.NoPos, nil, "MyStruct", nil),
		types.NewStruct(nil, nil),
		nil,
	)

	namedArray := types.NewNamed(
		types.NewTypeName(token.NoPos, nil, "MyArray", nil),
		types.NewArray(intType, 3),
		nil,
	)

	tests := []struct {
		name      string
		inputType types.Type
		expected  string
		wantErr   bool
	}{
		{"Bool", boolType, "false", false},
		{"Int", intType, "0", false},
		{"String", stringType, `""`, false},
		{"Float", floatType, "0", false},
		{"Pointer", types.NewPointer(intType), "nil", false},
		{"Slice", types.NewSlice(intType), "nil", false},
		{"Map", types.NewMap(stringType, intType), "nil", false},
		{"Chan", types.NewChan(types.SendRecv, intType), "nil", false},
		{"NamedStruct", namedM, "MyStruct{}", false},
		{"NamedArray", namedArray, "MyArray{}", false},
		{"AnonymousStruct", types.NewStruct(nil, nil), "struct{}{}", false},
		{"ErrorInterface", types.Universe.Lookup("error").Type(), "nil", false},
		{"TupleError", types.NewTuple(types.NewVar(token.NoPos, nil, "a", intType)), "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// ZeroCtx with no overrides
			ctx := ZeroCtx{}
			expr, err := ZeroExpr(tt.inputType, ctx)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ZeroExpr() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			var buf bytes.Buffer
			fset := token.NewFileSet()
			if err := printer.Fprint(&buf, fset, expr); err != nil {
				t.Fatalf("printer.Fprint() error = %v", err)
			}

			got := buf.String()
			if normalize(got) != normalize(tt.expected) {
				t.Errorf("ZeroExpr() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// TestZeroExpr_Overrides verifies that custom values are returned when specified in Context.
func TestZeroExpr_Overrides(t *testing.T) {
	// Setup a type that we will override
	pkg := types.NewPackage("k8s.io/api/admission/v1", "v1")
	typeName := types.NewTypeName(token.NoPos, pkg, "AdmissionResponse", nil)
	namedType := types.NewNamed(typeName, types.NewStruct(nil, nil), nil)
	pointerType := types.NewPointer(namedType)

	// Context with Override logic
	overrides := map[string]string{
		"*k8s.io/api/admission/v1.AdmissionResponse": "&v1.AdmissionResponse{Allowed: false}",
	}
	ctx := ZeroCtx{
		Overrides: overrides,
	}

	expr, err := ZeroExpr(pointerType, ctx)
	if err != nil {
		t.Fatalf("ZeroExpr with override failed: %v", err)
	}

	var buf bytes.Buffer
	printer.Fprint(&buf, token.NewFileSet(), expr)
	got := buf.String()

	expected := "&v1.AdmissionResponse{Allowed: false}"
	if normalize(got) != normalize(expected) {
		t.Errorf("Override failed. Got %q, Want %q", got, expected)
	}
}

// TestZeroExpr_CompositeWithQualifier tests qualifier logic in context.
func TestZeroExpr_CompositeWithQualifier(t *testing.T) {
	pkg := types.NewPackage("example.com/foo", "foo")
	named := types.NewNamed(
		types.NewTypeName(token.NoPos, pkg, "Bar", nil),
		types.NewStruct(nil, nil),
		nil,
	)

	// Qualifier: force "baz" for foo package
	qAlias := func(p *types.Package) string {
		if p.Path() == "example.com/foo" {
			return "baz"
		}
		return p.Name()
	}

	ctx := ZeroCtx{Qualifier: qAlias}

	expr, err := ZeroExpr(named, ctx)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	printer.Fprint(&buf, token.NewFileSet(), expr)

	expectedAlias := "baz.Bar{}"
	if buf.String() != expectedAlias {
		t.Errorf("ZeroExpr() with alias qualifier = %q, want %q", buf.String(), expectedAlias)
	}
}

// normalize removes all whitespace to ensure robust comparison.
func normalize(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\t", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}
