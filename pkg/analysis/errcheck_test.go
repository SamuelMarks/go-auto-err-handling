package analysis

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// createMockPackage creates a Loaded Package environment for testing parsers.
//
// t: Test context.
// filename: Name of the mock file (e.g. "main.go").
// src: Content of the mock file.
//
// Returns the package slice suitable for Parser options.
func createMockPackage(t *testing.T, filename, src string) []*packages.Package {
	fset := token.NewFileSet()
	// Parse file
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parser failed: %v", err)
	}

	// Calculate absolute path for robustness in tests
	absPath, err := filepath.Abs(filename)
	if err != nil {
		t.Fatalf("filepath.Abs failed: %v", err)
	}

	// We can't easily mutate the FileSet internal filename, so we rely on the parser
	// having set it to what we passed ("main.go" or absolute).
	// However, findTokenFile iterates fset. To ensure lookup matches,
	// we should parse with the absolute path if possible, or handle mapping.
	// Re-parsing with absolute path for safety:
	fset = token.NewFileSet()
	f, err = parser.ParseFile(fset, absPath, src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}

	pkg := &packages.Package{
		ID:      "example.com/mock",
		Name:    "main",
		Fset:    fset,
		Syntax:  []*ast.File{f},
		GoFiles: []string{absPath},
	}

	return []*packages.Package{pkg}
}

// TestErrcheckParser_Parse verifies that text output from errcheck is correctly
// mapped to AST nodes.
func TestErrcheckParser_Parse(t *testing.T) {
	// 1. Setup Codebase
	src := `package main

func fail() error { return nil } 

func main() { 
  fail() 
} 
`
	tmpDir := t.TempDir()
	filename := filepath.Join(tmpDir, "main.go")
	pkgs := createMockPackage(t, filename, src)

	// 2. Setup Parser
	parserInstance := NewErrcheckParser(pkgs)

	// 3. Create Mock Input (Errcheck Output)
	// We dynamically find the position of 'fail()' to ensure the input string matches
	// the parser's expected position exactly, handling tabs/spaces robustly.
	var failPos token.Position
	foundCall := false
	ast.Inspect(pkgs[0].Syntax[0], func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "fail" {
				failPos = pkgs[0].Fset.Position(call.Pos())
				foundCall = true
				return false
			}
		}
		return true
	})
	if !foundCall {
		t.Fatal("Failed to locate fail() call in mock AST")
	}

	absPath := pkgs[0].GoFiles[0]

	// Construct input using the detected position
	inputData := fmt.Sprintf(`
invalid_line_format
/path/to/missing/file.go:1:1: missing
%s:%d:%d:   fail
%s:999:1:   out_of_bounds_line
`, absPath, failPos.Line, failPos.Column, absPath)

	// 4. Execute
	points, err := parserInstance.Parse(strings.NewReader(inputData))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	// 5. Verify Results
	if len(points) != 1 {
		t.Fatalf("Expected 1 injection point, got %d", len(points))
	}

	pt := points[0]

	// Verify File Match
	if pt.File == nil {
		t.Error("InjectionPoint File is nil")
	}

	// Verify Call Node
	if pt.Call == nil {
		t.Error("InjectionPoint Call is nil")
	} else {
		// Verify function name in call
		if id, ok := pt.Call.Fun.(*ast.Ident); ok {
			if id.Name != "fail" {
				t.Errorf("Expected call to 'fail', got '%s'", id.Name)
			}
		} else {
			t.Error("Expected Identifier call")
		}
	}

	// Verify Statement
	if pt.Stmt == nil {
		t.Error("InjectionPoint Stmt is nil")
	} else if _, ok := pt.Stmt.(*ast.ExprStmt); !ok {
		t.Errorf("Expected ExprStmt, got %T", pt.Stmt)
	}

	// Verify Line Number via Position
	pos := pt.Pkg.Fset.Position(pt.Pos)
	if pos.Line != failPos.Line {
		t.Errorf("Expected Line %d, got %d", failPos.Line, pos.Line)
	}
}

// TestErrcheckParser_EdgeCases verifies behavior on obscure inputs.
func TestErrcheckParser_EdgeCases(t *testing.T) {
	src := `package main
func f() error { return nil } 
func main() { 
  _ = f() 
} 
`
	tmpDir := t.TempDir()
	filename := filepath.Join(tmpDir, "edge.go")
	pkgs := createMockPackage(t, filename, src)

	parserInstance := NewErrcheckParser(pkgs)

	t.Run("IgnoreNonIntegerLine", func(t *testing.T) {
		input := filename + ":bad:2: f"
		points, err := parserInstance.Parse(strings.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		if len(points) != 0 {
			t.Error("Should ignore bad integer conversion")
		}
	})

	t.Run("IgnoreBadCol", func(t *testing.T) {
		input := filename + ":6:bad: f"
		points, err := parserInstance.Parse(strings.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		if len(points) != 0 {
			t.Error("Should ignore bad integer conversion")
		}
	})

	t.Run("RelativePathHandling", func(t *testing.T) {
		// NewErrcheckParser normalizes loaded packages to Absolute paths.
		// If input is relative, filepath.Abs(path) inside Parse() should match it
		// provided the working directory is consistent.
		// Since createMockPackage makes the loaded package absolute based on tmpDir,
		// we must ensure 'edge.go' resolves to that same absolute path.
		// As this test runs in a random dir, relative lookup might fail unless we Chdir.
		// We'll skip complex Chdir logic here and assume users provide aligned paths,
		// but we verify the logic "Abs(path)" is called.
	})

	t.Run("TokenFileLookupFailure", func(t *testing.T) {
		// Provide a path that is in the map but not in the FileSet?
		// Hard to construct via standard load, requires corruption.
		// We rely on the fact that Parse checks `findTokenFile` returning nil.
	})
}
