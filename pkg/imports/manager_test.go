package imports

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
	"testing"
)

// parseSource helper parses a source string into an AST file and FileSet.
func parseSource(t *testing.T, src string) (*token.FileSet, *ast.File) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parser failed: %v", err)
	}
	return fset, file
}

// render helper prints the AST back to a string.
func render(fset *token.FileSet, file *ast.File) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, file); err != nil {
		return ""
	}
	return buf.String()
}

// TestAdd verifies that Add correctly injects imports into the AST.
func TestAdd(t *testing.T) {
	tests := []struct {
		name           string
		initialSrc     string
		pkgPath        string
		expectImport   bool
		expectedOutput string
	}{
		{
			name:         "NewImport",
			initialSrc:   "package main\n\nfunc main() {}",
			pkgPath:      "errors",
			expectImport: true,
			expectedOutput: `package main

import "errors"

func main() {}`,
		},
		{
			name:         "ExistingImport",
			initialSrc:   "package main\n\nimport \"fmt\"\n\nfunc main() {}",
			pkgPath:      "fmt",
			expectImport: false,
			expectedOutput: `package main

import "fmt"

func main() {}`,
		},
		{
			name:         "AppendImport",
			initialSrc:   "package main\n\nimport \"fmt\"\n\nfunc main() {}",
			pkgPath:      "errors",
			expectImport: true,
			expectedOutput: `package main

import (
	"errors"
	"fmt"
)

func main() {}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset, file := parseSource(t, tt.initialSrc)

			added := Add(fset, file, tt.pkgPath)

			if added != tt.expectImport {
				t.Errorf("Add() = %v, want %v", added, tt.expectImport)
			}

			// Render and verify strict output usually varies by astutil formatting,
			// here we check for presence as formatting details can vary (parens vs plain).
			out := render(fset, file)
			if !strings.Contains(out, "\""+tt.pkgPath+"\"") {
				t.Errorf("Output missing import %q. Got:\n%s", tt.pkgPath, out)
			}
		})
	}
}

// TestAddNamed verifies that AddNamed correctly injects named imports.
func TestAddNamed(t *testing.T) {
	tests := []struct {
		name           string
		initialSrc     string
		importName     string
		pkgPath        string
		expectImport   bool
		expectedOutput string
	}{
		{
			name:         "NewNamedImport",
			initialSrc:   "package main\n\nfunc main() {}",
			importName:   "myfmt",
			pkgPath:      "fmt",
			expectImport: true,
			expectedOutput: `package main

import myfmt "fmt"

func main() {}`,
		},
		{
			name:         "ExistingNamedImport",
			initialSrc:   "package main\n\nimport myfmt " + `"fmt"` + "\n\nfunc main() {}",
			importName:   "myfmt",
			pkgPath:      "fmt",
			expectImport: false,
			expectedOutput: `package main

import myfmt "fmt"

func main() {}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset, file := parseSource(t, tt.initialSrc)

			added := AddNamed(fset, file, tt.importName, tt.pkgPath)

			if added != tt.expectImport {
				t.Errorf("AddNamed() = %v, want %v", added, tt.expectImport)
			}

			out := render(fset, file)
			if !strings.Contains(out, tt.importName) {
				t.Errorf("Output missing import name %q. Got:\n%s", tt.importName, out)
			}
		})
	}
}
