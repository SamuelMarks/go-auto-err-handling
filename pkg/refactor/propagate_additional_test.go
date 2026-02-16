package refactor

import (
	"errors"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/astgen"
	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
	"golang.org/x/tools/go/packages"
)

type errorProvider struct {
	err error
}

func (e *errorProvider) Get(pkg *packages.Package, file *ast.File) (*dst.File, error) {
	return nil, e.err
}

func (e *errorProvider) MarkModified(file *ast.File) {}

type mapProvider struct {
	files    map[*ast.File]*dst.File
	modified []*ast.File
}

func (m *mapProvider) Get(pkg *packages.Package, file *ast.File) (*dst.File, error) {
	if f := m.files[file]; f != nil {
		return f, nil
	}
	return nil, errors.New("missing dst file")
}

func (m *mapProvider) MarkModified(file *ast.File) {
	m.modified = append(m.modified, file)
}

func setupMultiFileEnv(t *testing.T, files map[string]string) (*packages.Package, []*ast.File, map[*ast.File]*dst.File) {
	t.Helper()
	tmpDir := t.TempDir()
	fset := token.NewFileSet()
	var astFiles []*ast.File
	dstFiles := make(map[*ast.File]*dst.File)
	var goFiles []string

	for name, src := range files {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte(src), 0644); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}
		dec := decorator.NewDecorator(fset)
		dstFile, err := dec.DecorateFile(f)
		if err != nil {
			t.Fatalf("decorate failed: %v", err)
		}
		astFiles = append(astFiles, f)
		dstFiles[f] = dstFile
		goFiles = append(goFiles, path)
	}

	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}
	conf := types.Config{Importer: importer.Default()}
	pkgTypes, err := conf.Check("main", fset, astFiles, info)
	if err != nil {
		t.Fatalf("type check failed: %v", err)
	}
	pkg := &packages.Package{
		Fset:      fset,
		Syntax:    astFiles,
		Types:     pkgTypes,
		TypesInfo: info,
		GoFiles:   goFiles,
	}
	return pkg, astFiles, dstFiles
}

func findCallStmt(t *testing.T, f *ast.File, name string) (*ast.CallExpr, ast.Stmt) {
	t.Helper()
	var call *ast.CallExpr
	var stmt ast.Stmt
	ast.Inspect(f, func(n ast.Node) bool {
		if stmt != nil {
			return false
		}
		if es, ok := n.(*ast.ExprStmt); ok {
			if c, ok := es.X.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == name {
					call = c
					stmt = es
					return false
				}
			}
		}
		return true
	})
	if call == nil || stmt == nil {
		t.Fatalf("call stmt for %s not found", name)
	}
	return call, stmt
}

func TestPropagateCallers_NilTarget(t *testing.T) {
	if _, err := PropagateCallers(nil, nil, nil, "log-fatal"); err == nil {
		t.Error("expected error for nil target")
	}
}

func TestPropagateCallers_GetError(t *testing.T) {
	src := `package main
func Target() {} 
func main() { Target() } 
`
	pkg, _, _ := setupPropagateEnv(t, src)
	targetFunc := findDecl(t, pkg, "Target")
	provider := &errorProvider{err: errors.New("boom")}
	if _, err := PropagateCallers([]*packages.Package{pkg}, provider, targetFunc, "log-fatal"); err == nil {
		t.Error("expected error from provider")
	}
}

func TestPropagateCallers_FileNotFound(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", "package main\nfunc Target() {}", 0)
	if err != nil {
		t.Fatal(err)
	}
	id := &ast.Ident{Name: "Target"}
	sig := types.NewSignature(nil, nil, nil, false)
	target := types.NewFunc(token.NoPos, nil, "Target", sig)
	info := &types.Info{Uses: map[*ast.Ident]types.Object{id: target}}
	pkg := &packages.Package{Fset: fset, Syntax: []*ast.File{f}, TypesInfo: info}
	updates, err := PropagateCallers([]*packages.Package{pkg}, &mockProvider{}, target, "log-fatal")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updates != 0 {
		t.Errorf("expected 0 updates, got %d", updates)
	}
}

func TestProcessCallSite_NilArgs(t *testing.T) {
	if _, _, err := processCallSiteDST(nil, nil, nil, nil, nil, "log-fatal"); err == nil {
		t.Error("expected error for nil args")
	}
}

func TestProcessCallSite_NoCallOrStmt(t *testing.T) {
	src := `package main
func Target() {} 
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	var decl *ast.FuncDecl
	for _, d := range astFile.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "Target" {
			decl = fd
		}
	}
	if decl == nil {
		t.Fatal("decl not found")
	}
	targetObj := pkg.TypesInfo.ObjectOf(decl.Name)
	n, next, err := processCallSiteDST(pkg, astFile, dstFile, decl.Name, targetObj, "log-fatal")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 || next != nil {
		t.Errorf("expected no updates for non-call ident, got %d", n)
	}
}

func TestHandleEntryPoint_NoFile(t *testing.T) {
	pkg := &packages.Package{}
	stmt := &ast.ExprStmt{X: &ast.Ident{Name: "x"}}
	if err := HandleEntryPoint(pkg, &dst.File{}, nil, stmt, "log-fatal"); err == nil {
		t.Error("expected error for missing AST file")
	}
}

func TestHandleEntryPoint_Success(t *testing.T) {
	src := `package main
func Target() {} 
func main() { Target() } 
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	call, stmt := findCallStmt(t, astFile, "Target")
	if err := HandleEntryPoint(pkg, dstFile, call, stmt, "panic"); err != nil {
		t.Fatalf("HandleEntryPoint failed: %v", err)
	}
	out := renderDST(t, dstFile)
	if !strings.Contains(out, "panic(err)") {
		t.Errorf("expected panic handler, got:\n%s", out)
	}
}

func TestHandleTestError_Success(t *testing.T) {
	src := `package main
import "testing" 
func Target() {} 
func TestFoo(t *testing.T) { Target() } 
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	call, stmt := findCallStmt(t, astFile, "Target")
	if err := HandleTestError(pkg, dstFile, call, stmt, "t"); err != nil {
		t.Fatalf("HandleTestError failed: %v", err)
	}
	out := renderDST(t, dstFile)
	if !strings.Contains(out, "t.Fatal(err)") {
		t.Errorf("expected test fatal handler, got:\n%s", out)
	}
}

func TestContainsNode(t *testing.T) {
	idX := &ast.Ident{Name: "x"}
	idY := &ast.Ident{Name: "y"}
	root := &ast.BinaryExpr{X: idX, Y: idY}
	if !containsNode(root, idX) {
		t.Error("expected to find node")
	}
	if containsNode(root, &ast.Ident{Name: "z"}) {
		t.Error("expected missing node to be false")
	}
}

func TestHasTrailingErrorReturnDST(t *testing.T) {
	if hasTrailingErrorReturnDST(&dst.FuncType{}) {
		t.Error("expected false for nil results")
	}
	ftEmpty := &dst.FuncType{Results: &dst.FieldList{List: []*dst.Field{}}}
	if hasTrailingErrorReturnDST(ftEmpty) {
		t.Error("expected false for empty results")
	}
	ft := &dst.FuncType{
		Results: &dst.FieldList{List: []*dst.Field{{Type: dst.NewIdent("error")}}},
	}
	if !hasTrailingErrorReturnDST(ft) {
		t.Error("expected true for trailing error")
	}
	ft2 := &dst.FuncType{
		Results: &dst.FieldList{List: []*dst.Field{{Type: dst.NewIdent("int")}}},
	}
	if hasTrailingErrorReturnDST(ft2) {
		t.Error("expected false for non-error")
	}
	ft3 := &dst.FuncType{
		Results: &dst.FieldList{List: []*dst.Field{{Type: &dst.SelectorExpr{X: dst.NewIdent("pkg"), Sel: dst.NewIdent("Err")}}}},
	}
	if hasTrailingErrorReturnDST(ft3) {
		t.Error("expected false for non-ident error")
	}
}

func TestGenerateDstTerminalBody_OsExit(t *testing.T) {
	body := generateDstTerminalBody(HandlerOsExit, "")
	if len(body.List) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(body.List))
	}
	firstCall := body.List[0].(*dst.ExprStmt).X.(*dst.CallExpr)
	firstSel := firstCall.Fun.(*dst.SelectorExpr)
	if firstSel.X.(*dst.Ident).Name != "fmt" || firstSel.Sel.Name != "Println" {
		t.Error("expected fmt.Println in os-exit handler")
	}
	secondCall := body.List[1].(*dst.ExprStmt).X.(*dst.CallExpr)
	secondSel := secondCall.Fun.(*dst.SelectorExpr)
	if secondSel.X.(*dst.Ident).Name != "os" || secondSel.Sel.Name != "Exit" {
		t.Error("expected os.Exit in os-exit handler")
	}
}

func TestGenerateDstReturnBody_NilSig(t *testing.T) {
	body := generateDstReturnBody(nil, astgen.ZeroCtx{})
	if len(body.List) != 1 {
		t.Fatal("expected single return")
	}
	ret := body.List[0].(*dst.ReturnStmt)
	if len(ret.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(ret.Results))
	}
	if id, ok := ret.Results[0].(*dst.Ident); !ok || id.Name != "err" {
		t.Error("expected return err for nil signature")
	}
}

func makeNamedSignature() (*types.Signature, *types.TypeName) {
	obj := types.NewTypeName(token.NoPos, nil, "Widget", nil)
	named := types.NewNamed(obj, types.NewStruct(nil, nil), nil)
	errType := types.Universe.Lookup("error").Type()
	results := types.NewTuple(
		types.NewVar(token.NoPos, nil, "", named),
		types.NewVar(token.NoPos, nil, "", errType),
	)
	return types.NewSignature(nil, nil, results, false), obj
}

func TestGenerateBasicCheck_Terminal(t *testing.T) {
	ctx := rewriteContext{
		isTerminal: true,
		strategy:   HandlerPanic,
	}
	ifStmt := generateBasicCheck(ctx)
	out := renderDST(t, &dst.File{Name: dst.NewIdent("p"), Decls: []dst.Decl{
		&dst.FuncDecl{Name: dst.NewIdent("f"), Type: &dst.FuncType{}, Body: &dst.BlockStmt{List: []dst.Stmt{ifStmt}}},
	}})
	if !strings.Contains(out, "panic(err)") {
		t.Errorf("expected panic terminal handler, got:\n%s", out)
	}
}

func TestGenerateBasicCheck_ScopeNil(t *testing.T) {
	sig, _ := makeNamedSignature()
	ctx := rewriteContext{
		isTerminal:   false,
		enclosingSig: sig,
		scope:        nil,
		pos:          token.Pos(1),
	}
	ifStmt := generateBasicCheck(ctx)
	ret := ifStmt.Body.List[0].(*dst.ReturnStmt)
	if _, ok := ret.Results[0].(*dst.CompositeLit); !ok {
		t.Error("expected composite literal when scope is nil")
	}
}

func TestGenerateBasicCheck_ScopeLookupNil(t *testing.T) {
	sig, _ := makeNamedSignature()
	scope := types.NewScope(nil, 1, 100, "test")
	ctx := rewriteContext{
		isTerminal:   false,
		enclosingSig: sig,
		scope:        scope,
		pos:          token.Pos(50),
	}
	ifStmt := generateBasicCheck(ctx)
	ret := ifStmt.Body.List[0].(*dst.ReturnStmt)
	if _, ok := ret.Results[0].(*dst.CompositeLit); !ok {
		t.Error("expected composite literal when scope lookup is nil")
	}
}

func TestGenerateBasicCheck_ScopeShadowed(t *testing.T) {
	sig, obj := makeNamedSignature()
	scope := types.NewScope(nil, 1, 100, "test")
	shadow := types.NewTypeName(token.NoPos, nil, obj.Name(), nil)
	scope.Insert(shadow)
	ctx := rewriteContext{
		isTerminal:   false,
		enclosingSig: sig,
		scope:        scope,
		pos:          token.Pos(50),
	}
	ifStmt := generateBasicCheck(ctx)
	ret := ifStmt.Body.List[0].(*dst.ReturnStmt)
	if id, ok := ret.Results[0].(*dst.Ident); !ok || id.Name != "nil" {
		t.Error("expected nil result when type name is shadowed")
	}
}

func TestMapAstToDst_NotInFile(t *testing.T) {
	fset := token.NewFileSet()
	f1, err := parser.ParseFile(fset, "a.go", "package main\nfunc A() {}", 0)
	if err != nil {
		t.Fatal(err)
	}
	f2, err := parser.ParseFile(fset, "b.go", "package main\nfunc B() {}", 0)
	if err != nil {
		t.Fatal(err)
	}
	dst1, err := decorator.NewDecorator(fset).DecorateFile(f1)
	if err != nil {
		t.Fatal(err)
	}
	if node, _ := mapAstToDst(f1, dst1, f2.Name); node != nil {
		t.Error("expected nil mapping for node not in file")
	}
}

func TestMapAstToDst_StartIndexMissing(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "a.go", "package main\nfunc A() {}", 0)
	if err != nil {
		t.Fatal(err)
	}
	dstFile, err := decorator.NewDecorator(fset).DecorateFile(f)
	if err != nil {
		t.Fatal(err)
	}
	fake := &ast.Ident{Name: "X", NamePos: f.Pos() + 1}
	if node, _ := mapAstToDst(f, dstFile, fake); node != nil {
		t.Error("expected nil mapping for missing target in path")
	}
}

func TestMapAstToDst_ApplyTraversalError(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "a.go", "package main\nfunc A() { A() }", 0)
	if err != nil {
		t.Fatal(err)
	}
	dstFile := &dst.File{Name: dst.NewIdent("main")}
	var call *ast.CallExpr
	ast.Inspect(f, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			call = c
			return false
		}
		return true
	})
	if call == nil {
		t.Fatal("call not found")
	}
	if node, _ := mapAstToDst(f, dstFile, call); node != nil {
		t.Error("expected nil mapping when dst traversal fails")
	}
}

func TestMapAstToDst_DetermineStepError(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "a.go", "package main\nfunc A() { A() }", 0)
	if err != nil {
		t.Fatal(err)
	}
	dstFile, err := decorator.NewDecorator(fset).DecorateFile(f)
	if err != nil {
		t.Fatal(err)
	}
	var call *ast.CallExpr
	ast.Inspect(f, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			call = c
			return false
		}
		return true
	})
	if call == nil {
		t.Fatal("call not found")
	}
	orig := determineTraversalStepFn
	determineTraversalStepFn = func(ast.Node, ast.Node) (tStep, error) {
		return tStep{}, errors.New("boom")
	}
	t.Cleanup(func() { determineTraversalStepFn = orig })
	if node, _ := mapAstToDst(f, dstFile, call); node != nil {
		t.Error("expected nil mapping on determineTraversalStep error")
	}
}

func TestMapAstToDst_PathEmpty(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "a.go", "package main\nfunc A() {}", 0)
	if err != nil {
		t.Fatal(err)
	}
	dstFile, err := decorator.NewDecorator(fset).DecorateFile(f)
	if err != nil {
		t.Fatal(err)
	}
	fake := &ast.Ident{Name: "X", NamePos: f.End() + 10}
	if node, _ := mapAstToDst(f, dstFile, fake); node != nil {
		t.Error("expected nil mapping for out-of-file position")
	}
}

func TestProcessCallSite_MapError(t *testing.T) {
	src := `package main
func Target() {} 
func main() { Target() } 
`
	pkg, astFile, _ := setupPropagateEnv(t, src)
	targetFunc := findDecl(t, pkg, "Target")
	callID := findCall(t, astFile, "Target")
	_, _, err := processCallSiteDST(pkg, astFile, &dst.File{Name: dst.NewIdent("main")}, callID, targetFunc, "log-fatal")
	if err == nil {
		t.Error("expected mapping error")
	}
}

func TestProcessCallSite_TestHelper(t *testing.T) {
	src := `package main
import "testing" 
func Target() {} 
func helper(t *testing.T) { 
  t.Helper() 
  Target() 
} 
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	target := findDecl(t, pkg, "Target")
	call := findCall(t, astFile, "Target")
	n, _, err := processCallSiteDST(pkg, astFile, dstFile, call, target, "log-fatal")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Errorf("expected update count 1, got %d", n)
	}
	out := renderDST(t, dstFile)
	if !strings.Contains(out, "t.Fatal(err)") {
		t.Errorf("expected helper to use t.Fatal, got:\n%s", out)
	}
}

func TestProcessCallSite_AddErrorToSignatureError(t *testing.T) {
	src := `package main
func Target() {} 
func Caller() { Target() } 
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	target := findDecl(t, pkg, "Target")
	call := findCall(t, astFile, "Target")
	origAdd := addErrorToSignatureDSTFn
	addErrorToSignatureDSTFn = func(*dst.FuncDecl) (bool, error) {
		return false, errors.New("boom")
	}
	t.Cleanup(func() { addErrorToSignatureDSTFn = origAdd })
	if _, _, err := processCallSiteDST(pkg, astFile, dstFile, call, target, "log-fatal"); err == nil {
		t.Error("expected error from addErrorToSignatureDSTFn")
	}
}

func TestProcessCallSite_PatchSignatureError(t *testing.T) {
	src := `package main
func Target() {} 
func Caller() { Target() } 
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	target := findDecl(t, pkg, "Target")
	call := findCall(t, astFile, "Target")
	origAdd := addErrorToSignatureDSTFn
	origPatch := patchSignatureFn
	addErrorToSignatureDSTFn = func(*dst.FuncDecl) (bool, error) {
		return true, nil
	}
	patchSignatureFn = func(*types.Info, *ast.FuncDecl, *types.Package) error {
		return errors.New("boom")
	}
	t.Cleanup(func() {
		addErrorToSignatureDSTFn = origAdd
		patchSignatureFn = origPatch
	})
	if _, _, err := processCallSiteDST(pkg, astFile, dstFile, call, target, "log-fatal"); err == nil {
		t.Error("expected error from patchSignatureFn")
	}
}

func TestProcessCallSite_UnsupportedStmtError(t *testing.T) {
	src := `package main
func Target() bool { return true } 
func Caller() { 
  for Target() { 
  } 
} 
`
	// Note: IfStmt is now supported, so changed test case to ForStmt which is still unsupported in this context
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	target := findDecl(t, pkg, "Target")
	call := findCall(t, astFile, "Target")
	if _, _, err := processCallSiteDST(pkg, astFile, dstFile, call, target, "log-fatal"); err == nil {
		t.Error("expected unsupported stmt error")
	}
}

func TestDetermineTraversalStep(t *testing.T) {
	stmt := &ast.EmptyStmt{}
	block := &ast.BlockStmt{List: []ast.Stmt{stmt}}
	step, err := determineTraversalStep(block, stmt)
	if err != nil || step.FieldName != "List" || step.Index != 0 {
		t.Fatalf("unexpected step: %+v err=%v", step, err)
	}
	ifStmt := &ast.IfStmt{Cond: &ast.Ident{Name: "x"}, Body: &ast.BlockStmt{}}
	step, err = determineTraversalStep(ifStmt, ifStmt.Cond)
	if err != nil || step.FieldName != "Cond" || step.Index != -1 {
		t.Fatalf("unexpected step for pointer field: %+v err=%v", step, err)
	}
	exprStmt := &ast.ExprStmt{X: &ast.Ident{Name: "x"}}
	step, err = determineTraversalStep(exprStmt, exprStmt.X)
	if err != nil || step.FieldName != "X" || step.Index != -1 {
		t.Fatalf("unexpected step for interface field: %+v err=%v", step, err)
	}
	if _, err := determineTraversalStep(block, &ast.ReturnStmt{}); err == nil {
		t.Error("expected error when child not found")
	}
}

func TestDetermineTraversalStep_UnexportedField(t *testing.T) {
	child := &ast.Ident{Name: "x"}
	parent := &nodeWithUnexported{hidden: &ast.Ident{Name: "y"}, Child: child}
	step, err := determineTraversalStep(parent, child)
	if err != nil || step.FieldName != "Child" || step.Index != -1 {
		t.Fatalf("unexpected step for unexported field: %+v err=%v", step, err)
	}
}

type fakeNode struct {
	dst.NodeDecs
	Items []int
	Child int
}

func (f *fakeNode) Decorations() *dst.NodeDecs { return &f.NodeDecs }

type nodeWithUnexported struct {
	hidden ast.Node
	Child  ast.Node
}

func (n *nodeWithUnexported) Pos() token.Pos { return 1 }
func (n *nodeWithUnexported) End() token.Pos { return 1 }

func TestApplyTraversalStep(t *testing.T) {
	if _, err := applyTraversalStep(&dst.BlockStmt{}, tStep{FieldName: "Nope", Index: -1}); err == nil {
		t.Error("expected error for invalid field")
	}
	if _, err := applyTraversalStep(&fakeNode{Child: 1}, tStep{FieldName: "Child", Index: 0}); err == nil {
		t.Error("expected error for non-slice indexed field")
	}
	if _, err := applyTraversalStep(&fakeNode{Items: []int{1}}, tStep{FieldName: "Items", Index: 0}); err == nil {
		t.Error("expected error for non-node slice element")
	}
	if _, err := applyTraversalStep(&dst.BlockStmt{List: []dst.Stmt{&dst.EmptyStmt{}}}, tStep{FieldName: "List", Index: 0}); err != nil {
		t.Fatalf("unexpected error for slice element: %v", err)
	}
	if _, err := applyTraversalStep(&dst.ExprStmt{X: dst.NewIdent("x")}, tStep{FieldName: "X", Index: -1}); err != nil {
		t.Fatalf("unexpected error for non-slice node: %v", err)
	}
}

func TestFindFile(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "a.go", "package main\nfunc A() {}", 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg := &packages.Package{Syntax: []*ast.File{f}}
	if got := findFile(pkg, f.Pos()); got != f {
		t.Error("expected to find file by pos")
	}
	if got := findFile(pkg, f.End()+1); got != nil {
		t.Error("expected nil for pos outside file")
	}
}

func TestIsIdentFunctionInCall(t *testing.T) {
	id := &ast.Ident{Name: "f"}
	call := &ast.CallExpr{Fun: id}
	if !isIdentFunctionInCall(call, id) {
		t.Error("expected ident match")
	}
	sel := &ast.SelectorExpr{X: &ast.Ident{Name: "pkg"}, Sel: &ast.Ident{Name: "f"}}
	call = &ast.CallExpr{Fun: sel}
	if !isIdentFunctionInCall(call, sel.Sel) {
		t.Error("expected selector match")
	}
	call = &ast.CallExpr{Fun: &ast.FuncLit{}}
	if isIdentFunctionInCall(call, id) {
		t.Error("expected false for non-ident fun")
	}
}

func TestFindEnclosingFuncDetails(t *testing.T) {
	sig := types.NewSignature(nil, nil, nil, false)
	fnDecl := &ast.FuncDecl{Name: &ast.Ident{Name: "Foo"}, Type: &ast.FuncType{}, Body: &ast.BlockStmt{}}
	fnObj := types.NewFunc(token.NoPos, nil, "Foo", sig)
	info := &types.Info{Defs: map[*ast.Ident]types.Object{fnDecl.Name: fnObj}}
	gotSig, gotFunc, gotDecl := findEnclosingFuncDetails([]ast.Node{fnDecl}, info)
	if gotSig != sig || gotFunc != fnObj || gotDecl != fnDecl {
		t.Error("expected func decl details for func object")
	}

	varObj := types.NewVar(token.NoPos, nil, "Foo", sig)
	info = &types.Info{Defs: map[*ast.Ident]types.Object{fnDecl.Name: varObj}}
	gotSig, gotFunc, gotDecl = findEnclosingFuncDetails([]ast.Node{fnDecl}, info)
	if gotSig != sig || gotFunc != nil || gotDecl != fnDecl {
		t.Error("expected signature with non-func object")
	}

	info = &types.Info{Defs: map[*ast.Ident]types.Object{}}
	gotSig, gotFunc, gotDecl = findEnclosingFuncDetails([]ast.Node{fnDecl}, info)
	if gotSig != nil || gotFunc != nil || gotDecl != fnDecl {
		t.Error("expected nil signature when object missing")
	}

	lit := &ast.FuncLit{Type: &ast.FuncType{}, Body: &ast.BlockStmt{}}
	info = &types.Info{Types: map[ast.Expr]types.TypeAndValue{lit: {Type: sig}}}
	gotSig, gotFunc, gotDecl = findEnclosingFuncDetails([]ast.Node{lit}, info)
	if gotSig != sig || gotFunc != nil || gotDecl != nil {
		t.Error("expected func lit signature")
	}

	gotSig, gotFunc, gotDecl = findEnclosingFuncDetails([]ast.Node{&ast.GenDecl{}}, info)
	if gotSig != nil || gotFunc != nil || gotDecl != nil {
		t.Error("expected nil when no enclosing func")
	}
}

func TestIsEntryPointAndCanReturnError(t *testing.T) {
	mainPkg := types.NewPackage("main", "main")
	otherPkg := types.NewPackage("example.com/other", "other")
	sig := types.NewSignature(nil, nil, nil, false)
	if !IsEntryPoint(types.NewFunc(token.NoPos, mainPkg, "main", sig)) {
		t.Error("expected main in main package to be entry point")
	}
	if !IsEntryPoint(types.NewFunc(token.NoPos, mainPkg, "init", sig)) {
		t.Error("expected init to be entry point")
	}
	if IsEntryPoint(types.NewFunc(token.NoPos, otherPkg, "main", sig)) {
		t.Error("unexpected entry point for non-main package")
	}
	if IsEntryPoint(types.NewFunc(token.NoPos, mainPkg, "other", sig)) {
		t.Error("unexpected entry point for other function")
	}

	errType := types.Universe.Lookup("error").Type()
	sigErr := types.NewSignature(nil, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", errType)), false)
	if canReturnError(nil) {
		t.Error("expected false for nil signature")
	}
	if canReturnError(sig) {
		t.Error("expected false for void signature")
	}
	if !canReturnError(sigErr) {
		t.Error("expected true for error signature")
	}
}

func TestGetErrorReturnNameAndWrapper(t *testing.T) {
	if got := getErrorReturnNameDST(&dst.FuncType{}); got != "" {
		t.Errorf("expected empty name for nil results, got %q", got)
	}
	ft := &dst.FuncType{
		Results: &dst.FieldList{List: []*dst.Field{{Names: []*dst.Ident{dst.NewIdent("err")}, Type: dst.NewIdent("error")}}},
	}
	if got := getErrorReturnNameDST(ft); got != "err" {
		t.Errorf("expected err, got %q", got)
	}
	ft2 := &dst.FuncType{
		Results: &dst.FieldList{List: []*dst.Field{{Names: []*dst.Ident{dst.NewIdent("e")}, Type: dst.NewIdent("error")}}},
	}
	if got := getErrorReturnNameDST(ft2); got != "e" {
		t.Errorf("expected e, got %q", got)
	}
	ft3 := &dst.FuncType{
		Results: &dst.FieldList{List: []*dst.Field{{Type: dst.NewIdent("error")}}},
	}
	if got := getErrorReturnNameDST(ft3); got != "" {
		t.Errorf("expected empty name for unnamed error, got %q", got)
	}
	ft4 := &dst.FuncType{
		Results: &dst.FieldList{List: []*dst.Field{{Type: dst.NewIdent("int")}}},
	}
	if got := getErrorReturnNameDST(ft4); got != "" {
		t.Errorf("expected empty name for non-error result, got %q", got)
	}
	if !isErrorDstExprWrapper(dst.NewIdent("error")) {
		t.Error("expected error wrapper true")
	}
	if isErrorDstExprWrapper(dst.NewIdent("other")) {
		t.Error("expected error wrapper false")
	}
	if isErrorDstExprWrapper(&dst.SelectorExpr{X: dst.NewIdent("pkg"), Sel: dst.NewIdent("Err")}) {
		t.Error("expected error wrapper false for selector")
	}
}

func TestEnsureImportDST(t *testing.T) {
	file := &dst.File{
		Name: dst.NewIdent("p"),
		Imports: []*dst.ImportSpec{
			{Path: &dst.BasicLit{Kind: token.STRING, Value: `"fmt"`}},
		},
		Decls: []dst.Decl{},
	}
	ensureImportDST(file, "fmt")
	if len(file.Imports) != 1 {
		t.Error("expected no duplicate import")
	}
	ensureImportDST(file, "log")
	if len(file.Imports) != 2 {
		t.Error("expected import to be added")
	}
}

func TestGetScope(t *testing.T) {
	src := `package main
func main() { 
  var x int
  _ = x
} 
`
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "main.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var block *ast.BlockStmt
	var ident *ast.Ident
	ast.Inspect(astFile, func(n ast.Node) bool {
		if b, ok := n.(*ast.BlockStmt); ok && block == nil {
			block = b
		}
		if id, ok := n.(*ast.Ident); ok && id.Name == "x" {
			ident = id
		}
		return true
	})
	if block == nil || ident == nil {
		t.Fatal("nodes not found")
	}
	scopeBlock := types.NewScope(nil, 1, 100, "block")
	scopeFile := types.NewScope(nil, 1, 100, "file")
	info := &types.Info{Scopes: map[ast.Node]*types.Scope{block: scopeBlock, astFile: scopeFile}}
	pkg := &packages.Package{
		Fset:      fset,
		Syntax:    []*ast.File{astFile},
		Types:     types.NewPackage("main", "main"),
		TypesInfo: info,
	}
	if scope := getScope(pkg, block); scope != scopeBlock {
		t.Error("expected direct scope lookup")
	}
	if scope := getScope(pkg, ident); scope != scopeBlock {
		t.Error("expected path scope lookup")
	}

	pkgNoScopes := &packages.Package{
		Fset:      pkg.Fset,
		Syntax:    pkg.Syntax,
		Types:     pkg.Types,
		TypesInfo: &types.Info{Scopes: map[ast.Node]*types.Scope{}},
	}
	if scope := getScope(pkgNoScopes, ident); scope != pkg.Types.Scope() {
		t.Error("expected fallback to package scope")
	}
	pkgNilInfo := &packages.Package{Types: pkg.Types}
	if getScope(pkgNilInfo, ident) != nil {
		t.Error("expected nil for missing types info")
	}
	if getScope(nil, nil) != nil {
		t.Error("expected nil for nil package")
	}
}

func TestGetSignatureAndEquivalence(t *testing.T) {
	errType := types.Universe.Lookup("error").Type()
	sigA := types.NewSignature(nil, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", errType)), false)
	sigB := types.NewSignature(nil, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", errType)), false)
	sigC := types.NewSignature(nil, nil, nil, false)
	sigInt := types.NewSignature(nil, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", types.Typ[types.Int])), false)

	fn := types.NewFunc(token.NoPos, nil, "f", sigA)
	v := types.NewVar(token.NoPos, nil, "v", sigA)
	vOther := types.NewVar(token.NoPos, nil, "v2", types.Typ[types.Int])
	if getSignature(fn) != sigA {
		t.Error("expected signature for func")
	}
	if getSignature(v) != sigA {
		t.Error("expected signature for var")
	}
	if getSignature(vOther) != nil {
		t.Error("expected nil signature for non-func var")
	}
	if getSignature(types.NewTypeName(token.NoPos, nil, "T", nil)) != nil {
		t.Error("expected nil signature for non-callable object")
	}

	if signaturesEquivalent(nil, sigA) {
		t.Error("expected false for nil signature")
	}
	if signaturesEquivalent(sigA, sigC) {
		t.Error("expected false for different results")
	}
	if signaturesEquivalent(sigA, sigInt) {
		t.Error("expected false for different result types")
	}
	if !signaturesEquivalent(sigA, sigB) {
		t.Error("expected signatures to be equivalent")
	}
}

func TestHandleEntryPoint_MapError(t *testing.T) {
	src := `package main
func Target() {} 
func main() { Target() } 
`
	pkg, astFile, _ := setupPropagateEnv(t, src)
	call, stmt := findCallStmt(t, astFile, "Target")
	if err := HandleEntryPoint(pkg, &dst.File{Name: dst.NewIdent("main")}, call, stmt, "log-fatal"); err == nil {
		t.Error("expected mapping error")
	}
}

func TestHandleTestError_MapError(t *testing.T) {
	src := `package main
import "testing" 
func Target() {} 
func TestFoo(t *testing.T) { Target() } 
`
	pkg, astFile, _ := setupPropagateEnv(t, src)
	call, stmt := findCallStmt(t, astFile, "Target")
	if err := HandleTestError(pkg, &dst.File{Name: dst.NewIdent("main")}, call, stmt, "t"); err == nil {
		t.Error("expected mapping error")
	}
}

func TestHandleTestError_NoFile(t *testing.T) {
	pkg := &packages.Package{}
	stmt := &ast.ExprStmt{X: &ast.Ident{Name: "x"}}
	if err := HandleTestError(pkg, &dst.File{}, nil, stmt, "t"); err == nil {
		t.Error("expected error for missing AST file")
	}
}

func TestFindIdentForObj(t *testing.T) {
	_, astFile, _ := setupPropagateEnv(t, "package main\nvar x int")
	info := &types.Info{
		Defs: make(map[*ast.Ident]types.Object),
	}
	var defIdent *ast.Ident
	ast.Inspect(astFile, func(n ast.Node) bool {
		if vs, ok := n.(*ast.ValueSpec); ok && len(vs.Names) > 0 {
			defIdent = vs.Names[0]
			return false
		}
		return true
	})
	if defIdent == nil {
		t.Fatal("def ident not found")
	}
	info.Defs[defIdent] = types.NewVar(defIdent.Pos(), nil, "x", types.Typ[types.Int])
	obj := info.Defs[defIdent]
	if findIdentForObj(astFile, obj) == nil {
		t.Error("expected to find ident for object")
	}
	if findIdentForObj(astFile, types.NewVar(token.NoPos, nil, "y", types.Typ[types.Int])) != nil {
		t.Error("expected nil for object not in file")
	}
}

func TestProcessVarPropagation_NoDefinition(t *testing.T) {
	src := `package main
func Target() {} 
func main() { Target() } 
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	targetFunc := findDecl(t, pkg, "Target")
	callID := findCall(t, astFile, "Target")
	n, next, err := processVarPropagationDST(pkg, &mockProvider{f: dstFile}, astFile, dstFile, callID, targetFunc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 || next != nil {
		t.Error("expected no updates without definition")
	}
}

func TestProcessVarPropagation_TargetSigMissing(t *testing.T) {
	src := `package main
func Target() {} 
var f = Target
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	var targetID *ast.Ident
	ast.Inspect(astFile, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "Target" {
			targetID = id
			return false
		}
		return true
	})
	if targetID == nil {
		t.Fatal("target ident not found")
	}
	badTarget := types.NewVar(token.NoPos, nil, "x", types.Typ[types.Int])
	if _, _, err := processVarPropagationDST(pkg, &mockProvider{f: dstFile}, astFile, dstFile, targetID, badTarget); err == nil {
		t.Error("expected error for missing target signature")
	}
}

func TestProcessVarPropagation_DefinitionOtherFile(t *testing.T) {
	files := map[string]string{
		"a.go": `package main
var f func() 
func Target() {} 
`,
		"b.go": `package main
func assign() { f = Target } 
`,
	}
	pkg, astFiles, dstFiles := setupMultiFileEnv(t, files)
	var assignFile *ast.File
	for _, f := range astFiles {
		if strings.HasSuffix(pkg.Fset.File(f.Pos()).Name(), "b.go") {
			assignFile = f
		}
	}
	if assignFile == nil {
		t.Fatal("assign file not found")
	}
	var targetID *ast.Ident
	ast.Inspect(assignFile, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "Target" {
			targetID = id
			return false
		}
		return true
	})
	if targetID == nil {
		t.Fatal("target ident not found")
	}
	errType := types.Universe.Lookup("error").Type()
	sigErr := types.NewSignature(nil, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", errType)), false)
	targetFunc := types.NewFunc(token.NoPos, nil, "Target", sigErr)
	provider := &mapProvider{files: dstFiles}
	n, _, err := processVarPropagationDST(pkg, provider, assignFile, dstFiles[assignFile], targetID, targetFunc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Errorf("expected update count 1, got %d", n)
	}
	var defFile *ast.File
	for f := range dstFiles {
		if strings.HasSuffix(pkg.Fset.File(f.Pos()).Name(), "a.go") {
			defFile = f
		}
	}
	out := renderDST(t, dstFiles[defFile])
	if !strings.Contains(out, "var f func() error") {
		t.Errorf("expected definition type update, got:\n%s", out)
	}
}

func TestProcessVarPropagation_AlreadyHasError(t *testing.T) {
	src := `package main
func Target() error { return nil } 
var f func() error = Target
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	var targetID *ast.Ident
	ast.Inspect(astFile, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "Target" {
			targetID = id
			return false
		}
		return true
	})
	if targetID == nil {
		t.Fatal("target ident not found")
	}
	targetFunc := pkg.Types.Scope().Lookup("Target").(*types.Func)
	n, _, err := processVarPropagationDST(pkg, &mockProvider{f: dstFile}, astFile, dstFile, targetID, targetFunc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected no update when signatures match, got %d", n)
	}
}

func TestProcessVarPropagation_DefinitionGetError(t *testing.T) {
	src := `package main
func Target() {} 
var f = Target
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	var targetID *ast.Ident
	ast.Inspect(astFile, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "Target" {
			targetID = id
			return false
		}
		return true
	})
	if targetID == nil {
		t.Fatal("target ident not found")
	}
	targetFunc := pkg.Types.Scope().Lookup("Target").(*types.Func)
	if _, _, err := processVarPropagationDST(pkg, &errorProvider{err: errors.New("boom")}, astFile, dstFile, targetID, targetFunc); err == nil {
		t.Error("expected provider error")
	}
}

func TestProcessVarPropagation_PatchVarTypeError_DefiningIdent(t *testing.T) {
	src := `package main
func Target() {} 
var f func() 
func main() { f = Target } 
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	var targetID *ast.Ident
	ast.Inspect(astFile, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "Target" {
			targetID = id
			return false
		}
		return true
	})
	if targetID == nil {
		t.Fatal("target ident not found")
	}
	errType := types.Universe.Lookup("error").Type()
	sigErr := types.NewSignature(nil, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", errType)), false)
	targetFunc := types.NewFunc(token.NoPos, nil, "Target", sigErr)
	orig := patchVarTypeFn
	patchVarTypeFn = func(*types.Info, *ast.Ident, *types.Signature) (*types.Var, error) {
		return nil, errors.New("boom")
	}
	t.Cleanup(func() { patchVarTypeFn = orig })
	if _, _, err := processVarPropagationDST(pkg, &mockProvider{f: dstFile}, astFile, dstFile, targetID, targetFunc); err == nil {
		t.Error("expected PatchVarType error")
	}
}

func TestProcessVarPropagation_PatchVarTypeError_DefFile(t *testing.T) {
	files := map[string]string{
		"a.go": `package main
var f func() 
func Target() {} 
`,
		"b.go": `package main
func assign() { f = Target } 
`,
	}
	pkg, astFiles, dstFiles := setupMultiFileEnv(t, files)
	var assignFile *ast.File
	for _, f := range astFiles {
		if strings.HasSuffix(pkg.Fset.File(f.Pos()).Name(), "b.go") {
			assignFile = f
		}
	}
	if assignFile == nil {
		t.Fatal("assign file not found")
	}
	var targetID *ast.Ident
	ast.Inspect(assignFile, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "Target" {
			targetID = id
			return false
		}
		return true
	})
	if targetID == nil {
		t.Fatal("target ident not found")
	}
	errType := types.Universe.Lookup("error").Type()
	sigErr := types.NewSignature(nil, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", errType)), false)
	targetFunc := types.NewFunc(token.NoPos, nil, "Target", sigErr)
	orig := patchVarTypeFn
	patchVarTypeFn = func(*types.Info, *ast.Ident, *types.Signature) (*types.Var, error) {
		return nil, errors.New("boom")
	}
	t.Cleanup(func() { patchVarTypeFn = orig })
	if _, _, err := processVarPropagationDST(pkg, &mapProvider{files: dstFiles}, assignFile, dstFiles[assignFile], targetID, targetFunc); err == nil {
		t.Error("expected PatchVarType error")
	}
}

func TestPropagateCallers_ProcessVarPropagationError(t *testing.T) {
	src := `package main
func Target() {} 
var f = Target
`
	pkg, astFile, dstFile := setupPropagateEnv(t, src)
	var targetDecl *ast.FuncDecl
	for _, d := range astFile.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "Target" {
			targetDecl = fd
		}
	}
	if targetDecl == nil {
		t.Fatal("Target decl not found")
	}
	AddErrorToSignature(pkg.Fset, targetDecl)
	if err := PatchSignature(pkg.TypesInfo, targetDecl, pkg.Types); err != nil {
		t.Fatalf("PatchSignature failed: %v", err)
	}
	targetFunc := pkg.TypesInfo.ObjectOf(targetDecl.Name)
	orig := patchVarTypeFn
	patchVarTypeFn = func(*types.Info, *ast.Ident, *types.Signature) (*types.Var, error) {
		return nil, errors.New("boom")
	}
	t.Cleanup(func() { patchVarTypeFn = orig })
	if _, err := PropagateCallers([]*packages.Package{pkg}, &mockProvider{f: dstFile}, targetFunc, "log-fatal"); err == nil {
		t.Error("expected error from processVarPropagationDST")
	}
}

func TestRefactorCallSite_TailOptimization(t *testing.T) {
	call := &dst.CallExpr{Fun: dst.NewIdent("Target")}
	exprStmt := &dst.ExprStmt{X: call}
	block := &dst.BlockStmt{List: []dst.Stmt{exprStmt}}
	errType := types.Universe.Lookup("error").Type()
	sig := types.NewSignature(nil, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", errType)), false)
	ctx := rewriteContext{
		stmt:         exprStmt,
		parent:       block,
		enclosingSig: sig,
		targetSig:    sig,
		isTerminal:   false,
	}
	if err := refactorCallSiteDST(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := block.List[0].(*dst.ReturnStmt); !ok {
		t.Error("expected tail call to be replaced with return")
	}
}

func TestRefactorCallSite_Unsupported(t *testing.T) {
	// ForStmt not yet supported in propagate.go
	ctx := rewriteContext{stmt: &dst.ForStmt{}}
	if err := refactorCallSiteDST(ctx); err == nil {
		t.Error("expected error for unsupported stmt type")
	}
}

func TestRefactorGoStmt_ClearsTestParam(t *testing.T) {
	call := &dst.CallExpr{Fun: dst.NewIdent("Target")}
	gs := &dst.GoStmt{Call: call}
	block := &dst.BlockStmt{List: []dst.Stmt{gs}}
	file := &dst.File{Name: dst.NewIdent("p")}
	ctx := rewriteContext{
		dstFile:    file,
		parent:     block,
		isTerminal: false,
		strategy:   HandlerPanic,
		testParam:  "t",
	}
	if err := refactorGoStmt(ctx, gs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := renderDST(t, &dst.File{Name: dst.NewIdent("p"), Decls: []dst.Decl{
		&dst.FuncDecl{Name: dst.NewIdent("f"), Type: &dst.FuncType{}, Body: block},
	}})
	if strings.Contains(out, "t.Fatal") {
		t.Error("expected test param to be cleared in goroutine")
	}
	if !strings.Contains(out, "panic(err)") {
		t.Error("expected panic handler in goroutine")
	}
}

func TestRefactorDeferStmt_JoinsErrors(t *testing.T) {
	call := &dst.CallExpr{Fun: dst.NewIdent("Target")}
	ds := &dst.DeferStmt{Call: call}
	block := &dst.BlockStmt{List: []dst.Stmt{ds}}
	errType := types.Universe.Lookup("error").Type()
	sig := types.NewSignature(nil, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", errType)), false)
	fnDecl := &dst.FuncDecl{
		Name: dst.NewIdent("f"),
		Type: &dst.FuncType{
			Results: &dst.FieldList{List: []*dst.Field{
				{Names: []*dst.Ident{dst.NewIdent("err")}, Type: dst.NewIdent("error")},
			}},
		},
		Body: block,
	}
	file := &dst.File{Name: dst.NewIdent("p"), Decls: []dst.Decl{fnDecl}}
	ctx := rewriteContext{
		dstFile:        file,
		parent:         block,
		enclosingSig:   sig,
		enclosingFnDst: fnDecl,
	}
	if err := refactorDeferStmt(ctx, ds); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := renderDST(t, file)
	if !strings.Contains(out, "errors.Join") {
		t.Errorf("expected errors.Join in defer handler, got:\n%s", out)
	}
}

func TestRefactorDeferStmt_MissingErrNameFallsBack(t *testing.T) {
	call := &dst.CallExpr{Fun: dst.NewIdent("Target")}
	ds := &dst.DeferStmt{Call: call}
	block := &dst.BlockStmt{List: []dst.Stmt{ds}}
	errType := types.Universe.Lookup("error").Type()
	sig := types.NewSignature(nil, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", errType)), false)
	fnDecl := &dst.FuncDecl{
		Name: dst.NewIdent("f"),
		Type: &dst.FuncType{
			Results: &dst.FieldList{List: []*dst.Field{
				{Names: []*dst.Ident{dst.NewIdent("res")}, Type: dst.NewIdent("int")},
				{Type: dst.NewIdent("error")},
			}},
		},
		Body: block,
	}
	file := &dst.File{Name: dst.NewIdent("p"), Decls: []dst.Decl{fnDecl}}
	ctx := rewriteContext{
		dstFile:        file,
		parent:         block,
		enclosingSig:   sig,
		enclosingFnDst: fnDecl,
	}
	if err := refactorDeferStmt(ctx, ds); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := renderDST(t, file)
	if !strings.Contains(out, "log.Printf") {
		t.Errorf("expected log fallback, got:\n%s", out)
	}
}

func TestRefactorDeferStmt_EnsureNamedReturnsError(t *testing.T) {
	call := &dst.CallExpr{Fun: dst.NewIdent("Target")}
	ds := &dst.DeferStmt{Call: call}
	block := &dst.BlockStmt{List: []dst.Stmt{ds}}
	errType := types.Universe.Lookup("error").Type()
	sig := types.NewSignature(nil, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", errType)), false)
	fnDecl := &dst.FuncDecl{
		Name: dst.NewIdent("f"),
		Type: &dst.FuncType{
			Results: &dst.FieldList{List: []*dst.Field{
				{Names: []*dst.Ident{dst.NewIdent("err")}, Type: dst.NewIdent("error")},
			}},
		},
		Body: block,
	}
	file := &dst.File{Name: dst.NewIdent("p"), Decls: []dst.Decl{fnDecl}}
	orig := ensureNamedReturnsDSTFn
	ensureNamedReturnsDSTFn = func(*dst.FuncDecl) (bool, error) {
		return false, errors.New("boom")
	}
	t.Cleanup(func() { ensureNamedReturnsDSTFn = orig })
	ctx := rewriteContext{
		dstFile:        file,
		parent:         block,
		enclosingSig:   sig,
		enclosingFnDst: fnDecl,
	}
	if err := refactorDeferStmt(ctx, ds); err == nil {
		t.Error("expected EnsureNamedReturnsDST error")
	}
}

func TestRefactorSwitchStmt_Init(t *testing.T) {
	ctx := rewriteContext{}
	call := &dst.CallExpr{Fun: dst.NewIdent("Target")}
	ctx.call = call
	stmt := &dstSwitchStmt{
		Init: &dst.ExprStmt{X: call},
		Body: &dst.BlockStmt{},
	}
	_ = stmt
	// Handled via E2E logic in propagate_test.go usually, checking helper logic here if needed
}

type dstSwitchStmt = dst.SwitchStmt
