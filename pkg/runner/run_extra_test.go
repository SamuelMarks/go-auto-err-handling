package runner

import (
	"errors"
	"go/ast"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/analysis"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/filter"
	"github.com/dave/dst"
	"golang.org/x/tools/go/packages"
)

func TestRun_LoadAndAnalysisErrors(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	loadPackagesFn = func([]string, string) ([]*packages.Package, error) {
		return nil, errors.New("load boom")
	}
	if err := Run(Options{Paths: []string{"."}}); err == nil || !strings.Contains(err.Error(), "load failed") {
		t.Fatalf("expected load failed error, got %v", err)
	}

	loadPackagesFn = func([]string, string) ([]*packages.Package, error) {
		return []*packages.Package{{}}, nil
	}
	detectFn = func([]*packages.Package, *filter.Filter, bool) ([]analysis.InjectionPoint, error) {
		return nil, errors.New("detect boom")
	}
	if err := Run(Options{Paths: []string{"."}}); err == nil || !strings.Contains(err.Error(), "analysis failed") {
		t.Fatalf("expected analysis failed error, got %v", err)
	}
}

func TestRun_CheckBranches(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	loadPackagesFn = func([]string, string) ([]*packages.Package, error) {
		return []*packages.Package{{}}, nil
	}
	detectFn = func([]*packages.Package, *filter.Filter, bool) ([]analysis.InjectionPoint, error) {
		return nil, nil
	}
	if err := Run(Options{Paths: []string{"."}, Check: true}); err != nil {
		t.Fatalf("expected check pass, got %v", err)
	}

	detectFn = func([]*packages.Package, *filter.Filter, bool) ([]analysis.InjectionPoint, error) {
		return []analysis.InjectionPoint{{}}, nil
	}
	if err := Run(Options{Paths: []string{"."}, Check: true}); err == nil || !strings.Contains(err.Error(), "check failed") {
		t.Fatalf("expected check failed error, got %v", err)
	}
}

func TestRun_NoPackagesAndNoChanges(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	loadPackagesFn = func([]string, string) ([]*packages.Package, error) {
		return []*packages.Package{}, nil
	}
	if err := Run(Options{Paths: []string{"."}}); err != nil {
		t.Fatalf("expected no error for empty package list, got %v", err)
	}

	loadPackagesFn = func([]string, string) ([]*packages.Package, error) {
		return []*packages.Package{{}}, nil
	}
	detectFn = func([]*packages.Package, *filter.Filter, bool) ([]analysis.InjectionPoint, error) {
		return []analysis.InjectionPoint{{}}, nil
	}
	applyRefactorsFn = func(*dstManager, []analysis.InjectionPoint, Options, *analysis.InterfaceRegistry) (int, error) {
		return 0, nil
	}
	if err := Run(Options{Paths: []string{"."}}); err != nil {
		t.Fatalf("expected no changes applied, got %v", err)
	}
}

func TestRun_ApplyRefactorsAndIOErrors(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	loadPackagesFn = func([]string, string) ([]*packages.Package, error) {
		return []*packages.Package{{}}, nil
	}
	detectFn = func([]*packages.Package, *filter.Filter, bool) ([]analysis.InjectionPoint, error) {
		return []analysis.InjectionPoint{{}}, nil
	}

	applyRefactorsFn = func(*dstManager, []analysis.InjectionPoint, Options, *analysis.InterfaceRegistry) (int, error) {
		return 0, errors.New("apply boom")
	}
	if err := Run(Options{Paths: []string{"."}}); err == nil {
		t.Fatal("expected applyRefactors error")
	}

	newDstManagerFn = func([]*packages.Package) *dstManager {
		return &dstManager{
			cache:    map[string]*dst.File{},
			modified: map[string]bool{},
			fset:     token.NewFileSet(),
		}
	}
	applyRefactorsFn = func(mgr *dstManager, _ []analysis.InjectionPoint, _ Options, _ *analysis.InterfaceRegistry) (int, error) {
		mgr.modified["missing.go"] = true
		return 1, nil
	}

	readFileFn = func(string) ([]byte, error) {
		return nil, errors.New("read boom")
	}
	if err := Run(Options{Paths: []string{"."}, DryRun: true}); err == nil {
		t.Fatal("expected PrintDiffs error")
	}

	createFileFn = func(string) (*os.File, error) {
		return nil, errors.New("create boom")
	}
	if err := Run(Options{Paths: []string{"."}}); err == nil {
		t.Fatal("expected Save error")
	}
}

func TestRun_PanicDstError(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	loadPackagesFn = func([]string, string) ([]*packages.Package, error) {
		fset, astFile := parseTestFile(t)
		pkg := &packages.Package{
			ID:     "p",
			Fset:   fset,
			Syntax: []*ast.File{astFile},
		}
		return []*packages.Package{pkg}, nil
	}
	detectFn = func([]*packages.Package, *filter.Filter, bool) ([]analysis.InjectionPoint, error) {
		return nil, nil // No points, but PanicToReturn is true, so should proceed
	}
	// Make Get fail by failing implementation hook
	decorateFileFn = func(*token.FileSet, *ast.File) (*dst.File, error) {
		return nil, errors.New("dst boom")
	}

	// This should run without error, just logging the warning internally (not captured unless we pipe log)
	if err := Run(Options{Paths: []string{"."}, PanicToReturn: true}); err != nil {
		t.Fatalf("expected Run to succeed despite dst error in panic loop, got %v", err)
	}
}
