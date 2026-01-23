package analysis

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"golang.org/x/tools/go/packages"
)

// setupComplianceEnv creates a mocked package environment with types loaded.
// It parses source code and runs the type checker to produce valid *types.Func objects.
//
// t: Testing context.
// src: The source code to parse.
//
// Returns the types.Package, the list of *packages.Package wrapper, and the FileSet.
func setupComplianceEnv(t *testing.T, src string) (*types.Package, []*packages.Package, *token.FileSet) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parser failed: %v", err)
	}

	// Type check
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
	}

	var conf types.Config
	// We do not set an Importer here, so standard library imports won't resolve in this specific
	// mock unless we mock the importer. For unit testing local interface compliance,
	// distinct packages are sufficient.

	pkg, err := conf.Check("main", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("type check failed: %v", err)
	}

	// Wrap in golang.org/x/tools/go/packages struct to satisfy NewInterfaceRegistry signature
	p := &packages.Package{
		PkgPath:   "main",
		Types:     pkg,
		TypesInfo: info,
		Imports:   nil, // Dependencies would be mocked here if needed
	}

	return pkg, []*packages.Package{p}, fset
}

// TestCheckCompliance_LocalInterface verifies detection of conflicts
// where a struct implements an interface defined in the same package.
func TestCheckCompliance_LocalInterface(t *testing.T) {
	src := `package main

// Target Interface
type Runner interface {
	Run()
}

// Struct implementing Runner
type Service struct {}

// Method satisfying Runner.Run
func (s *Service) Run() {}

// Method NOT in Runner
func (s *Service) Stop() {}

// Unrelated struct
type Other struct {}
func (o *Other) Run() {}
`
	tpkg, pkgs, _ := setupComplianceEnv(t, src)
	registry := NewInterfaceRegistry(pkgs)

	// Helper to lookup method object
	findMethod := func(typeName, methodName string) *types.Func {
		obj := tpkg.Scope().Lookup(typeName)
		if obj == nil {
			t.Fatalf("type %s not found", typeName)
		}
		named := obj.(*types.TypeName)
		// Look up method on the *Pointer* receiver if defined that way, or Named type
		// Our test source uses *Service receiver.
		// types.NewMethodSet will find it.
		// Detailed lookup:
		mObj, _, _ := types.LookupFieldOrMethod(named.Type(), true, pkgWithinType(named.Pkg()), methodName)
		if mObj == nil {
			t.Fatalf("method %s.%s not found", typeName, methodName)
		}
		return mObj.(*types.Func)
	}

	// Case 1: Service.Run (conflicts with Runner)
	t.Run("ConflictDetected", func(t *testing.T) {
		method := findMethod("Service", "Run")
		conflicts, err := registry.CheckCompliance(method)
		if err != nil {
			t.Fatalf("CheckCompliance error: %v", err)
		}
		if len(conflicts) == 0 {
			t.Error("Expected conflict with Runner interface, got none")
		} else {
			c := conflicts[0]
			if c.Interface.Name() != "Runner" {
				t.Errorf("Expected conflict with Runner, got %s", c.Interface.Name())
			}
			t.Logf("Conflict found: %s", c.Error())
		}
	})

	// Case 2: Service.Stop (no conflict, interface doesn't have Stop)
	t.Run("NoConflict_ExtraMethod", func(t *testing.T) {
		method := findMethod("Service", "Stop")
		conflicts, err := registry.CheckCompliance(method)
		if err != nil {
			t.Fatalf("CheckCompliance error: %v", err)
		}
		if len(conflicts) > 0 {
			t.Errorf("Expected no conflicts for Stop(), got %d", len(conflicts))
		}
	})

	// Case 3: Other.Run (no conflict, Other doesn't implement Runner technically?
	// Wait, Other struct{} has Run(). Runner has Run().
	// Other implicitly implements Runner. So refactoring Other.Run SHOULD conflict.)
	t.Run("Conflict_ImplicitImplementation", func(t *testing.T) {
		method := findMethod("Other", "Run")
		conflicts, err := registry.CheckCompliance(method)
		if err != nil {
			t.Fatal(err)
		}
		if len(conflicts) == 0 {
			t.Error("Expected conflict for Other.Run (implicit satisfaction), got none")
		}
	})
}

// pkgWithinType helper provides a context package for lookup.
func pkgWithinType(p *types.Package) *types.Package {
	return p
}

// TestCheckCompliance_NotAMethod verifies behavior on plain functions.
func TestCheckCompliance_NotAMethod(t *testing.T) {
	src := `package main
func Standalone() {}
`
	tpkg, pkgs, _ := setupComplianceEnv(t, src)
	registry := NewInterfaceRegistry(pkgs)

	obj := tpkg.Scope().Lookup("Standalone")
	if obj == nil {
		t.Fatal("Standalone func not found")
	}
	fn := obj.(*types.Func)

	conflicts, err := registry.CheckCompliance(fn)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) > 0 {
		t.Error("Expected no conflicts for standalone function")
	}
}

// TestCheckCompliance_EmptyInterface verifies that emptiness doesn't cause blocks.
// Changing a method doesn't break `interface{}` satisfaction.
func TestCheckCompliance_EmptyInterface(t *testing.T) {
	// Note: NewInterfaceRegistry logic filters for *Named* interfaces in source.
	// `type Any interface{}` is a named interface.
	src := `package main
type Any interface {}
type S struct {}
func (s *S) Do() {}
`
	tpkg, pkgs, _ := setupComplianceEnv(t, src)
	registry := NewInterfaceRegistry(pkgs)

	// Lookup S.Do
	obj := tpkg.Scope().Lookup("S")
	named := obj.Type().(*types.Named)
	mObj, _, _ := types.LookupFieldOrMethod(named, true, tpkg, "Do")
	method := mObj.(*types.Func)

	conflicts, err := registry.CheckCompliance(method)
	if err != nil {
		t.Fatal(err)
	}
	// "Any" has 0 methods. S implements Any. S.Do is not in Any.
	// Therefore changing S.Do does not break Any.
	if len(conflicts) > 0 {
		t.Errorf("Expected 0 conflicts for empty interface, got %d", len(conflicts))
	}
}
