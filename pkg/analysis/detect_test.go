package analysis

import (
	"bytes"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"log"
	"testing"

	"golang.org/x/tools/go/packages"
)

func loadTypesAllowError(t *testing.T, src string) (*types.Package, *ast.File, *types.Info, *token.FileSet) {
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
	conf := types.Config{
		Importer: importer.Default(),
		Error:    func(error) {},
	}
	pkg, _ := conf.Check("p", fset, []*ast.File{file}, info)
	if pkg == nil {
		t.Fatal("expected package even with type errors")
	}
	return pkg, file, info, fset
}

func TestDetect_Cases(t *testing.T) {
	src := `package p
import "errors"

type S struct{ F error }

func fail() error { return errors.New("x") }
func ok() int { return 1 }
func okBool() bool { return true }
func multi() (int, error) { return 0, errors.New("x") }
func use(S) {}

func run() error {
	fail()
	ok()
	fail().Error()
	use(S{F: fail()})
	_, _ = fail(), ok()
	_, _ = fail().Error(), ok()
	err := fail()
	_ = err
	_, _ = multi()
	defer fail()
	defer ok()
	defer fail().Error()
	go fail()
	go ok()
	go fail().Error()
	if okBool() {}
	if fail() {}
	switch fail() { default: }
	return S{F: fail()}
}

var _, _ = multi()
var _ = fail().Error()
var _ = S{F: fail()}
`

	pkgTypes, file, info, fset := loadTypesAllowError(t, src)

	// Ensure error-returning calls have type info even with type errors.
	errType := types.Universe.Lookup("error").Type()
	tuple := types.NewTuple(
		types.NewVar(token.NoPos, nil, "", types.Typ[types.Int]),
		types.NewVar(token.NoPos, nil, "", errType),
	)
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok {
			switch ident.Name {
			case "fail":
				info.Types[call] = types.TypeAndValue{Type: errType}
			case "multi":
				info.Types[call] = types.TypeAndValue{Type: tuple}
			}
		}
		return true
	})

	pkg := &packages.Package{
		Fset:      fset,
		Types:     pkgTypes,
		TypesInfo: info,
		Syntax:    []*ast.File{file},
	}

	var buf bytes.Buffer
	origOutput := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(origOutput)

	points, err := Detect([]*packages.Package{pkg}, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) == 0 {
		t.Fatal("expected at least one injection point")
	}
	if buf.Len() == 0 {
		t.Fatal("expected debug logging output")
	}

	want := map[string]bool{
		"expr":   false,
		"assign": false,
		"defer":  false,
		"go":     false,
		"if":     false,
		"switch": false,
		"return": false,
		"global": false,
	}

	for _, pt := range points {
		if pt.Stmt == nil {
			want["global"] = true
			continue
		}
		switch pt.Stmt.(type) {
		case *ast.ExprStmt:
			want["expr"] = true
		case *ast.AssignStmt:
			want["assign"] = true
		case *ast.DeferStmt:
			want["defer"] = true
		case *ast.GoStmt:
			want["go"] = true
		case *ast.IfStmt:
			want["if"] = true
		case *ast.SwitchStmt:
			want["switch"] = true
		case *ast.ReturnStmt:
			want["return"] = true
		}
	}

	for k, ok := range want {
		if !ok {
			t.Fatalf("expected injection point for %s", k)
		}
	}
}
