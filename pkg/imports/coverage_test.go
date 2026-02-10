package imports

import (
	"go/parser"
	"go/token"
	"testing"
)

func TestIdentifierExists(t *testing.T) {
	src := `package p
func f() { var used int; _ = used }`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if !identifierExists(file, "used") {
		t.Fatal("expected identifier to exist")
	}
	if identifierExists(file, "missing") {
		t.Fatal("did not expect identifier to exist")
	}
}

func TestGenerateSafeAliasSkipsExisting(t *testing.T) {
	src := `package p
var std_fmt_1 = 1
var std_fmt_2 = 2`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	alias := generateSafeAlias(file, "fmt")
	if alias == "std_fmt_1" || alias == "std_fmt_2" {
		t.Fatalf("expected alias to skip existing names, got %q", alias)
	}
}

func TestAddAliasedImport(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", "package p\n", 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	addAliasedImport(fset, file, "fmt2", "fmt")
	if len(file.Imports) != 1 {
		t.Fatalf("expected 1 import, got %d", len(file.Imports))
	}
	imp := file.Imports[0]
	if imp.Name == nil || imp.Name.Name != "fmt2" {
		t.Fatalf("expected alias fmt2, got %+v", imp.Name)
	}
}
