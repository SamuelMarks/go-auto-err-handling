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
//
// t: The testing context.
// src: The source string of the function declaration.
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
//
// fset: The FileSet associated with the node.
// node: The AST node to render.
func render(fset *token.FileSet, node ast.Node) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, node); err != nil {
		return ""
	}
	return buf.String()
}

// TestAddErrorToSignature checks various scenarios for signature modification,
// specifically focusing on the logic that enforces anonymous returns ("Anonymous Mode")
// when possible.
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
			// Previously this would yield (res int, err error).
			// Now "Anonymous Mode" should trigger because 'res' is used but no naked return.
			// It should strip 'res', inject 'var res int', and return (int, error).
			name:  "NamedResult_Anonymize_WithUsage",
			input: "func A() (res int) { res = 1; return res }",
			// Variable usage "res = 1" implies we need to keep the variable.
			// Signature becomes anonymous. Var injected.
			expected: "func A() (int, error) { var res int; res = 1; return res, nil }",
		},
		{
			// Named result unused in body.
			// "Anonymous Mode" should strip it entirely without injecting var.
			name:     "NamedResult_Anonymize_NoUsage",
			input:    "func A() (res int) { return 1 }",
			expected: "func A() (int, error) { return 1, nil }",
		},
		{
			// Naked return present: MUST PRESERVE NAMES.
			name:     "NakedReturn_PreserveNames",
			input:    "func A() (x int) { x=1; return }",
			expected: "func A() (x int, err error) { x = 1; return }",
		},
		{
			// Mixed Control Flow
			name:  "MixedControlFlow",
			input: "func A() { if true { return }; fmt.Print() }",
			// Adjust expected string indentation to match standard format
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
			// Collision detection normally handled by generating "err1",
			// but here "Anonymous Mode" should trigger, stripping "err" int param.
			// Body uses "err", so we inject "var err int".
			// Signature becomes (int, error). No name collision in sig.
			name:     "CollisionParam_Anonymized",
			input:    "func A(err int) (res int) { return 1 }",
			expected: "func A(err int) (int, error) { return 1, nil }",
		},
		{
			// If Naked Return forces names, THEN collision logic applies.
			// Input has result named "err".
			name:     "CollisionResult_PreserveNames",
			input:    "func A() (err int) { err=1; return }", // Naked -> Keep names
			expected: "func A() (err int, err1 error) { err = 1; return }",
		},
		{
			// Edge case: Multiple named returns, some used.
			// Input: (a, b int). a used. b unused. Explicit return.
			// Anonymize -> (int, int, error). Inject var a. Drop b.
			name:     "MultiResult_PartialUsage",
			input:    "func A() (a, b int) { a = 1; return a, 2 }",
			expected: "func A() (int, int, error) { var a int; a = 1; return a, 2, nil }",
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
	// Note: We use AST-based fallback in these tests because setting up full TypesInfo
	// for simple strings requires go/types.Config{}.Check(), which is tested in naming_test.go.
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
			expected:    "func A() (i int) { return 1 }", // AST int -> i
			wantChanged: true,
		},
		{
			name:        "UnnamedError",
			input:       "func A() error { return nil }",
			expected:    "func A() (err error) { return nil }", // AST error -> err
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
			expected:    "func A() (err error, err1 error) { return nil, nil }", // err, err1 collision check
			wantChanged: true,
		},
		{
			name:        "UnnamedCollisionStrings",
			input:       "func A() (string, string) { return \"\", \"\" }",
			expected:    "func A() (s string, s1 string) { return \"\", \"\" }",
			wantChanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset, decl := parseFuncDecl(t, tt.input)

			changed, err := EnsureNamedReturns(fset, decl, nil)
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
	_, err := EnsureNamedReturns(token.NewFileSet(), nil, nil)
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
