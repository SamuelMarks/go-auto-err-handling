package refactor

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

// TestPatchSignature verifies that patching correctly updates the type system view.
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

	// 3. Modify AST (Simulate AddErrorToSignature logic manually to isolate test)
	// Add ", error" to AST results
	if targetDecl.Type.Results == nil {
		targetDecl.Type.Results = &ast.FieldList{}
	}
	targetDecl.Type.Results.List = append(targetDecl.Type.Results.List, &ast.Field{
		Type: &ast.Ident{Name: "error"},
	})

	// 4. Apply Patch
	if err := PatchSignature(info, targetDecl, pkg); err != nil {
		t.Fatalf("PatchSignature failed: %v", err)
	}

	// 5. Verify New State
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
}

// TestPatchSignature_NilInputs verifies safe nil checking.
func TestPatchSignature_NilInputs(t *testing.T) {
	if err := PatchSignature(nil, nil, nil); err == nil {
		t.Error("Expected error for nil inputs")
	}
}
