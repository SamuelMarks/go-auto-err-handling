package runner

import (
	"bytes"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestFormatAST_AppliesGoImports verification.
// We provide AST containing usage of "fmt" but NO import "fmt".
// We expect formatAST to return valid Go code with the import added.
func TestFormatAST_AppliesGoImports(t *testing.T) {
	src := `package main

func main() {
	fmt.Println("Hello")
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", src, 0)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	// Pre-check: Ensure import does not exist in AST
	if len(f.Imports) != 0 {
		t.Fatal("Source already has imports")
	}

	// 1. Run formatAST
	outBytes, err := formatAST(fset, f, "main.go")
	if err != nil {
		t.Fatalf("formatAST failed: %v", err)
	}
	out := string(outBytes)

	// 2. Validate Import Added
	if !strings.Contains(out, `import "fmt"`) {
		t.Errorf("Expected 'import \"fmt\"' to be added by goimports. Content:\n%s", out)
	}

	// 3. Validate Formatting
	// go/format produces "fmt.Println...", goimports might organize spacing.
	// We check basic structure.
	if !strings.Contains(out, "package main") {
		t.Error("Missing package declaration")
	}
}

// TestFormatAST_HandlesFormatting checks messy AST spacing is fixed.
func TestFormatAST_HandlesFormatting(t *testing.T) {
	// We use multiple statements to ensure lines are created
	src := `package main
import "fmt"
func main() { 
x := 1
fmt.Println(x) 
}`

	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, "main.go", src, 0)

	outBytes, err := formatAST(fset, f, "main.go")
	if err != nil {
		t.Fatal(err)
	}
	out := string(outBytes)

	// Expect standard Go formatting: function body on new lines
	expectedSubset := `func main() {
	x := 1
	fmt.Println(x)
}`
	if !strings.Contains(out, expectedSubset) {
		t.Errorf("Expected standard formatting. Got:\n%s", out)
	}
}

// TestFormatAST_SyntaxError handles cases where AST is valid but generated code might be weird (edge case).
// Actually if AST is valid, format.Node gives valid Code.
// If imports.Process fails (e.g. valid syntax but ambiguous imports), it returns error.
func TestFormatAST_InvalidProcessing(t *testing.T) {
	// Code with usage of unknown package that cannot be resolved
	src := `package main
func main() {
	unknownpkg.DoSomething()
}
`
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, "main.go", src, 0)

	// imports.Process usually does NOT fail on missing imports it can't resolve;
	// it just leaves them alone. It fails on syntax errors in input bytes.
	// Since format.Node produces valid syntax from valid AST, this is hard to trigger failure
	// unless we mess up the filename or similar.

	_, err := formatAST(fset, f, "main.go")
	if err != nil {
		// It shouldn't error just for missing pkg
		t.Fatalf("formatAST unexpectedly failed on unknown pkg: %v", err)
	}
}

// TestFormatAST_Empty ensures empty AST/File works.
func TestFormatAST_Empty(t *testing.T) {
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, "empty.go", "package empty", 0)

	var buf bytes.Buffer
	if err := formatASTToBuf(fset, f, "empty.go", &buf); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Error("Expected output")
	}
}

func formatASTToBuf(fset *token.FileSet, node interface{}, filename string, buf *bytes.Buffer) error {
	b, err := formatAST(fset, node, filename)
	if err != nil {
		return err
	}
	buf.Write(b)
	return nil
}
