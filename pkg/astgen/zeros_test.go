package astgen

import (
	"bytes"
	"go/printer"
	"go/token"
	"go/types"
	"testing"
)

// TestZeroExpr verifies that ZeroExpr generates the correct AST representation
// for various go/types inputs.
func TestZeroExpr(t *testing.T) {
	// Setup common types for test cases
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
		// We compare string representations of the AST to verify correctness
		expected string
		wantErr  bool
	}{
		{
			name:      "Bool",
			inputType: boolType,
			expected:  "false",
		},
		{
			name:      "Int",
			inputType: intType,
			expected:  "0",
		},
		{
			name:      "String",
			inputType: stringType,
			expected:  `""`,
		},
		{
			name:      "Float",
			inputType: floatType,
			expected:  "0",
		},
		{
			name:      "Pointer",
			inputType: types.NewPointer(intType),
			expected:  "nil",
		},
		{
			name:      "Slice",
			inputType: types.NewSlice(intType),
			expected:  "nil",
		},
		{
			name:      "Map",
			inputType: types.NewMap(stringType, intType),
			expected:  "nil",
		},
		{
			name:      "NamedStruct",
			inputType: namedM,
			expected:  "MyStruct{}",
		},
		{
			name:      "NamedArray",
			inputType: namedArray,
			expected:  "MyArray{}",
		},
		{
			name:      "AnonymousStruct",
			inputType: types.NewStruct(nil, nil),
			expected:  "struct{}{}",
		},
		{
			name:      "ErrorInterface",
			inputType: types.Universe.Lookup("error").Type(),
			expected:  "nil",
		},
		{
			name:      "TupleError",
			inputType: types.NewTuple(types.NewVar(token.NoPos, nil, "a", intType)),
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, err := ZeroExpr(tt.inputType, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ZeroExpr() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			// Render AST to string for comparison
			var buf bytes.Buffer
			fset := token.NewFileSet()
			if err := printer.Fprint(&buf, fset, expr); err != nil {
				t.Fatalf("printer.Fprint() error = %v", err)
			}

			if got := buf.String(); got != tt.expected {
				t.Errorf("ZeroExpr() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// TestZeroExpr_CompositeWithQualifier tests the qualifier logic for composite types.
func TestZeroExpr_CompositeWithQualifier(t *testing.T) {
	pkg := types.NewPackage("example.com/foo", "foo")
	named := types.NewNamed(
		types.NewTypeName(token.NoPos, pkg, "Bar", nil),
		types.NewStruct(nil, nil),
		nil,
	)

	// Qualifier that forces package name inclusion
	q := func(p *types.Package) string {
		return p.Name()
	}

	expr, err := ZeroExpr(named, q)
	if err != nil {
		t.Fatalf("ZeroExpr() error = %v", err)
	}

	var buf bytes.Buffer
	printer.Fprint(&buf, token.NewFileSet(), expr)

	expected := "foo.Bar{}"
	if buf.String() != expected {
		t.Errorf("ZeroExpr() with qualifier = %q, want %q", buf.String(), expected)
	}
}
