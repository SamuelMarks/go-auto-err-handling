package analysis

import (
	"bytes"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/filter"
	"golang.org/x/tools/go/packages"
)

func TestCheckCompliance_NonInterfaceEntry(t *testing.T) {
	pkg := types.NewPackage("p", "p")
	reg := &InterfaceRegistry{
		interfaces: []*types.TypeName{
			types.NewTypeName(token.NoPos, pkg, "NotIface", types.Typ[types.Int]),
		},
	}
	named := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "S", nil), types.NewStruct(nil, nil), nil)
	recv := types.NewVar(token.NoPos, pkg, "s", types.NewPointer(named))
	sig := types.NewSignatureType(recv, nil, nil, nil, nil, false)
	method := types.NewFunc(token.NoPos, pkg, "Run", sig)
	if _, err := reg.CheckCompliance(method); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestErrcheckParser_ParseSkipsBadNumbersAndMissingFile(t *testing.T) {
	parser := &ErrcheckParser{fileMap: map[string]fileContext{}}
	points, err := parser.Parse(strings.NewReader("file.go:xx:1: msg\nfile.go:1:yy: msg\nmissing.go:1:1: msg\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 0 {
		t.Fatal("expected no points")
	}
}

func TestCheckForChainsAndCompositeLit(t *testing.T) {
	call := &ast.CallExpr{Fun: &ast.Ident{Name: "foo"}}
	sel := &ast.SelectorExpr{X: call, Sel: &ast.Ident{Name: "bar"}}

	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{
		call: {Type: types.Universe.Lookup("error").Type()},
	}}

	called := false
	checkForChains(info, sel, func(c *ast.CallExpr) {
		called = true
	})
	if !called {
		t.Fatal("expected checkForChains to invoke callback")
	}

	called = false
	lit := &ast.CompositeLit{Elts: []ast.Expr{call}}
	checkForCompositeLit(info, lit, func(c *ast.CallExpr) {
		called = true
	})
	if !called {
		t.Fatal("expected checkForCompositeLit to invoke callback")
	}

	// KeyValueExpr branch
	called = false
	kv := &ast.KeyValueExpr{Key: &ast.Ident{Name: "F"}, Value: call}
	lit = &ast.CompositeLit{Elts: []ast.Expr{kv}}
	checkForCompositeLit(info, lit, func(c *ast.CallExpr) {
		called = true
	})
	if !called {
		t.Fatal("expected checkForCompositeLit to invoke callback for key/value")
	}

	// Nil root should be a no-op.
	checkForCompositeLit(info, nil, func(c *ast.CallExpr) {})

	// FuncLit should stop traversal.
	called = false
	fn := &ast.FuncLit{Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: call}}}}
	checkForCompositeLit(info, fn, func(c *ast.CallExpr) {
		called = true
	})
	if called {
		t.Fatal("expected func literal to stop traversal")
	}
}

func TestIsErrorReturningCallAndBlankIdentifier(t *testing.T) {
	call := &ast.CallExpr{Fun: &ast.Ident{Name: "foo"}}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{
		call: {Type: types.Typ[types.Int]},
	}}
	if ok, _ := isErrorReturningCall(nil, call); ok {
		t.Fatal("expected false for nil info")
	}
	if ok, _ := isErrorReturningCall(info, call); ok {
		t.Fatal("expected false for non-error type")
	}

	// Tuple with non-error last element
	tuple := types.NewTuple(types.NewVar(token.NoPos, nil, "a", types.Typ[types.Int]))
	info.Types[call] = types.TypeAndValue{Type: tuple}
	if ok, _ := isErrorReturningCall(info, call); ok {
		t.Fatal("expected false for tuple without error")
	}

	if isBlankIdentifier(&ast.BasicLit{}) {
		t.Fatal("expected non-ident to be false")
	}
}

func TestIsErrorReturningCall_MissingAndVoid(t *testing.T) {
	call := &ast.CallExpr{Fun: &ast.Ident{Name: "missing"}}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}
	if ok, _ := isErrorReturningCall(info, call); ok {
		t.Fatal("expected false for missing type info")
	}

	src := "package p\nfunc noop() {}\nfunc run() { noop() }\n"
	_, file, info2, _ := loadTypes(t, src)
	call2 := findCall(t, file, "noop")
	if ok, _ := isErrorReturningCall(info2, call2); ok {
		t.Fatal("expected false for void return")
	}
}

func TestIsGlobalErrorIgnored_MultiAssign(t *testing.T) {
	call := &ast.CallExpr{Fun: &ast.Ident{Name: "foo"}}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{
		call: {Type: types.Universe.Lookup("error").Type()},
	}}
	vSpec := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent("_")}, Values: []ast.Expr{call}}
	if !isGlobalErrorIgnored(info, vSpec, 0, call) {
		t.Fatal("expected global error ignore for 1:1 blank assignment")
	}
}

func TestIsGlobalErrorIgnored_FalseCases(t *testing.T) {
	call := &ast.CallExpr{Fun: &ast.Ident{Name: "foo"}}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{
		call: {Type: types.Typ[types.Int]},
	}}
	vSpec := &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent("_")}, Values: []ast.Expr{call}}
	if isGlobalErrorIgnored(info, vSpec, 0, call) {
		t.Fatal("expected false for non-error return")
	}

	info.Types[call] = types.TypeAndValue{Type: types.Universe.Lookup("error").Type()}
	vSpec = &ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent("_")}, Values: []ast.Expr{call, call}}
	if isGlobalErrorIgnored(info, vSpec, 0, call) {
		t.Fatal("expected false for mismatched value counts")
	}
}

func TestFindSafeEmbeddedCall_Paren(t *testing.T) {
	call := &ast.CallExpr{Fun: &ast.Ident{Name: "foo"}}
	paren := &ast.ParenExpr{X: call}
	if got := findSafeEmbeddedCall(paren); got != call {
		t.Fatal("expected to unwrap paren expr")
	}
}

func TestGetCalledFunction_SelectorAndNil(t *testing.T) {
	pkg := types.NewPackage("example.com/p", "p")
	fnObj := types.NewFunc(token.NoPos, pkg, "Do", types.NewSignatureType(nil, nil, nil, nil, nil, false))

	call := &ast.CallExpr{Fun: &ast.SelectorExpr{X: &ast.Ident{Name: "p"}, Sel: &ast.Ident{Name: "Do"}}}
	info := &types.Info{Uses: map[*ast.Ident]types.Object{
		call.Fun.(*ast.SelectorExpr).Sel: fnObj,
	}}
	if fn := getCalledFunction(info, call); fn == nil || fn.Name() != "Do" {
		t.Fatal("expected selector function to be resolved")
	}

	call2 := &ast.CallExpr{Fun: &ast.Ident{Name: "Missing"}}
	info2 := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	if fn := getCalledFunction(info2, call2); fn != nil {
		t.Fatal("expected nil function when object missing")
	}

	// Variable of non-func type should return nil.
	call3 := &ast.CallExpr{Fun: &ast.Ident{Name: "x"}}
	info3 := &types.Info{Uses: map[*ast.Ident]types.Object{
		call3.Fun.(*ast.Ident): types.NewVar(token.NoPos, pkg, "x", types.Typ[types.Int]),
	}}
	if fn := getCalledFunction(info3, call3); fn != nil {
		t.Fatal("expected nil function for non-signature var")
	}
}

func TestShouldInclude_FilterBySymbolAndDirective(t *testing.T) {
	src := "package p\nimport \"errors\"\nfunc fail() error { return errors.New(\"x\") }\nfunc run() {\n\tfail() // auto-err:ignore\n}\n"
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "p.go")
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: importer.Default()}
	_, err = conf.Check("example.com/p", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatalf("typecheck error: %v", err)
	}

	call := findCall(t, file, "fail")
	fnObj := info.ObjectOf(call.Fun.(*ast.Ident)).(*types.Func)
	flt := filter.New(nil, []string{fnObj.Pkg().Path() + "." + fnObj.Name()})

	cmap := ast.NewCommentMap(fset, file, file.Comments)
	stmt := findStmtContainingCall(file, call)

	if shouldInclude(&packages.Package{Fset: fset, TypesInfo: info}, file, call, stmt, cmap, flt, true) {
		t.Fatal("expected filter to exclude symbol")
	}

	// Directive should exclude when attached to statement.
	cmap = ast.CommentMap{
		stmt: []*ast.CommentGroup{{List: []*ast.Comment{{Text: "// auto-err:ignore"}}}},
	}
	if shouldInclude(&packages.Package{Fset: fset, TypesInfo: info}, file, call, stmt, cmap, nil, false) {
		t.Fatal("expected directive to exclude call")
	}

	var buf bytes.Buffer
	origOutput := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(origOutput)

	// Directive debug logging.
	buf.Reset()
	if shouldInclude(&packages.Package{Fset: fset, TypesInfo: info}, file, call, stmt, cmap, nil, true) {
		t.Fatal("expected directive to exclude call with debug")
	}
	if buf.Len() == 0 {
		t.Fatal("expected directive debug log output")
	}

	// File filter should exclude when glob matches.
	fltFile := filter.New([]string{"p.go"}, nil)
	if shouldInclude(&packages.Package{Fset: fset, TypesInfo: info}, file, call, stmt, nil, fltFile, false) {
		t.Fatal("expected file filter to exclude call")
	}

	buf.Reset()
	if shouldInclude(&packages.Package{Fset: fset, TypesInfo: info}, file, call, stmt, nil, fltFile, true) {
		t.Fatal("expected file filter to exclude call with debug")
	}
	if buf.Len() == 0 {
		t.Fatal("expected file filter debug log output")
	}
}

func findStmtContainingCall(file *ast.File, target *ast.CallExpr) ast.Stmt {
	var stmt ast.Stmt
	ast.Inspect(file, func(n ast.Node) bool {
		if stmt != nil {
			return false
		}
		if s, ok := n.(ast.Stmt); ok {
			ast.Inspect(s, func(inner ast.Node) bool {
				if inner == target {
					stmt = s
					return false
				}
				return true
			})
		}
		return true
	})
	return stmt
}
