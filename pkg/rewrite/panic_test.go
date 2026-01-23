package rewrite

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/imports"
)

// helper to format output code
func formatCode(fset *token.FileSet, file *ast.File) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return ""
	}
	// Run imports to clean up fmt
	out, err := imports.Process("main.go", buf.Bytes(), nil)
	if err != nil {
		return buf.String()
	}
	return string(out)
}

// normalize removes whitespace and comments to ensure robust comparisons
func normalize(s string) string {
	// Strip comments first to avoid whitespace removal merging them with code
	lines := strings.Split(s, "\n")
	var codeLines []string
	for _, line := range lines {
		if idx := strings.Index(line, "//"); idx != -1 {
			line = line[:idx]
		}
		codeLines = append(codeLines, line)
	}
	s = strings.Join(codeLines, "")

	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\t", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

func TestRewritePanics(t *testing.T) {
	// We parse and mockcheck a file to get TypesInfo populated sufficiently for panic analysis
	src := `package main

import "errors" 

func panicString() { 
  panic("fail") 
} 

func panicError() { 
  panic(errors.New("fail")) 
} 

func panicOther() { 
  panic(123) 
} 

// Function that already has error return
func existingError() error { 
  if true { 
    panic("boom") 
  } 
  return nil
} 

// Function with multiple returns
func complex() int { 
  panic("c") 
} 
`

	file, pkg := parseAndCheck(t, src)
	injector := NewInjector(pkg, "", "")

	changed, err := injector.RewritePanics(file)
	if err != nil {
		t.Fatalf("RewritePanics error: %v", err)
	}
	if !changed {
		t.Error("Expected changes, got none")
	}

	out := formatCode(injector.Fset, file)
	normOut := normalize(out)

	// Case 1: Panic String -> fmt.Errorf
	if !strings.Contains(normOut, `returnfmt.Errorf("%s","fail")`) {
		t.Errorf("panicString not rewritten correctly. Got:\n%s", out)
	}

	// Case 2: Panic Error -> return err
	if !strings.Contains(out, "func panicError() error") {
		t.Error("Signature for panicError not updated")
	}
	if !strings.Contains(normOut, `returnerrors.New("fail")`) {
		t.Error("Did not return error expression directly")
	}

	// Case 3: Panic Other -> fmt.Errorf("%v")
	if !strings.Contains(normOut, `returnfmt.Errorf("%v",123)`) {
		t.Errorf("panicOther not rewritten to %%v format. Got:\n%s", out)
	}

	// Case 4: Existing Error -> Don't double signature
	if strings.Contains(out, "func existingError() (error, error)") {
		t.Error("existingError signature doubled")
	}
	if !strings.Contains(normOut, `returnfmt.Errorf("%s","boom")`) {
		t.Error("existingError panic not rewritten")
	}

	// Case 5: Complex (int) -> (int, error) + Zero Value
	if !strings.Contains(out, "func complex() (int, error)") {
		t.Error("complex signature not updated correctly")
	}
	if !strings.Contains(normOut, `return0,fmt.Errorf("%s","c")`) {
		t.Errorf("complex return not generating zero values correctly. Got:\n%s", out)
	}
}

func TestRewritePanics_NoArgs(t *testing.T) {
	// panic() without args is usually invalid in modern Go but technically AST parses it.
	// Our logic throws error.
	src := `package main
func empty() { 
  panic() 
} 
`
	// We skip strict type check for this simple parse
	fset := token.NewFileSet()
	file, _ := parser.ParseFile(fset, "", src, 0)

	pkg := &packages.Package{Fset: fset, TypesInfo: nil} // No types info
	injector := NewInjector(pkg, "", "")

	_, err := injector.RewritePanics(file)
	if err == nil {
		t.Error("Expected error for panic() with no args")
	}
}

func TestRewritePanics_NestedFunc(t *testing.T) {
	// Ensure we don't accidentally rewrite nested closures unless intended (logic says skip nested)
	src := `package main
func main() { 
  _ = func() { 
    panic("nested") 
  } 
} 
`
	file, pkg := parseAndCheck(t, src)
	injector := NewInjector(pkg, "", "")

	changed, err := injector.RewritePanics(file)
	if err != nil {
		t.Fatal(err)
	}

	// Expect NO change because we skip nested func lits in top-level traversal
	if changed {
		t.Error("Should not have modified nested closure")
	}

	out := formatCode(injector.Fset, file)
	if !strings.Contains(out, `panic("nested")`) {
		t.Error("Nested panic should remain")
	}
}
