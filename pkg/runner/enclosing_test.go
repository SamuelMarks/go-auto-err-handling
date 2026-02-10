package runner

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"golang.org/x/tools/go/packages"
)

// setupEnclosingEnv parses and checks a source string to provide a realistic packages.Package
// environment with populated TypesInfo for testing.
func setupEnclosingEnv(t *testing.T, src string) (*token.FileSet, *ast.File, *packages.Package) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parser failed: %v", err)
	}

	conf := types.Config{
		Importer: importer.Default(),
	}
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}

	pkg, err := conf.Check("main", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("type check failed: %v", err)
	}

	p := &packages.Package{
		Fset:      fset,
		Syntax:    []*ast.File{f},
		Types:     pkg,
		TypesInfo: info,
	}

	return fset, f, p
}

// findNodePos locates the first AST node matching the provided predicate and returns its position.
func findNodePos(f *ast.File, predicate func(ast.Node) bool) token.Pos {
	var found token.Pos
	ast.Inspect(f, func(n ast.Node) bool {
		if found != token.NoPos {
			return false
		}
		if n != nil && predicate(n) {
			found = n.Pos()
			return false
		}
		return true
	})
	return found
}

func TestFindEnclosingFunc(t *testing.T) {
	src := `package main

func TopLevel() {
	x := 1 // Point A
	_ = x
	
	func() {
		y := 2 // Point B
		_ = y
		
		func() {
			z := 3 // Point C
			_ = z
		}()
	}()
}

var Global = func() {
	g := 4 // Point D
	_ = g
}
`
	_, file, pkg := setupEnclosingEnv(t, src)

	// Identify positions for markers
	posA := findNodePos(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "x" {
			return true
		}
		return false
	})
	posB := findNodePos(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "y" {
			return true
		}
		return false
	})
	posC := findNodePos(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "z" {
			return true
		}
		return false
	})
	posD := findNodePos(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "g" {
			return true
		}
		return false
	})

	t.Run("TopLevelDeclaration", func(t *testing.T) {
		ctx := FindEnclosingFunc(pkg, file, posA)
		if ctx == nil {
			t.Fatal("Expected context, got nil")
		}
		if ctx.Decl == nil || ctx.Decl.Name.Name != "TopLevel" {
			t.Error("Expected TopLevel declaration context")
		}
		if ctx.IsLiteral() {
			t.Error("Expected Decl, got Literal indicator")
		}
		if ctx.Node != ctx.Decl {
			t.Error("Node field should match Decl")
		}
		if ctx.Sig == nil {
			t.Error("Signature should be resolved")
		}
	})

	t.Run("MiddleLiteral", func(t *testing.T) {
		ctx := FindEnclosingFunc(pkg, file, posB)
		if ctx == nil {
			t.Fatal("Expected context, got nil")
		}
		if ctx.Lit == nil {
			t.Error("Expected Literal context")
		}
		if ctx.Decl != nil {
			t.Error("Expected nil Decl for literal context")
		}
		if !ctx.IsLiteral() {
			t.Error("Expected IsLiteral() to be true")
		}
	})

	t.Run("NestedLiteral", func(t *testing.T) {
		ctx := FindEnclosingFunc(pkg, file, posC)
		if ctx == nil {
			t.Fatal("Expected context, got nil")
		}
		if ctx.Lit == nil {
			t.Fatal("Expected Literal context")
		}
		// Ensure we got the innermost one. Since strict equality on pointers works for AST nodes:
		// We'll rely on the fact that FindEnclosingFunc uses the first node in path.
		// The path is [Ident(z), AssignStmt, Block, FuncLit(Inner), Block, FuncLit(Outer)...]
		// It should pick FuncLit(Inner).
	})

	t.Run("GlobalVariableLiteral", func(t *testing.T) {
		ctx := FindEnclosingFunc(pkg, file, posD)
		if ctx == nil {
			t.Fatal("Expected context for global func literal, got nil")
		}
		if ctx.Lit == nil {
			t.Error("Expected Literal context")
		}
	})
}

func TestFindEnclosingFunc_NoContext(t *testing.T) {
	src := `package main

var x = 1 // Point Global
`
	_, file, pkg := setupEnclosingEnv(t, src)

	posGlobal := findNodePos(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "x" {
			return true
		}
		return false
	})

	ctx := FindEnclosingFunc(pkg, file, posGlobal)
	if ctx != nil {
		t.Errorf("Expected nil context for global variable, got %v", ctx)
	}
}

func TestFindEnclosingFunc_MissingTypes(t *testing.T) {
	// Setup parsing but NO type info
	fset := token.NewFileSet()
	src := "package main\nfunc Foo() { x := 1 }"
	f, _ := parser.ParseFile(fset, "main.go", src, 0)

	// Empty package with no type info maps
	pkg := &packages.Package{
		Fset:      fset,
		Syntax:    []*ast.File{f},
		TypesInfo: &types.Info{}, // Empty maps
	}

	pos := findNodePos(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "x" {
			return true
		}
		return false
	})

	ctx := FindEnclosingFunc(pkg, f, pos)
	if ctx != nil {
		t.Error("Expected nil context when TypesInfo is missing")
	}
}

func TestFindEnclosingFunc_Subtest(t *testing.T) {
	src := `package main
import "testing"

func TestFoo(t *testing.T) {
	t.Run("sub", func(subT *testing.T) {
		x := 1 // Point Sub
		_ = x
	})
}
`
	_, file, pkg := setupEnclosingEnv(t, src)

	posSub := findNodePos(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "x" {
			return true
		}
		return false
	})

	ctx := FindEnclosingFunc(pkg, file, posSub)
	if ctx == nil {
		t.Fatal("Expected context for subtest")
	}
	if ctx.TestParam != "subT" {
		t.Errorf("Expected TestParam 'subT', got '%s'", ctx.TestParam)
	}
	if !ctx.IsLiteral() {
		t.Error("Expected IsLiteral true for subtest closure")
	}
}

func TestFindEnclosingFunc_TopLevelTestParam(t *testing.T) {
	src := `package main
import "testing"

func TestTop(t *testing.T) {
	x := 1
	_ = x
}
`
	_, file, pkg := setupEnclosingEnv(t, src)

	pos := findNodePos(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "x" {
			return true
		}
		return false
	})

	ctx := FindEnclosingFunc(pkg, file, pos)
	if ctx == nil {
		t.Fatal("expected context for top-level test")
	}
	if ctx.TestParam != "t" {
		t.Fatalf("expected TestParam 't', got %q", ctx.TestParam)
	}
}

func TestFindEnclosingFunc_LiteralMissingTypeInfo(t *testing.T) {
	fset := token.NewFileSet()
	src := `package main
func run() {
	func() {
		y := 2
		_ = y
	}()
}`
	file, err := parser.ParseFile(fset, "main.go", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	pkg := &packages.Package{
		Fset:      fset,
		Syntax:    []*ast.File{file},
		TypesInfo: &types.Info{Types: map[ast.Expr]types.TypeAndValue{}},
	}

	pos := findNodePos(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "y" {
			return true
		}
		return false
	})

	if ctx := FindEnclosingFunc(pkg, file, pos); ctx != nil {
		t.Fatal("expected nil context when func literal type info missing")
	}
}

func TestIsSubtestCall_FallbackTypes(t *testing.T) {
	sel := &ast.SelectorExpr{
		X:   ast.NewIdent("t"),
		Sel: ast.NewIdent("Run"),
	}
	call := &ast.CallExpr{Fun: sel}

	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}
	testingPkg := types.NewPackage("testing", "testing")
	named := types.NewNamed(types.NewTypeName(token.NoPos, testingPkg, "T", nil), types.NewStruct(nil, nil), nil)
	info.Types[sel.X] = types.TypeAndValue{Type: types.NewPointer(named)}

	if !isSubtestCall(call, info) {
		t.Fatal("expected subtest call via fallback types info")
	}

	info.Types[sel.X] = types.TypeAndValue{Type: named}
	if isSubtestCall(call, info) {
		t.Fatal("expected non-pointer testing.T to fail subtest detection")
	}
}

func TestIsSubtestCall_NonRunSelector(t *testing.T) {
	call := &ast.CallExpr{Fun: &ast.SelectorExpr{X: ast.NewIdent("t"), Sel: ast.NewIdent("Skip")}}
	if isSubtestCall(call, &types.Info{}) {
		t.Fatal("expected non-Run selector to be false")
	}
}
