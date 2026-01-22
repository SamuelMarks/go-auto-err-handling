package refactor

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"testing"
)

// parseFuncDecl helper parses a function string into an AST FuncDecl.
func parseFuncDecl(t *testing.T, src string) (*token.FileSet, *ast.FuncDecl) {
	fset := token.NewFileSet()
	// Wrap in package to be valid file
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

// render helper formats the node back to string.
func render(fset *token.FileSet, node ast.Node) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, node); err != nil {
		return ""
	}
	return buf.String()
}

func TestAddErrorToSignature(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
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
			name:     "NamedResult",
			input:    "func A() (res int) { res = 1; return }",
			expected: "func A() (res int, err error) { res = 1; return }",
		},
		{
			name:     "NamedResultExplicit",
			input:    "func A() (res int) { return 1 }",
			expected: "func A() (res int, err error) { return 1, nil }",
		},
		{
			name:  "MixedControlFlow",
			input: "func A() { if true { return }; fmt.Print() }",
			expected: `func A() error {
	if true {
		return nil
	}
	fmt.Print()
	return nil
}`,
		},
		{
			name:     "NestedFunctionIgnored",
			input:    "func A() { f := func() int { return 1 }; f() }",
			expected: "func A() error { f := func() int { return 1 }; f(); return nil }",
		},
		{
			name:     "ExistingErrorShouldDouble", // Technical behavior check: it blindly adds error
			input:    "func A() error { return nil }",
			expected: "func A() (error, error) { return nil, nil }",
		},
	}

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

			if got := render(fset, decl); got != tt.expected {
				t.Errorf("Mismatch.\nGot:\n%s\nWant:\n%s", got, tt.expected)
			}
		})
	}
}

func TestEnsureNamedReturns(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expected    string
		wantChanged bool
		wantErr     bool
	}{
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
			expected:    "func A() (ret0 int) { return 1 }",
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
			expected:    "func A() (ret0 int, ret1 string) { return 1, \"\" }",
			wantChanged: true,
		},
		{
			name:        "UnnamedMixedWithError",
			input:       "func A() (int, error) { return 1, nil }",
			expected:    "func A() (ret0 int, err error) { return 1, nil }",
			wantChanged: true,
		},
		{
			name:        "UnnamedMultipleError",
			input:       "func A() (error, error) { return nil, nil }",
			expected:    "func A() (ret0 error, err error) { return nil, nil }",
			wantChanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset, decl := parseFuncDecl(t, tt.input)

			changed, err := EnsureNamedReturns(fset, decl)
			if (err != nil) != tt.wantErr {
				t.Fatalf("EnsureNamedReturns() error = %v, wantErr %v", err, tt.wantErr)
			}
			if changed != tt.wantChanged {
				t.Errorf("EnsureNamedReturns() changed = %v, want %v", changed, tt.wantChanged)
			}
			if tt.wantErr {
				return
			}

			if got := render(fset, decl); got != tt.expected {
				t.Errorf("Mismatch.\nGot:\n%s\nWant:\n%s", got, tt.expected)
			}
		})
	}
}

func TestEnsureNamedReturns_NilDecl(t *testing.T) {
	_, err := EnsureNamedReturns(token.NewFileSet(), nil)
	if err == nil {
		t.Error("expected error for nil decl, got nil")
	}
}

func TestAddErrorToSignature_NilDecl(t *testing.T) {
	_, err := AddErrorToSignature(token.NewFileSet(), nil)
	if err == nil {
		t.Error("expected error for nil decl, got nil")
	}
}
