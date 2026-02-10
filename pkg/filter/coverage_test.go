package filter

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

func TestMatchesFile_ReadError(t *testing.T) {
	fset := token.NewFileSet()
	f := fset.AddFile("/path/does/not/exist.go", -1, 1)
	pos := f.Pos(1)
	flt := New(nil, nil)
	if _, err := flt.MatchesFile(fset, pos); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestMatchesFile_FileNotInFset(t *testing.T) {
	fset := token.NewFileSet()
	other := token.NewFileSet()
	f := other.AddFile("/tmp/other.go", -1, 1)
	pos := f.Pos(1)
	flt := New(nil, nil)
	if ok, err := flt.MatchesFile(fset, pos); err != nil || ok {
		t.Fatalf("expected false/nil when file not in fset, got ok=%v err=%v", ok, err)
	}
}

func TestIsGeneratedFile_ScannerErr(t *testing.T) {
	tmpDir := t.TempDir()
	path := tmpDir + "/long.go"
	longLine := make([]byte, bufio.MaxScanTokenSize+10)
	for i := range longLine {
		longLine[i] = 'a'
	}
	if err := os.WriteFile(path, longLine, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := isGeneratedFile(path); err == nil {
		t.Fatal("expected scanner error for long line")
	}
}

func TestIsTestingTypeBranches(t *testing.T) {
	cases := []struct {
		name string
		expr ast.Expr
		want bool
	}{
		{name: "NotStar", expr: &ast.Ident{Name: "testing"}, want: false},
		{name: "StarNonSelector", expr: &ast.StarExpr{X: &ast.Ident{Name: "T"}}, want: false},
		{name: "SelectorNonIdent", expr: &ast.StarExpr{X: &ast.SelectorExpr{X: &ast.BasicLit{}, Sel: &ast.Ident{Name: "T"}}}, want: false},
		{name: "WrongPkg", expr: &ast.StarExpr{X: &ast.SelectorExpr{X: &ast.Ident{Name: "other"}, Sel: &ast.Ident{Name: "T"}}}, want: false},
		{name: "WrongType", expr: &ast.StarExpr{X: &ast.SelectorExpr{X: &ast.Ident{Name: "testing"}, Sel: &ast.Ident{Name: "M"}}}, want: false},
		{name: "CorrectType", expr: &ast.StarExpr{X: &ast.SelectorExpr{X: &ast.Ident{Name: "testing"}, Sel: &ast.Ident{Name: "T"}}}, want: true},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTestingType(tt.expr); got != tt.want {
				t.Fatalf("isTestingType()=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsHelperMethodCallBranches(t *testing.T) {
	cases := []struct {
		name  string
		call  *ast.CallExpr
		param string
		want  bool
	}{
		{name: "NonSelector", call: &ast.CallExpr{Fun: &ast.Ident{Name: "Helper"}}, param: "t", want: false},
		{name: "WrongMethod", call: &ast.CallExpr{Fun: &ast.SelectorExpr{X: &ast.Ident{Name: "t"}, Sel: &ast.Ident{Name: "Other"}}}, param: "t", want: false},
		{name: "NonIdentReceiver", call: &ast.CallExpr{Fun: &ast.SelectorExpr{X: &ast.BasicLit{}, Sel: &ast.Ident{Name: "Helper"}}}, param: "t", want: false},
		{name: "WrongReceiver", call: &ast.CallExpr{Fun: &ast.SelectorExpr{X: &ast.Ident{Name: "x"}, Sel: &ast.Ident{Name: "Helper"}}}, param: "t", want: false},
		{name: "Match", call: &ast.CallExpr{Fun: &ast.SelectorExpr{X: &ast.Ident{Name: "t"}, Sel: &ast.Ident{Name: "Helper"}}}, param: "t", want: true},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := isHelperMethodCall(tt.call, tt.param); got != tt.want {
				t.Fatalf("isHelperMethodCall()=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTestingFunc_MultiNames(t *testing.T) {
	src := `package p
func TestFoo(t, u *testing.T) {}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	var decl *ast.FuncDecl
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			decl = fd
			break
		}
	}
	if decl == nil {
		t.Fatal("missing func decl")
	}
	if got := isTestingFunc(decl, "T"); got {
		t.Fatal("expected false for multiple names")
	}
}

func TestIsTestingFunc_Branches(t *testing.T) {
	cases := []struct {
		name string
		decl *ast.FuncDecl
		want bool
	}{
		{name: "NoParams", decl: &ast.FuncDecl{Type: &ast.FuncType{Params: &ast.FieldList{}}}, want: false},
		{name: "NonStar", decl: &ast.FuncDecl{Type: &ast.FuncType{Params: &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{ast.NewIdent("t")}, Type: ast.NewIdent("testing.T")}}}}}, want: false},
		{name: "NonSelector", decl: &ast.FuncDecl{Type: &ast.FuncType{Params: &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{ast.NewIdent("t")}, Type: &ast.StarExpr{X: ast.NewIdent("T")}}}}}}, want: false},
		{name: "PkgNotIdent", decl: &ast.FuncDecl{Type: &ast.FuncType{Params: &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{ast.NewIdent("t")}, Type: &ast.StarExpr{X: &ast.SelectorExpr{X: &ast.BasicLit{}, Sel: ast.NewIdent("T")}}}}}}}, want: false},
		{name: "WrongPkg", decl: &ast.FuncDecl{Type: &ast.FuncType{Params: &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{ast.NewIdent("t")}, Type: &ast.StarExpr{X: &ast.SelectorExpr{X: ast.NewIdent("other"), Sel: ast.NewIdent("T")}}}}}}}, want: false},
		{name: "WrongTypeName", decl: &ast.FuncDecl{Type: &ast.FuncType{Params: &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{ast.NewIdent("t")}, Type: &ast.StarExpr{X: &ast.SelectorExpr{X: ast.NewIdent("testing"), Sel: ast.NewIdent("X")}}}}}}}, want: false},
		{name: "Valid", decl: &ast.FuncDecl{Type: &ast.FuncType{Params: &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{ast.NewIdent("t")}, Type: &ast.StarExpr{X: &ast.SelectorExpr{X: ast.NewIdent("testing"), Sel: ast.NewIdent("T")}}}}}}}, want: true},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTestingFunc(tt.decl, "T"); got != tt.want {
				t.Fatalf("isTestingFunc()=%v, want %v", got, tt.want)
			}
		})
	}
}
