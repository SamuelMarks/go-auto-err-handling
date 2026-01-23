package imports

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// Helper to setup environment
func setup(t *testing.T, src string) (*packages.Package, *ast.File) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}

	// Mock minimal types info
	info := &types.Info{
		Defs: make(map[*ast.Ident]types.Object),
		Uses: make(map[*ast.Ident]types.Object),
	}

	// Populate defs for local variables to simulate conflicts
	ast.Inspect(f, func(n ast.Node) bool {
		if assign, ok := n.(*ast.AssignStmt); ok {
			for _, expr := range assign.Lhs {
				if id, ok := expr.(*ast.Ident); ok {
					// Treat as variable definition
					v := types.NewVar(token.NoPos, nil, id.Name, types.Typ[types.Int])
					info.Defs[id] = v
				}
			}
		}
		return true
	})

	pkg := &packages.Package{
		Fset:      fset,
		Syntax:    []*ast.File{f},
		TypesInfo: info,
	}
	return pkg, f
}

func TestResolveAlias_NoConflict(t *testing.T) {
	src := `package main
func main() {
	x := 1
}
`
	pkg, file := setup(t, src)
	resolver := NewConflictResolver(pkg, file)

	alias, aliased := resolver.ResolveAlias("fmt", "fmt")

	if aliased {
		t.Error("Expected no alias needed")
	}
	if alias != "fmt" {
		t.Errorf("Expected 'fmt', got '%s'", alias)
	}
}

func TestResolveAlias_Conflict(t *testing.T) {
	src := `package main
func main() {
	fmt := "local variable"
	_ = fmt
}
`
	pkg, file := setup(t, src)
	resolver := NewConflictResolver(pkg, file)

	// We request "fmt". Local var "fmt" exists. Should generate alias.
	alias, aliased := resolver.ResolveAlias("fmt", "fmt")

	if !aliased {
		t.Error("Expected usage to be aliased due to conflict")
	}
	if !strings.HasPrefix(alias, "std_fmt_") {
		t.Errorf("Expected safe alias prefix, got '%s'", alias)
	}

	// Verify import was added to AST
	var buf bytes.Buffer
	tokenSet := token.NewFileSet()
	format.Node(&buf, tokenSet, file)
	out := buf.String()

	// Use flexible check because astutil behavior varies on formatting
	if !strings.Contains(out, `"fmt"`) || !strings.Contains(out, alias) {
		t.Errorf("Modified AST missing aliased import.\nAlias: %s\nOutput:\n%s", alias, out)
	}
}

func TestResolveAlias_ConflictMultiple(t *testing.T) {
	// Scenario: "fmt" and "std_fmt_1" are taken.
	src := `package main
func main() {
	fmt := 1
	std_fmt_1 := 2
}
`
	pkg, file := setup(t, src)
	resolver := NewConflictResolver(pkg, file)

	alias, aliased := resolver.ResolveAlias("fmt", "fmt")

	if !aliased {
		t.Error("Should be aliased")
	}
	if alias != "std_fmt_2" {
		t.Errorf("Expected incremented alias 'std_fmt_2', got '%s'", alias)
	}
}
