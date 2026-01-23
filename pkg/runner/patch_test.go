package runner

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/refactor"
)

// TestPatchSignature verifies that the patching logic correctly updates the type system
// view of a function after its AST has been modified.
func TestPatchSignature(t *testing.T) {
	// 1. Setup Environment
	src := `package main
func Target() int { return 1 }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("main", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatal(err)
	}

	// Locate Target
	var targetDecl *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "Target" {
			targetDecl = fd
		}
	}
	if targetDecl == nil {
		t.Fatal("Target func not found")
	}

	// 2. Verify Initial State
	objBefore := info.ObjectOf(targetDecl.Name)
	sigBefore := objBefore.Type().(*types.Signature)
	if sigBefore.Results().Len() != 1 {
		t.Fatalf("Expected 1 return initially, got %d", sigBefore.Results().Len())
	}

	// 3. Modify AST (Simulate refactor.AddErrorToSignature)
	// We use the simpler method directly for test isolation or the helper if available.
	// We'll trust refactor.AddErrorToSignature works per its own tests.
	if _, err := refactor.AddErrorToSignature(fset, targetDecl); err != nil {
		t.Fatal(err)
	}

	// Note: AST is now `func Target() (int, error)`.
	// But TypeInfo still points to old object/signature usually.

	// 4. Retrieve Object Again (Should still be old unless we patch)
	objStale := info.ObjectOf(targetDecl.Name)
	sigStale := objStale.Type().(*types.Signature)
	// Technically info.ObjectOf returns the same pointer, so it's the same object.
	if sigStale.Results().Len() != 1 {
		// If this fails, it means go/types magically updated? Unlikely.
		t.Fatal("Expected stale type info to still show 1 return")
	}

	// 5. Apply Patch
	if err := PatchSignature(info, targetDecl, pkg); err != nil {
		t.Fatalf("PatchSignature failed: %v", err)
	}

	// 6. Verify New State
	objAfter := info.ObjectOf(targetDecl.Name)
	if objAfter == objBefore {
		t.Error("Expected new object instance after patch")
	}

	sigAfter := objAfter.Type().(*types.Signature)
	if sigAfter.Results().Len() != 2 {
		t.Fatalf("Expected 2 returns after patch, got %d", sigAfter.Results().Len())
	}

	lastType := sigAfter.Results().At(1).Type()
	if lastType.String() != "error" {
		t.Errorf("Expected last return to be error, got %s", lastType.String())
	}

	// 7. Verify Type map for the Type Node
	tv := info.Types[targetDecl.Type]
	if tv.Type != sigAfter {
		t.Error("Info.Types[FuncDecl.Type] was not updated")
	}
}

func TestPatchSignature_NilInputs(t *testing.T) {
	if err := PatchSignature(nil, nil, nil); err == nil {
		t.Error("Expected error for nil inputs")
	}
}
