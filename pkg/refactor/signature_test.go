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

// parseFuncDecl helper parses a function string into an AST FuncDecl.
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

// parseDstFuncDecl helper parses a function string into a DST FuncDecl.
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

	// Wrap in a file to satisfy strict decorator.Fprint signature requirement
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

	// Basic string cleanup to extract just the func
	// Output is "package p\n\nfunc ..."
	s := buf.String()
	s = strings.TrimPrefix(s, "package p")
	return strings.TrimSpace(s)
}

// TestAddErrorToSignature checks various scenarios for AST signature modification.
func TestAddErrorToSignature(t *testing.T) {
	tests := getSignatureTests()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset, decl := parseFuncDecl(t, tt.input)

			_, err := AddErrorToSignature(fset, decl)
			if (err != nil) != tt.wantErr {
				t.Fatalf("AddErrorToSignature() error = %v, wantErr %v", err, tt.wantErr)
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

// TestAddErrorToSignatureDST checks scenarios for DST signature modification.
func TestAddErrorToSignatureDST(t *testing.T) {
	tests := getSignatureTests()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decl := parseDstFuncDecl(t, tt.input)

			_, err := AddErrorToSignatureDST(decl)
			if (err != nil) != tt.wantErr {
				t.Fatalf("AddErrorDST() error = %v, wantErr %v", err, tt.wantErr)
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

// TestEnsureNamedReturns tests the naming logic (AST).
func TestEnsureNamedReturns(t *testing.T) {
	tests := getEnsureNamedTests()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset, decl := parseFuncDecl(t, tt.input)

			changed, err := EnsureNamedReturns(fset, decl, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("EnsureNamedReturns() error = %v, wantErr %v", err, tt.wantErr)
			}
			if changed != tt.wantChanged {
				t.Errorf("Changed=%v, want %v", changed, tt.wantChanged)
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

// TestEnsureNamedReturnsDST tests naming logic on DST nodes.
func TestEnsureNamedReturnsDST(t *testing.T) {
	tests := getEnsureNamedTests()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decl := parseDstFuncDecl(t, tt.input)

			changed, err := EnsureNamedReturnsDST(decl)
			if (err != nil) != tt.wantErr {
				t.Fatalf("DST error = %v", err)
			}
			if changed != tt.wantChanged {
				t.Errorf("DST Changed=%v, want %v", changed, tt.wantChanged)
			}

			if got := renderDst(decl); normalize(got) != normalize(tt.expected) {
				t.Errorf("DST Mismatch.\nGot:\n%s\nWant:\n%s", got, tt.expected)
			}
		})
	}
}

func TestEnsureNamedReturns_NilDecl(t *testing.T) {
	_, err := EnsureNamedReturns(token.NewFileSet(), nil, nil)
	if err == nil {
		t.Error("expected error for nil decl, got nil")
	}
	_, err2 := EnsureNamedReturnsDST(nil)
	if err2 == nil {
		t.Error("expected error for nil decl DST")
	}
}

func TestAddErrorToSignature_NilDecl(t *testing.T) {
	_, err := AddErrorToSignature(token.NewFileSet(), nil)
	if err == nil {
		t.Error("expected error for nil decl, got nil")
	}
	_, err2 := AddErrorToSignatureDST(nil)
	if err2 == nil {
		t.Error("expected error for nil decl DST")
	}
}

// Checks if heursitics work for special types via NameForDstExpr local logic
func TestEnsureNamedReturnsDST_Heuristics(t *testing.T) {
	src := "func A() (context.Context, int) { return nil, 1 }"
	decl := parseDstFuncDecl(t, src)
	changed, _ := EnsureNamedReturnsDST(decl)
	if !changed {
		t.Error("Should change")
	}
	out := renderDst(decl)
	// Expected: ctx, i
	if normalize(out) != normalize("func A() (ctx context.Context, i int) { return nil, 1 }") {
		t.Errorf("Heuristics failed. Got: %s", out)
	}
}

// --- Data ---

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
			name:     "VoidFunctionWithCode",
			input:    "func A() { fmt.Println() }",
			expected: "func A() error { fmt.Println(); return nil }",
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
			name:     "NamedResult_Anonymize_NoUsage",
			input:    "func A() (res int) { return 1 }",
			expected: "func A() (int, error) { return 1, nil }",
		},
		{
			name:     "NakedReturn_PreserveNames",
			input:    "func A() (x int) { x=1; return }",
			expected: "func A() (x int, err error) { x = 1; return }",
		},
		{
			name:  "MixedControlFlow",
			input: "func A() { if true { return }; fmt.Print() }",
			// Removed semicolons as go/format (and normalization) does not preserve them in blocks with whitespace
			expected: `func A() error { if true { return nil } fmt.Print() return nil }`,
		},
		{
			name:     "CollisionParam_Anonymized",
			input:    "func A(err int) (res int) { return 1 }",
			expected: "func A(err int) (int, error) { return 1, nil }",
		},
		{
			name:     "CollisionResult_PreserveNames",
			input:    "func A() (err int) { err=1; return }",
			expected: "func A() (err int, err1 error) { err = 1; return }",
		},
		{
			name:     "MultiResult_PartialUsage",
			input:    "func A() (a, b int) { a = 1; return a, 2 }",
			expected: "func A() (int, int, error) { var a int; a = 1; return a, 2, nil }",
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
			name:        "AlreadyNamed",
			input:       "func A() (x int) { return x }",
			expected:    "func A() (x int) { return x }",
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
		{
			name:        "UnnamedMixed",
			input:       "func A() (int, string) { return 1, \"\" }",
			expected:    "func A() (i int, s string) { return 1, \"\" }",
			wantChanged: true,
		},
		{
			name:        "UnnamedMixedWithError",
			input:       "func A() (int, error) { return 1, nil }",
			expected:    "func A() (i int, err error) { return 1, nil }",
			wantChanged: true,
		},
		{
			name:        "UnnamedMultipleError",
			input:       "func A() (error, error) { return nil, nil }",
			expected:    "func A() (err error, err1 error) { return nil, nil }",
			wantChanged: true,
		},
	}
}

// normalize removes whitespace for comparison.
func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
