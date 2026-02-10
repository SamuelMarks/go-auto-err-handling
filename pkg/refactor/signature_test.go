package refactor

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
)

func parseFuncDecl(t *testing.T, src string) (*token.FileSet, *ast.FuncDecl) {
	fset := token.NewFileSet()
	fileSrc := "package p\n" + src
	file, err := parser.ParseFile(fset, "", fileSrc, 0)
	if err != nil {
		t.Fatalf("parser failed: %v", err)
	}
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok {
			return fset, fd
		}
	}
	t.Fatalf("no function declaration found")
	return nil, nil
}

func parseDstFuncDecl(t *testing.T, src string) *dst.FuncDecl {
	fileSrc := "package p\n" + src
	file, err := decorator.Parse(fileSrc)
	if err != nil {
		t.Fatalf("decorator failed: %v", err)
	}
	for _, decl := range file.Decls {
		if fd, ok := decl.(*dst.FuncDecl); ok {
			return fd
		}
	}
	t.Fatalf("no func decl in dst")
	return nil
}

func render(fset *token.FileSet, node ast.Node) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, node); err != nil {
		return ""
	}
	return buf.String()
}

func renderDst(node dst.Node) string {
	var buf bytes.Buffer
	decl, ok := node.(dst.Decl)
	if !ok {
		return ""
	}
	file := &dst.File{
		Name:  dst.NewIdent("p"),
		Decls: []dst.Decl{decl},
	}
	if err := decorator.Fprint(&buf, file); err != nil {
		return ""
	}
	s := buf.String()
	s = strings.TrimPrefix(s, "package p")
	return strings.TrimSpace(s)
}

func TestAddErrorToSignature(t *testing.T) {
	tests := getSignatureTests()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset, decl := parseFuncDecl(t, tt.input)
			_, err := AddErrorToSignature(fset, decl)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Error %v", err)
			}
			if tt.wantErr {
				return
			}
			if got := render(fset, decl); normalize(got) != normalize(tt.expected) {
				t.Errorf("Mismatch.\nGot:\n%s\nWant:\n%s", got, tt.expected)
			}
		})
	}
}

func TestAddErrorToSignatureDST(t *testing.T) {
	tests := getSignatureTests()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decl := parseDstFuncDecl(t, tt.input)
			_, err := AddErrorToSignatureDST(decl)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Error %v", err)
			}
			if tt.wantErr {
				return
			}
			if got := renderDst(decl); normalize(got) != normalize(tt.expected) {
				t.Errorf("DST Mismatch.\nGot:\n%s\nWant:\n%s", got, tt.expected)
			}
		})
	}
}

func TestEnsureNamedReturns(t *testing.T) {
	tests := getEnsureNamedTests()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset, decl := parseFuncDecl(t, tt.input)
			changed, err := EnsureNamedReturns(fset, decl, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Error %v", err)
			}
			if changed != tt.wantChanged {
				t.Errorf("Changed %v", changed)
			}
			if tt.wantErr {
				return
			}
			if got := render(fset, decl); normalize(got) != normalize(tt.expected) {
				t.Errorf("Mismatch. Got %s", got)
			}
		})
	}
}

func TestEnsureNamedReturnsDST(t *testing.T) {
	tests := getEnsureNamedTests()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decl := parseDstFuncDecl(t, tt.input)
			changed, err := EnsureNamedReturnsDST(decl)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Error %v", err)
			}
			if changed != tt.wantChanged {
				t.Errorf("Changed %v", changed)
			}
			if got := renderDst(decl); normalize(got) != normalize(tt.expected) {
				t.Errorf("Mismatch. Got %s", got)
			}
		})
	}
}

func TestEnsureNamedReturns_NilDecl(t *testing.T) {
	_, err := EnsureNamedReturns(token.NewFileSet(), nil, nil)
	if err == nil {
		t.Error("Expected error")
	}
	_, err2 := EnsureNamedReturnsDST(nil)
	if err2 == nil {
		t.Error("Expected error")
	}
}

func TestAddErrorToSignature_NilDecl(t *testing.T) {
	_, err := AddErrorToSignature(token.NewFileSet(), nil)
	if err == nil {
		t.Error("Expected error")
	}
	_, err2 := AddErrorToSignatureDST(nil)
	if err2 == nil {
		t.Error("Expected error")
	}
}

func TestAddErrorToFuncTypeDST(t *testing.T) {
	ft := &dst.FuncType{Params: &dst.FieldList{}}
	changed, err := AddErrorToFuncTypeDST(ft)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("Expected change")
	}
	if len(ft.Results.List) != 1 {
		t.Error("Expected added result")
	}
}

func TestEnsureNamedReturnsDST_Heuristics(t *testing.T) {
	src := "func A() (context.Context, int) { return nil, 1 }"
	decl := parseDstFuncDecl(t, src)
	changed, _ := EnsureNamedReturnsDST(decl)
	if !changed {
		t.Error("Should change")
	}
	out := renderDst(decl)
	if normalize(out) != normalize("func A() (ctx context.Context, i int) { return nil, 1 }") {
		t.Errorf("Heuristics failed. Got: %s", out)
	}
}

type sigTest struct {
	name     string
	input    string
	expected string
	wantErr  bool
}

func getSignatureTests() []sigTest {
	return []sigTest{
		{
			name:     "VoidFunction",
			input:    "func A() {}",
			expected: "func A() error { return nil }",
		},
		{
			name:     "VoidFunctionWithReturn",
			input:    "func A() { return }",
			expected: "func A() error { return nil }",
		},
		{
			name:     "UnnamedResult",
			input:    "func A() int { return 1 }",
			expected: "func A() (int, error) { return 1, nil }",
		},
		{
			name:     "NamedResult_Anonymize_WithUsage",
			input:    "func A() (res int) { res = 1; return res }",
			expected: "func A() (int, error) { var res int; res = 1; return res, nil }",
		},
		{
			name:     "NakedReturn_PreserveNames",
			input:    "func A() (x int) { x=1; return }",
			expected: "func A() (x int, err error) { x = 1; return }",
		},
	}
}

type namedTest struct {
	name        string
	input       string
	expected    string
	wantChanged bool
	wantErr     bool
}

func getEnsureNamedTests() []namedTest {
	return []namedTest{
		{
			name:        "Void",
			input:       "func A() {}",
			expected:    "func A() {}",
			wantChanged: false,
		},
		{
			name:        "UnnamedSingle",
			input:       "func A() int { return 1 }",
			expected:    "func A() (i int) { return 1 }",
			wantChanged: true,
		},
		{
			name:        "UnnamedError",
			input:       "func A() error { return nil }",
			expected:    "func A() (err error) { return nil }",
			wantChanged: true,
		},
	}
}

func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
