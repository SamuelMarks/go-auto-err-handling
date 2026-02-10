package analysis

import (
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/filter"
	"golang.org/x/tools/go/packages"
)

func loadTypes(t *testing.T, src string) (*types.Package, *ast.File, *types.Info, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("p", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatalf("typecheck error: %v", err)
	}
	return pkg, file, info, fset
}

func findCall(t *testing.T, file *ast.File, name string) *ast.CallExpr {
	t.Helper()
	var call *ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if call != nil {
			return false
		}
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == name {
				call = c
				return false
			}
		}
		return true
	})
	if call == nil {
		t.Fatalf("call %s not found", name)
	}
	return call
}

func TestGenerateUniqueName(t *testing.T) {
	if got := GenerateUniqueName(nil, "err"); got != "err" {
		t.Fatalf("expected err, got %q", got)
	}
	root := types.NewScope(nil, token.NoPos, token.NoPos, "root")
	root.Insert(types.NewVar(token.NoPos, nil, "err", types.Typ[types.Int]))
	root.Insert(types.NewVar(token.NoPos, nil, "err1", types.Typ[types.Int]))
	if got := GenerateUniqueName(root, "err"); got != "err2" {
		t.Fatalf("expected err2, got %q", got)
	}
}

func TestVerifyStructuralImplementation(t *testing.T) {
	pkg := types.NewPackage("p", "p")
	ifaceMethod := types.NewFunc(token.NoPos, pkg, "Run", types.NewSignatureType(nil, nil, nil, nil, nil, false))
	iface := types.NewInterfaceType([]*types.Func{ifaceMethod}, nil)
	iface.Complete()

	named := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "Service", nil), types.NewStruct(nil, nil), nil)
	recv := types.NewVar(token.NoPos, pkg, "s", types.NewPointer(named))
	methodSig := types.NewSignatureType(recv, nil, nil, nil, nil, false)
	method := types.NewFunc(token.NoPos, pkg, "Run", methodSig)
	named.AddMethod(method)

	if !verifyStructuralImplementation(types.NewPointer(named), iface) {
		t.Fatal("expected structural implementation to be true")
	}

	// Unexported method should require same package.
	pkgA := types.NewPackage("a", "a")
	pkgB := types.NewPackage("b", "b")
	unexported := types.NewFunc(token.NoPos, pkgA, "run", types.NewSignatureType(nil, nil, nil, nil, nil, false))
	iface2 := types.NewInterfaceType([]*types.Func{unexported}, nil)
	iface2.Complete()

	namedB := types.NewNamed(types.NewTypeName(token.NoPos, pkgB, "Other", nil), types.NewStruct(nil, nil), nil)
	recvB := types.NewVar(token.NoPos, pkgB, "o", types.NewPointer(namedB))
	methodSigB := types.NewSignatureType(recvB, nil, nil, nil, nil, false)
	methodB := types.NewFunc(token.NoPos, pkgB, "run", methodSigB)
	namedB.AddMethod(methodB)

	if verifyStructuralImplementation(types.NewPointer(namedB), iface2) {
		t.Fatal("expected structural implementation to be false for unexported method from different pkg")
	}
}

func TestInterfaceRegistryScanPackage(t *testing.T) {
	reg := &InterfaceRegistry{seen: make(map[*packages.Package]bool)}
	reg.scanPackage(nil)
	reg.scanPackage(&packages.Package{})

	pkg := types.NewPackage("p", "p")
	iface := types.NewInterfaceType(nil, nil)
	iface.Complete()
	pkg.Scope().Insert(types.NewTypeName(token.NoPos, pkg, "I", iface))

	dep := types.NewPackage("dep", "dep")
	depIface := types.NewInterfaceType(nil, nil)
	depIface.Complete()
	dep.Scope().Insert(types.NewTypeName(token.NoPos, dep, "J", depIface))

	depPkg := &packages.Package{Types: dep}
	mainPkg := &packages.Package{Types: pkg, Imports: map[string]*packages.Package{"dep": depPkg}}

	reg.scanPackage(mainPkg)
	if len(reg.interfaces) != 2 {
		t.Fatalf("expected 2 interfaces, got %d", len(reg.interfaces))
	}
	// Ensure no duplicates on re-scan
	reg.scanPackage(mainPkg)
	if len(reg.interfaces) != 2 {
		t.Fatalf("expected 2 interfaces after rescan, got %d", len(reg.interfaces))
	}
}

func TestCheckCompliance_NonSignature(t *testing.T) {
	pkg := types.NewPackage("p", "p")
	reg := &InterfaceRegistry{}
	fn := types.NewFunc(token.NoPos, pkg, "X", nil)
	if _, err := reg.CheckCompliance(fn); err == nil {
		t.Fatal("expected error for non-signature func")
	}
}

func TestCheckCompliance_NoImplementation(t *testing.T) {
	pkg := types.NewPackage("p", "p")
	ifaceMethod := types.NewFunc(token.NoPos, pkg, "Run", types.NewSignatureType(nil, nil, nil, nil, nil, false))
	iface := types.NewInterfaceType([]*types.Func{ifaceMethod}, nil)
	iface.Complete()
	ifaceName := types.NewTypeName(token.NoPos, pkg, "Runner", iface)

	reg := &InterfaceRegistry{interfaces: []*types.TypeName{ifaceName}}

	named := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "Service", nil), types.NewStruct(nil, nil), nil)
	recv := types.NewVar(token.NoPos, pkg, "s", types.NewPointer(named))
	sig := types.NewSignatureType(recv, nil, nil, nil, nil, false)
	method := types.NewFunc(token.NoPos, pkg, "Stop", sig)
	named.AddMethod(method)

	conflicts, err := reg.CheckCompliance(method)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %d", len(conflicts))
	}
}

func TestFindTokenFileAndResolveNodeContext(t *testing.T) {
	src := `package p
func fail() error { return nil }
func run() { _ = fail() }`
	pkg, file, info, fset := loadTypes(t, src)
	pos := findCall(t, file, "fail").Pos()
	ctx := fileContext{pkg: &packages.Package{Fset: fset, TypesInfo: info, Types: pkg}, file: file}
	point, ok := resolveNodeContext(ctx, pos)
	if !ok || point.Call == nil || point.Stmt == nil {
		t.Fatal("expected to resolve call context")
	}

	if findTokenFile(fset, "does-not-exist.go") != nil {
		t.Fatal("expected nil token file for missing path")
	}
}

func TestResolveNodeContext_NoCall(t *testing.T) {
	src := `package p
func run() { var x int; _ = x }`
	pkg, file, info, fset := loadTypes(t, src)
	ctx := fileContext{pkg: &packages.Package{Fset: fset, TypesInfo: info, Types: pkg}, file: file}
	point, ok := resolveNodeContext(ctx, file.Pos())
	if ok || point.Call != nil {
		t.Fatal("expected no call context")
	}
}

func TestPathEnclosing(t *testing.T) {
	src := `package p
func run() {}`
	_, file, _, _ := loadTypes(t, src)
	path := pathEnclosing(file, file.Pos(), file.Pos())
	if len(path) == 0 {
		t.Fatal("expected non-empty path")
	}
}

func TestVisitorHelpers(t *testing.T) {
	src := `package p
import "errors"
func single() error { return errors.New("x") }
func multi() (int, error) { return 1, errors.New("x") }
var g, _ = multi()
func use() {
	var f func() error = single
	_ = f
	f()
	_ = (&struct{F error}{F: single()})
}
`
	_, file, info, fset := loadTypes(t, src)

	callSingle := findCall(t, file, "single")
	ok, idx := isErrorReturningCall(info, callSingle)
	if !ok || idx != 0 {
		t.Fatalf("expected single error return, got ok=%v idx=%d", ok, idx)
	}

	callMulti := findCall(t, file, "multi")
	ok, idx = isErrorReturningCall(info, callMulti)
	if !ok || idx != 1 {
		t.Fatalf("expected error at index 1, got ok=%v idx=%d", ok, idx)
	}

	if !isBlankIdentifier(&ast.Ident{Name: "_"}) {
		t.Fatal("expected blank identifier true")
	}
	if isBlankIdentifier(nil) {
		t.Fatal("expected nil to be false")
	}

	var vSpec *ast.ValueSpec
	ast.Inspect(file, func(n ast.Node) bool {
		if vs, ok := n.(*ast.ValueSpec); ok {
			for _, name := range vs.Names {
				if name.Name == "g" {
					vSpec = vs
					return false
				}
			}
		}
		return true
	})
	if vSpec == nil {
		t.Fatal("expected value spec")
	}
	if !isGlobalErrorIgnored(info, vSpec, 0, callMulti) {
		t.Fatal("expected global error ignore to be true")
	}

	if findSafeEmbeddedCall(&ast.UnaryExpr{X: callSingle}) == nil {
		t.Fatal("expected to find embedded call")
	}
	if findSafeEmbeddedCall(nil) != nil {
		t.Fatal("expected nil for nil expr")
	}

	fn := getCalledFunction(info, callSingle)
	if fn == nil || fn.Name() != "single" {
		t.Fatal("expected resolved function for single")
	}

	// Variable function call should synthesize a func symbol named "f".
	var callVar *ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "f" {
				callVar = c
				return false
			}
		}
		return true
	})
	if callVar == nil {
		t.Fatal("expected call to f")
	}
	fnVar := getCalledFunction(info, callVar)
	if fnVar == nil || fnVar.Name() != "f" {
		t.Fatal("expected synthesized func for variable call")
	}

	// hasIgnoreDirective branch
	cmap := ast.NewCommentMap(fset, file, file.Comments)
	var stmt ast.Stmt
	ast.Inspect(file, func(n ast.Node) bool {
		if s, ok := n.(ast.Stmt); ok {
			stmt = s
			return false
		}
		return true
	})
	if hasIgnoreDirective(stmt, cmap) {
		t.Fatal("did not expect ignore directive")
	}

	// shouldInclude error path: MatchesFile returning error -> false
	badFilter := filter.New(nil, nil)
	if shouldInclude(&packages.Package{Fset: fset, TypesInfo: info}, file, callSingle, stmt, cmap, badFilter, true) {
		t.Fatal("expected shouldInclude to be false on MatchesFile error")
	}
	// direct shouldInclude true when no filters/directives
	if !shouldInclude(&packages.Package{Fset: fset, TypesInfo: info}, file, callSingle, stmt, cmap, nil, false) {
		t.Fatal("expected shouldInclude to be true")
	}
}

func TestErrcheckParser_ParseCoverage(t *testing.T) {
	// Prepare temp file on disk so absolute paths line up.
	src := "package p\nfunc fail() error { return nil }\nfunc run() { _ = fail() }\n"
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
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("p", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatalf("typecheck error: %v", err)
	}

	// Build packages.Package
	p := &packages.Package{Fset: fset, Types: pkg, TypesInfo: info, Syntax: []*ast.File{file}}
	parser := NewErrcheckParser([]*packages.Package{p})

	call := findCall(t, file, "fail")
	pos := fset.Position(call.Pos())
	line := pos.Line
	col := pos.Column

	lineText := fmt.Sprintf("%s:%d:%d: ignored", pos.Filename, line, col)
	points, err := parser.Parse(strings.NewReader(lineText))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("expected 1 point, got %d", len(points))
	}

	// Position that does not resolve to a call should be skipped.
	noCall := fmt.Sprintf("%s:1:1: ignored", pos.Filename)
	points, err = parser.Parse(strings.NewReader(noCall))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("expected 0 points, got %d", len(points))
	}
}

func TestErrcheckParser_ParseSkipsAndErrors(t *testing.T) {
	parser := &ErrcheckParser{fileMap: map[string]fileContext{}}
	points, err := parser.Parse(strings.NewReader("badline\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 0 {
		t.Fatal("expected no points")
	}

	// Scanner error
	bad := &errorReader{}
	_, err = parser.Parse(bad)
	if err == nil {
		t.Fatal("expected scanner error")
	}
}

func TestErrcheckParser_TokenFileMissingAndLineOutOfRange(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "missing.go", "package p\n", 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ctx := fileContext{pkg: &packages.Package{Fset: token.NewFileSet()}, file: file}
	abs, err := filepath.Abs("missing.go")
	if err != nil {
		t.Fatalf("abs error: %v", err)
	}
	parser := &ErrcheckParser{fileMap: map[string]fileContext{abs: ctx}}
	points, err := parser.Parse(strings.NewReader(fmt.Sprintf("%s:999:1: msg", abs)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 0 {
		t.Fatal("expected no points")
	}

	// Line out of range branch (token file exists).
	ctx2 := fileContext{pkg: &packages.Package{Fset: fset}, file: file}
	parser2 := &ErrcheckParser{fileMap: map[string]fileContext{abs: ctx2}}
	points, err = parser2.Parse(strings.NewReader(fmt.Sprintf("%s:999:1: msg", abs)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 0 {
		t.Fatal("expected no points for out-of-range line")
	}
}

func TestErrcheckParser_ParseAbsError(t *testing.T) {
	orig := filepathAbs
	defer func() { filepathAbs = orig }()
	filepathAbs = func(string) (string, error) {
		return "", errors.New("boom")
	}

	parser := &ErrcheckParser{fileMap: map[string]fileContext{}}
	points, err := parser.Parse(strings.NewReader("file.go:1:1: msg\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 0 {
		t.Fatal("expected no points when abs path fails")
	}
}

type errorReader struct{}

func (e *errorReader) Read(_ []byte) (int, error) {
	return 0, errors.New("boom")
}
