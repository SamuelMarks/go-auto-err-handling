package refactor

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

// TestPatchSignature verifies that patching correctly updates the type system view for function decls.
func TestPatchSignature(t *testing.T) {
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

	var targetDecl *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "Target" {
			targetDecl = fd
		}
	}
	if targetDecl == nil {
		t.Fatal("Target func not found")
	}

	objBefore := info.ObjectOf(targetDecl.Name)
	sigBefore := objBefore.Type().(*types.Signature)
	if sigBefore.Results().Len() != 1 {
		t.Fatalf("Expected 1 return initially, got %d", sigBefore.Results().Len())
	}

	if targetDecl.Type.Results == nil {
		targetDecl.Type.Results = &ast.FieldList{}
	}
	targetDecl.Type.Results.List = append(targetDecl.Type.Results.List, &ast.Field{
		Type: &ast.Ident{Name: "error"},
	})

	if err := PatchSignature(info, targetDecl, pkg); err != nil {
		t.Fatalf("PatchSignature failed: %v", err)
	}

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

// TestPatchVarType verifies patching a variable's type.
func TestPatchVarType(t *testing.T) {
	src := `package main
var f func()
func main() {
	f()
}
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

	// Find var definition
	var varIdent *ast.Ident
	ast.Inspect(f, func(n ast.Node) bool {
		if vs, ok := n.(*ast.ValueSpec); ok {
			if len(vs.Names) > 0 && vs.Names[0].Name == "f" {
				varIdent = vs.Names[0]
			}
		}
		return true
	})
	if varIdent == nil {
		t.Fatal("Variable f not found")
	}

	oldObj := info.ObjectOf(varIdent)
	oldSig := oldObj.Type().(*types.Signature)
	newSig := ExtendSignatureWithError(oldSig, pkg)

	if _, err := PatchVarType(info, varIdent, newSig); err != nil {
		t.Fatal(err)
	}

	newObj := info.ObjectOf(varIdent)
	if newObj == oldObj {
		t.Error("Variable object not updated")
	}
	sig := newObj.Type().(*types.Signature)
	if sig.Results().Len() != 1 {
		t.Error("Expected error return added to variable signature")
	}

	foundUse := false
	for id, obj := range info.Uses {
		if id.Name == "f" && obj == newObj {
			foundUse = true
		}
	}
	if !foundUse {
		t.Error("Uses not updated to point to new variable object")
	}
}

func TestPatchSignature_NilInputs(t *testing.T) {
	if err := PatchSignature(nil, nil, nil); err == nil {
		t.Error("Expected error for nil inputs")
	}
}
