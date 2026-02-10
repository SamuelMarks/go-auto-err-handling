package runner

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"testing"

	"github.com/dave/dst"
	"golang.org/x/tools/go/packages"
)

func parseTestFile(t *testing.T) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", "package main\nfunc main() {}\n", 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	return fset, file
}

func TestDstManagerGetBranches(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	fset, file := parseTestFile(t)
	pkg := &packages.Package{Fset: fset}

	mgr := &dstManager{
		fset:     token.NewFileSet(),
		cache:    map[string]*dst.File{},
		modified: map[string]bool{},
	}
	if _, err := mgr.Get(pkg, file); err == nil {
		t.Fatal("expected error for missing tok file")
	}

	decorateFileFn = func(*token.FileSet, *ast.File) (*dst.File, error) {
		return nil, errors.New("decorate boom")
	}
	mgr = newDstManager([]*packages.Package{pkg})
	if _, err := mgr.Get(pkg, file); err == nil {
		t.Fatal("expected decorate error")
	}

	decorateFileFn = func(*token.FileSet, *ast.File) (*dst.File, error) {
		return &dst.File{}, nil
	}
	if _, err := mgr.Get(pkg, file); err != nil {
		t.Fatalf("unexpected get error: %v", err)
	}
	name := fset.File(file.Pos()).Name()
	mgr.cache[name] = &dst.File{}
	if _, err := mgr.Get(pkg, file); err != nil {
		t.Fatalf("unexpected cache get error: %v", err)
	}
}

func TestDstManagerPrintDiffsAndSaveErrors(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	mgr := &dstManager{
		cache:    map[string]*dst.File{"missing.go": &dst.File{}},
		modified: map[string]bool{"missing.go": true},
	}

	readFileFn = func(string) ([]byte, error) {
		return nil, errors.New("read boom")
	}
	if err := mgr.PrintDiffs(os.Stdout); err == nil {
		t.Fatal("expected read error")
	}

	readFileFn = func(string) ([]byte, error) {
		return []byte("package main\n"), nil
	}
	restorerFprintFn = func(io.Writer, *dst.File) error {
		return errors.New("fprint boom")
	}
	if err := mgr.PrintDiffs(os.Stdout); err == nil {
		t.Fatal("expected fprint error")
	}

	restorerFprintFn = func(io.Writer, *dst.File) error { return nil }
	if err := mgr.PrintDiffs(os.Stdout); err != nil {
		t.Fatalf("unexpected PrintDiffs error: %v", err)
	}

	createFileFn = func(string) (*os.File, error) {
		return nil, errors.New("create boom")
	}
	if err := mgr.Save(); err == nil {
		t.Fatal("expected create error")
	}

	tmp, err := os.CreateTemp("", "save-*.go")
	if err != nil {
		t.Fatalf("tempfile error: %v", err)
	}
	_ = tmp.Close()
	createFileFn = func(string) (*os.File, error) {
		return os.Create(tmp.Name())
	}
	restorerFprintFn = func(io.Writer, *dst.File) error {
		return errors.New("fprint boom")
	}
	if err := mgr.Save(); err == nil {
		t.Fatal("expected save fprint error")
	}

	restorerFprintFn = func(io.Writer, *dst.File) error { return nil }
	if err := mgr.Save(); err != nil {
		t.Fatalf("unexpected save error: %v", err)
	}
	_ = os.Remove(tmp.Name())
}
