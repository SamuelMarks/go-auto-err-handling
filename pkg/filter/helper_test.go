package filter

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// parseFuncDeclHelper parses a source string into a FuncDecl.
func parseFuncDeclHelper(t *testing.T, src string) *ast.FuncDecl {
	fset := token.NewFileSet()
	// Wrap in package context
	fullSrc := "package p\n" + src
	f, err := parser.ParseFile(fset, "", fullSrc, 0)
	if err != nil {
		t.Fatalf("parser error: %v", err)
	}
	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok {
			return fd
		}
	}
	return nil
}

// TestIsTestHelper verifies detection of functions calling t.Helper().
func TestIsTestHelper(t *testing.T) {
	tests := []struct {
		name         string
		src          string
		expectHelper bool
		expectParam  string
	}{
		{
			name: "StandardHelperT",
			src: `func MyHelper(t *testing.T) {
				t.Helper()
				t.Log("foo")
			}`,
			expectHelper: true,
			expectParam:  "t",
		},
		{
			name: "StandardHelperB",
			src: `func BenchmarkHelper(b *testing.B) {
				b.Helper()
			}`,
			expectHelper: true,
			expectParam:  "b",
		},
		{
			name: "HelperWithArgs",
			src: `func Assert(t *testing.T, val int) {
				t.Helper()
			}`,
			expectHelper: true,
			expectParam:  "t",
		},
		{
			name: "CustomName",
			src: `func Check(ctrl *testing.T) {
				ctrl.Helper()
			}`,
			expectHelper: true,
			expectParam:  "ctrl",
		},
		{
			name: "NotHelper_NoCall",
			src: `func Check(t *testing.T) {
				t.Log("fail")
			}`,
			expectHelper: false,
			expectParam:  "",
		},
		{
			name: "NotHelper_WrongType",
			src: `func Check(i int) {
			}`,
			expectHelper: false,
			expectParam:  "",
		},
		{
			name: "NotHelper_NestedCall",
			src: `func Check(t *testing.T) {
				func() {
					t.Helper() // Nested closure, scope barrier
				}()
			}`,
			expectHelper: false,
			expectParam:  "t", // Param exists, but call is invalid/hidden
		},
		{
			name: "NotHelper_NotTestingPkg",
			src: `func Check(t *other.T) {
				t.Helper()
			}`,
			expectHelper: false,
			expectParam:  "",
		},
		{
			name: "Helper_NotPointer",
			src: `func Check(t testing.T) {
				t.Helper()
			}`,
			expectHelper: false,
			expectParam:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decl := parseFuncDeclHelper(t, tt.src)
			isHelper, param := IsTestHelper(decl)

			if isHelper != tt.expectHelper {
				t.Errorf("IsTestHelper() = %v, want %v", isHelper, tt.expectHelper)
			}
			// Only check param name if we expect it to be identified as having a test param
			// (IsTestHelper returns false but might have found param name internally,
			// but public return is only valid if isHelper is true usually,
			// however implementation returns param name if found in first pass only if logic aligns.
			// Actually our implementation returns param if found, wait:
			// Implementation returns `testParamName` if scan succeeds.
			// But if scan fails, it returns `testParamName`?
			// The code: 'return hasHelperCall, testParamName'.
			// So if hasHelperCall is false, but testParamName was found in signature, it returns name.
			// Let's verify expectation logic.)

			if tt.expectParam != "" && param != tt.expectParam {
				t.Errorf("Expected param %q, got %q", tt.expectParam, param)
			}
		})
	}
}

// TestIsTestHelper_Nil verifies safety on nil inputs.
func TestIsTestHelper_Nil(t *testing.T) {
	ok, _ := IsTestHelper(nil)
	if ok {
		t.Error("Expected false for nil decl")
	}
	emptyBody := &ast.FuncDecl{Type: &ast.FuncType{}}
	ok, _ = IsTestHelper(emptyBody)
	if ok {
		t.Error("Expected false for nil body")
	}
}
