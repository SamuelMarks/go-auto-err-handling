package runner

import (
	"errors"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/analysis"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/filter"
	"golang.org/x/tools/go/packages"
	"os"
	"testing"
)

func TestRun_LoadFailed(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	loadPackagesFn = func([]string, string, bool) ([]*packages.Package, error) {
		return nil, errors.New("load error")
	}

	err := Run(Options{})
	if err == nil || err.Error() != "load failed: load error" {
		t.Fatalf("expected load error, got %v", err)
	}
}

func TestRun_NoPackages(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	loadPackagesFn = func([]string, string, bool) ([]*packages.Package, error) {
		return nil, nil
	}

	err := Run(Options{})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestRun_DetectFailed(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	loadPackagesFn = func([]string, string, bool) ([]*packages.Package, error) {
		return []*packages.Package{{}}, nil
	}
	detectFn = func([]*packages.Package, *filter.Filter, bool) ([]analysis.InjectionPoint, error) {
		return nil, errors.New("detect error")
	}

	err := Run(Options{})
	if err == nil || err.Error() != "analysis failed: detect error" {
		t.Fatalf("expected detect error, got %v", err)
	}
}

func TestRun_ApplyRefactorsFailed(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	loadPackagesFn = func([]string, string, bool) ([]*packages.Package, error) {
		return []*packages.Package{{}}, nil
	}
	detectFn = func([]*packages.Package, *filter.Filter, bool) ([]analysis.InjectionPoint, error) {
		return []analysis.InjectionPoint{{}}, nil
	}
	applyRefactorsFn = func(mgr *dstManager, points []analysis.InjectionPoint, opts Options, registry *analysis.InterfaceRegistry) (int, error) {
		return 0, errors.New("apply error")
	}

	err := Run(Options{})
	if err == nil || err.Error() != "apply error" {
		t.Fatalf("expected apply error, got %v", err)
	}
}

func TestRun_SaveFailed(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	loadPackagesFn = func([]string, string, bool) ([]*packages.Package, error) {
		return []*packages.Package{{}}, nil
	}
	detectFn = func([]*packages.Package, *filter.Filter, bool) ([]analysis.InjectionPoint, error) {
		return []analysis.InjectionPoint{{}}, nil
	}
	applyRefactorsFn = func(mgr *dstManager, points []analysis.InjectionPoint, opts Options, registry *analysis.InterfaceRegistry) (int, error) {
		mgr.modified["test.go"] = true
		return 1, nil // return 1 change to bypass count == 0
	}
	createFileFn = func(path string) (*os.File, error) {
		return nil, errors.New("save error")
	}

	err := Run(Options{}) // DryRun is false by default, will call Save()
	if err == nil || err.Error() != "save error" {
		t.Fatalf("expected save error, got %v", err)
	}
}

func TestRun_PrintDiffsFailed(t *testing.T) {
	hooks := saveRunnerHooks()
	defer hooks.restore()

	loadPackagesFn = func([]string, string, bool) ([]*packages.Package, error) {
		return []*packages.Package{{}}, nil
	}
	detectFn = func([]*packages.Package, *filter.Filter, bool) ([]analysis.InjectionPoint, error) {
		return []analysis.InjectionPoint{{}}, nil
	}
	applyRefactorsFn = func(mgr *dstManager, points []analysis.InjectionPoint, opts Options, registry *analysis.InterfaceRegistry) (int, error) {
		mgr.cache["test.go"] = nil
		mgr.modified["test.go"] = true
		return 1, nil
	}

	err := Run(Options{DryRun: true})
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected file not found error, got %v", err)
	}
}
