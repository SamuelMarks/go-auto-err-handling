package refactor

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

func typeCheckSingleFile(t *testing.T, src string) (*token.FileSet, *ast.File, *types.Info, *types.Package) {
	t.Helper()
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
	return fset, f, info, pkg
}

func findFuncDecl(t *testing.T, f *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name {
			return fd
		}
	}
	t.Fatalf("func %s not found", name)
	return nil
}

func TestPatchSignature_ObjectNotFound(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", "package main\nfunc Target() {}", 0)
	if err != nil {
		t.Fatal(err)
	}
	decl := findFuncDecl(t, f, "Target")
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}
	if err := PatchSignature(info, decl, nil); err == nil {
		t.Error("expected error when object is missing")
	}
}

func TestPatchSignature_NotFunction(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", "package main\nfunc Target() {}", 0)
	if err != nil {
		t.Fatal(err)
	}
	decl := findFuncDecl(t, f, "Target")
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  map[*ast.Ident]types.Object{decl.Name: types.NewVar(token.NoPos, nil, "Target", types.Typ[types.Int])},
		Uses:  make(map[*ast.Ident]types.Object),
	}
	if err := PatchSignature(info, decl, nil); err == nil {
		t.Error("expected error when object is not func")
	}
}

func TestPatchSignature_NotSignature(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", "package main\nfunc Target() {}", 0)
	if err != nil {
		t.Fatal(err)
	}
	decl := findFuncDecl(t, f, "Target")
	fn := types.NewFunc(token.NoPos, nil, "Target", nil)
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  map[*ast.Ident]types.Object{decl.Name: fn},
		Uses:  make(map[*ast.Ident]types.Object),
	}
	if err := PatchSignature(info, decl, nil); err == nil {
		t.Error("expected error when func has no signature")
	}
}

func TestPatchVarType_NilInputs(t *testing.T) {
	if _, err := PatchVarType(nil, nil, nil); err == nil {
		t.Error("expected error for nil inputs")
	}
}

func TestPatchVarType_ObjectNotFound(t *testing.T) {
	_, f, info, _ := typeCheckSingleFile(t, "package main\nvar f func()")
	var defIdent *ast.Ident
	ast.Inspect(f, func(n ast.Node) bool {
		if vs, ok := n.(*ast.ValueSpec); ok && len(vs.Names) > 0 {
			defIdent = vs.Names[0]
			return false
		}
		return true
	})
	if defIdent == nil {
		t.Fatal("def ident not found")
	}
	// Clear type info to simulate missing object.
	info.Defs = make(map[*ast.Ident]types.Object)
	if _, err := PatchVarType(info, defIdent, nil); err == nil {
		t.Error("expected error when object missing")
	}
}

func TestPatchVarType_NotVar(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", "package main\nvar f func()", 0)
	if err != nil {
		t.Fatal(err)
	}
	var defIdent *ast.Ident
	ast.Inspect(f, func(n ast.Node) bool {
		if vs, ok := n.(*ast.ValueSpec); ok && len(vs.Names) > 0 {
			defIdent = vs.Names[0]
			return false
		}
		return true
	})
	if defIdent == nil {
		t.Fatal("def ident not found")
	}
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  map[*ast.Ident]types.Object{defIdent: types.NewFunc(token.NoPos, nil, "f", types.NewSignature(nil, nil, nil, false))},
		Uses:  make(map[*ast.Ident]types.Object),
	}
	if _, err := PatchVarType(info, defIdent, nil); err == nil {
		t.Error("expected error when object is not var")
	}
}

func TestPatchVarType_FallbackByPos(t *testing.T) {
	_, f, info, pkg := typeCheckSingleFile(t, "package main\nvar f func()")
	var defIdent *ast.Ident
	ast.Inspect(f, func(n ast.Node) bool {
		if vs, ok := n.(*ast.ValueSpec); ok && len(vs.Names) > 0 {
			defIdent = vs.Names[0]
			return false
		}
		return true
	})
	if defIdent == nil {
		t.Fatal("def ident not found")
	}
	oldObj := info.ObjectOf(defIdent).(*types.Var)
	oldSig := oldObj.Type().(*types.Signature)
	newSig := ExtendSignatureWithError(oldSig, pkg)
	identCopy := &ast.Ident{Name: defIdent.Name, NamePos: defIdent.Pos()}
	newObj, err := PatchVarType(info, identCopy, newSig)
	if err != nil {
		t.Fatalf("PatchVarType failed: %v", err)
	}
	if info.Defs[defIdent] != newObj {
		t.Error("Defs not updated for original ident")
	}
}

func TestPatchVarType_FromUseUpdatesDef(t *testing.T) {
	_, f, info, pkg := typeCheckSingleFile(t, "package main\nvar f func()\nfunc main(){ f() }")
	var defIdent *ast.Ident
	var useIdent *ast.Ident
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.ValueSpec:
			if len(v.Names) > 0 && v.Names[0].Name == "f" {
				defIdent = v.Names[0]
			}
		case *ast.CallExpr:
			if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "f" {
				useIdent = id
			}
		}
		return true
	})
	if defIdent == nil || useIdent == nil {
		t.Fatal("idents not found")
	}
	oldObj := info.ObjectOf(defIdent).(*types.Var)
	oldSig := oldObj.Type().(*types.Signature)
	newSig := ExtendSignatureWithError(oldSig, pkg)
	newObj, err := PatchVarType(info, useIdent, newSig)
	if err != nil {
		t.Fatalf("PatchVarType failed: %v", err)
	}
	if info.Defs[defIdent] != newObj {
		t.Error("Defs not updated when patching from use")
	}
	if info.ObjectOf(useIdent) != newObj {
		t.Error("Uses not updated for use ident")
	}
}

func TestLookupObject_UsesFallbackAndNil(t *testing.T) {
	if lookupObject(nil, nil) != nil {
		t.Error("expected nil for nil inputs")
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", "package main\nfunc f() {}", 0)
	if err != nil {
		t.Fatal(err)
	}
	var useIdent *ast.Ident
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "f" {
			useIdent = id
			return false
		}
		return true
	})
	if useIdent == nil {
		t.Fatal("ident not found")
	}
	obj := types.NewFunc(useIdent.Pos(), nil, "f", types.NewSignature(nil, nil, nil, false))
	info := &types.Info{
		Uses: map[*ast.Ident]types.Object{useIdent: obj},
		Defs: make(map[*ast.Ident]types.Object),
	}
	identCopy := &ast.Ident{Name: useIdent.Name, NamePos: useIdent.Pos()}
	if got := lookupObject(info, identCopy); got != obj {
		t.Error("expected lookupObject to find object via uses fallback")
	}
}
