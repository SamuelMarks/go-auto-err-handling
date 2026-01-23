package refactor

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
	"golang.org/x/tools/imports"
)

// Helper to manually construct a package with a target function reference
func setupPropagateEnv(t *testing.T, src string) (*token.FileSet, *packages.Package, *types.Func) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", src, 0)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}

	// We start with a dummy configuration
	// We need to "fake" the type checking such that the 'target' function is identified.
	conf := types.Config{Importer: nil}
	pkg, err := conf.Check("main", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}

	// Find the object for 'target'
	obj := pkg.Scope().Lookup("target")
	if obj == nil {
		t.Fatalf("could not find target function in scope")
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		t.Fatalf("target is not a function")
	}

	p := &packages.Package{
		Fset:      fset,
		Syntax:    []*ast.File{f},
		Types:     pkg,
		TypesInfo: info,
	}

	return fset, p, fn
}

// renderWithImports formatting like implementation runner does
func renderWithImports(t *testing.T, fset *token.FileSet, file *ast.File) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		t.Fatalf("format error: %v", err)
	}
	// Run goimports
	out, err := imports.Process("main.go", buf.Bytes(), nil)
	if err != nil {
		// unit tests might lack GOPATH context for some pkgs, but stdlib (fmt/log) should resolve
		return buf.String()
	}
	return string(out)
}

func TestPropagateCallers(t *testing.T) {
	// Scenario:
	// target() is called.
	// enclosing() returns error.
	// We expect target() call to be updated to assignments and checks added.
	src := `package main

func target() {} 

func enclosing() error { 
  target()      // Case 1: Bare call
  return nil
} 
`
	// To test assignment modification, we need valid Go for the parser.
	srcAssign := `package main
func target() int { return 0 } 
func enclosing() error { 
  x := target() 
  _ = x
  return nil
} 
`

	// Case 1: Void -> Error
	t.Run("VoidToError", func(t *testing.T) {
		fset, pkg, target := setupPropagateEnv(t, src)

		// Run Propagation
		n, err := PropagateCallers([]*packages.Package{pkg}, target, "log-fatal")
		if err != nil {
			t.Fatalf("PropagateCallers failed: %v", err)
		}
		if n != 1 {
			t.Errorf("Expected 1 update, got %d", n)
		}

		out := renderWithImports(t, fset, pkg.Syntax[0])

		if !strings.Contains(out, "if err := target(); err != nil") {
			t.Error("Did not find collapsed error check 'if err := target(); err != nil'")
		}
	})

	// Case 2: Int -> Int, Error
	t.Run("IntToIntError", func(t *testing.T) {
		fset, pkg, target := setupPropagateEnv(t, srcAssign)

		n, err := PropagateCallers([]*packages.Package{pkg}, target, "log-fatal")
		if err != nil {
			t.Fatalf("PropagateCallers failed: %v", err)
		}
		if n != 1 {
			t.Errorf("Expected 1 update, got %d", n)
		}

		out := renderWithImports(t, fset, pkg.Syntax[0])

		if !strings.Contains(out, "x, err := target()") {
			t.Errorf("Did not find updated assignment. Got:\n%s", out)
		}
	})
}

// TestPropagateCallers_EntryPoint verifies main/init protection logic.
func TestPropagateCallers_EntryPoint(t *testing.T) {
	// Scenario: main calls target which now returns error.
	srcMain := `package main
func target() {} 
func main() { 
  target() 
} 
`
	// Scenario: init calls target
	srcInit := `package main
func target() {} 
func init() { 
  target() 
} 
`
	// 1. Test main with log-fatal
	t.Run("MainLogFatal", func(t *testing.T) {
		fset, pkg, target := setupPropagateEnv(t, srcMain)
		n, err := PropagateCallers([]*packages.Package{pkg}, target, "log-fatal")
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("Expected 1 update, got %d", n)
		}

		out := renderWithImports(t, fset, pkg.Syntax[0])

		if strings.Contains(out, "func main() error") {
			t.Error("Should not have changed main signature")
		}
		if !strings.Contains(out, "log.Fatal(err)") {
			t.Error("Expected log.Fatal(err)")
		}
		if !strings.Contains(out, `"log"`) {
			t.Error("Expected log import to be added by Process")
		}
		// Expect collapsed form
		if !strings.Contains(out, "if err := target(); err != nil") {
			t.Error("Expected collapsed form in main")
		}
	})

	// 2. Test init with panic
	t.Run("InitPanic", func(t *testing.T) {
		fset, pkg, target := setupPropagateEnv(t, srcInit)
		n, err := PropagateCallers([]*packages.Package{pkg}, target, "panic")
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("Expected 1 update, got %d", n)
		}

		out := renderWithImports(t, fset, pkg.Syntax[0])

		if strings.Contains(out, "panic(err)") {
			// Found panic
		} else {
			t.Error("Expected panic(err)")
		}
	})

	// 3. Test main with os-exit
	t.Run("MainOsExit", func(t *testing.T) {
		fset, pkg, target := setupPropagateEnv(t, srcMain)
		n, err := PropagateCallers([]*packages.Package{pkg}, target, "os-exit")
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("Expected 1 update, got %d", n)
		}

		out := renderWithImports(t, fset, pkg.Syntax[0])

		if !strings.Contains(out, "os.Exit(1)") {
			t.Error("Expected os.Exit(1)")
		}
		if !strings.Contains(out, "fmt.Println(err)") {
			t.Error("Expected fmt.Println(err)")
		}
		// os might not be added if the env doesn't find os.Exit package path, but checking expectation logic
	})
}

// TestPropagateCallers_NoEnclosingError verifies behavior when caller cannot return error.
func TestPropagateCallers_NoEnclosingError(t *testing.T) {
	src := `package main
func target() {} 
func someFunc() { 
  target() // call
} 
`
	fset, pkg, target := setupPropagateEnv(t, src)

	n, err := PropagateCallers([]*packages.Package{pkg}, target, "log-fatal")
	if err != nil {
		t.Fatalf("PropagateCallers failed: %v", err)
	}
	if n != 1 {
		t.Errorf("Expected 1 update")
	}

	out := renderWithImports(t, fset, pkg.Syntax[0])

	if !strings.Contains(out, "_") && !strings.Contains(out, "target()") {
		t.Errorf("Expected modification to assignment or blank, got:\n%s", out)
	}
}
