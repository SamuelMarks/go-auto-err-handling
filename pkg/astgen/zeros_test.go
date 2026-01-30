package astgen

import (
	"bytes"
	"go/printer"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
)

// TestZeroExpr verifies standard generation (AST).
func TestZeroExpr(t *testing.T) {
	cases := getTestCases(t)

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ctx := ZeroCtx{}
			expr, err := ZeroExpr(tt.inputType, ctx)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ZeroExpr() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			// Use AST printer
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

// TestZeroExprDST verifies DST generation.
func TestZeroExprDST(t *testing.T) {
	cases := getTestCases(t)

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ctx := ZeroCtx{}
			expr, err := ZeroExprDST(tt.inputType, ctx)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ZeroExprDST() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			// Wrap expr in a file node to print it safely using dst Restorer
			got := renderDstNode(t, expr)

			if normalize(got) != normalize(tt.expected) {
				t.Errorf("ZeroExprDST() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// renderDstNode wraps a node in a dummy File to use Restorer.Fprint securely.
func renderDstNode(t *testing.T, node dst.Node) string {
	var buf bytes.Buffer
	res := decorator.NewRestorer()

	var expr dst.Expr
	if e, ok := node.(dst.Expr); ok {
		expr = e
	} else {
		t.Fatalf("Unsupported node type for rendering: %T", node)
	}

	// Create a dummy file structure: package p; var _ = <expr>
	file := &dst.File{
		Name: dst.NewIdent("p"),
		Decls: []dst.Decl{
			&dst.GenDecl{
				Tok: token.VAR,
				Specs: []dst.Spec{
					&dst.ValueSpec{
						Names:  []*dst.Ident{dst.NewIdent("_")},
						Values: []dst.Expr{expr},
					},
				},
			},
		},
	}

	if err := res.Fprint(&buf, file); err != nil {
		t.Fatalf("Fprint failed: %v", err)
	}

	// Output will be "package p\n\nvar _ = <expected>"
	// We normalize it anyway, so we just strip the known prefix
	s := buf.String()
	// Hacky cleanup for checking
	s = strings.ReplaceAll(s, "package p", "")
	s = strings.ReplaceAll(s, "var _ =", "")
	return s
}

type testCase struct {
	name      string
	inputType types.Type
	expected  string
	wantErr   bool
}

func getTestCases(t *testing.T) []testCase {
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

	typeParamT := types.NewTypeParam(
		types.NewTypeName(token.NoPos, nil, "T", nil),
		nil,
	)

	return []testCase{
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
		{"GenericTypeParam", typeParamT, "*new(T)", false},
		{"TupleError", types.NewTuple(types.NewVar(token.NoPos, nil, "a", intType)), "", true},
		{"NilInput", nil, "", true},
	}
}

// TestASTOverrides & TestDSTOverrides shared logic handled by separate runners below

func TestZeroExpr_Overrides(t *testing.T) {
	ctx, ptr := setupOverrideCtx()
	expr, err := ZeroExpr(ptr, ctx)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printer.Fprint(&buf, token.NewFileSet(), expr)
	checkOverride(t, buf.String())
}

func TestZeroExprDST_Overrides(t *testing.T) {
	ctx, ptr := setupOverrideCtx()
	expr, err := ZeroExprDST(ptr, ctx)
	if err != nil {
		t.Fatal(err)
	}
	checkOverride(t, renderDstNode(t, expr))
}

func setupOverrideCtx() (ZeroCtx, types.Type) {
	pkg := types.NewPackage("k8s.io/api/admission/v1", "v1")
	typeName := types.NewTypeName(token.NoPos, pkg, "AdmissionResponse", nil)
	namedType := types.NewNamed(typeName, types.NewStruct(nil, nil), nil)
	pointerType := types.NewPointer(namedType)
	overrides := map[string]string{
		"*k8s.io/api/admission/v1.AdmissionResponse": "&v1.AdmissionResponse{Allowed: false}",
	}
	return ZeroCtx{Overrides: overrides}, pointerType
}

func checkOverride(t *testing.T, got string) {
	expected := "&v1.AdmissionResponse{Allowed: false}"
	if normalize(got) != normalize(expected) {
		t.Errorf("Override mismatch. Got %q, Want %q", got, expected)
	}
}

// TestZeroExpr_SoftInit verifies that maps and channels are initialized with make()
func TestZeroExpr_SoftInit(t *testing.T) {
	boolType := types.Typ[types.Bool]
	intType := types.Typ[types.Int]
	stringType := types.Typ[types.String]
	mapType := types.NewMap(stringType, intType)
	chanType := types.NewChan(types.SendRecv, boolType)
	namedMapParams := types.NewNamed(
		types.NewTypeName(token.NoPos, nil, "MyStartMap", nil),
		types.NewMap(stringType, boolType),
		nil,
	)

	ctx := ZeroCtx{MakeMapsAndChans: true}
	tests := []struct {
		name      string
		inputType types.Type
		expected  string
	}{
		{"Map", mapType, "make(map[string]int)"},
		{"Chan", chanType, "make(chan bool)"},
		{"NamedMap", namedMapParams, "make(MyStartMap)"},
		{"SliceIgnored", types.NewSlice(intType), "nil"},
	}

	for _, tt := range tests {
		t.Run("AST_"+tt.name, func(t *testing.T) {
			expr, _ := ZeroExpr(tt.inputType, ctx)
			var buf bytes.Buffer
			printer.Fprint(&buf, token.NewFileSet(), expr)
			if normalize(buf.String()) != normalize(tt.expected) {
				t.Errorf("AST SoftInit mismatch. Got %q", buf.String())
			}
		})
		t.Run("DST_"+tt.name, func(t *testing.T) {
			expr, _ := ZeroExprDST(tt.inputType, ctx)
			got := renderDstNode(t, expr)
			if normalize(got) != normalize(tt.expected) {
				t.Errorf("DST SoftInit mismatch. Got %q", got)
			}
		})
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
	qAlias := func(p *types.Package) string {
		if p.Path() == "example.com/foo" {
			return "baz"
		}
		return p.Name()
	}
	ctx := ZeroCtx{Qualifier: qAlias}
	expected := "baz.Bar{}"

	// AST
	exprAST, _ := ZeroExpr(named, ctx)
	var buf bytes.Buffer
	printer.Fprint(&buf, token.NewFileSet(), exprAST)
	if buf.String() != expected {
		t.Errorf("AST Qualifier failed. Got %s", buf.String())
	}

	// DST
	exprDST, _ := ZeroExprDST(named, ctx)
	gotDST := renderDstNode(t, exprDST)
	if normalize(gotDST) != normalize(expected) {
		t.Errorf("DST Qualifier failed. Got %s", gotDST)
	}
}

func normalize(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\t", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}
