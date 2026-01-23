package analysis

import (
	"fmt"
	"go/types"

	"golang.org/x/tools/go/packages"
)

// InterfaceConflict represents a detected conflict where refactoring a function
// would break the implementation of an interface.
type InterfaceConflict struct {
	// Method is the function being refactored.
	Method *types.Func
	// Interface is the named interface that would be broken.
	Interface *types.TypeName
	// InterfaceMethod is the specific method definition within the interface that conflicts.
	InterfaceMethod *types.Func
}

// Error formats the conflict into a readable string.
func (c InterfaceConflict) Error() string {
	return fmt.Sprintf("method %s.%s implements interface %s.%s; changing definition would break contract",
		c.Method.Type().(*types.Signature).Recv().Type().String(),
		c.Method.Name(),
		c.Interface.Pkg().Path(),
		c.Interface.Name(),
	)
}

// InterfaceRegistry maintains a cache of all visible interface definitions
// across the loaded packages and their transitive dependencies.
//
// It is used to perform quick lookups when determining if a method refactor
// is safe to perform.
type InterfaceRegistry struct {
	// interfaces stores a list of all named interface types encountered during the scan.
	interfaces []*types.TypeName
	// seen avoids processing the same package identifier multiple times in the dependency graph.
	seen map[string]bool
}

// NewInterfaceRegistry initializes a registry and populates it by scanning
// the provided packages and their dependencies for named interface definitions.
//
// pkgs: The entry point packages loaded by the tool.
//
// Returns a fully populated InterfaceRegistry.
func NewInterfaceRegistry(pkgs []*packages.Package) *InterfaceRegistry {
	reg := &InterfaceRegistry{
		interfaces: []*types.TypeName{},
		seen:       make(map[string]bool),
	}
	for _, pkg := range pkgs {
		reg.scanPackage(pkg)
	}
	return reg
}

// scanPackage parses the package scopes for Named types that are interfaces.
// It recursively scans imported packages (`pkg.Imports`) to ensure cross-package
// interfaces (like `io.Writer`) are detected.
//
// pkg: The package to scan.
func (r *InterfaceRegistry) scanPackage(pkg *packages.Package) {
	if pkg == nil || pkg.Types == nil || r.seen[pkg.PkgPath] {
		return
	}
	r.seen[pkg.PkgPath] = true

	// Iterate over package scope definitions
	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if typeName, ok := obj.(*types.TypeName); ok {
			// We only care about named interfaces.
			if _, ok := typeName.Type().Underlying().(*types.Interface); ok {
				r.interfaces = append(r.interfaces, typeName)
			}
		}
	}

	// Recurse dependencies
	// Go/packages populates Imports map with fully typed package objects
	for _, imported := range pkg.Imports {
		r.scanPackage(imported)
	}
}

// CheckCompliance verifies if refactoring the given method would violate any
// interface implementations found in the registry.
//
// A conflict exists if:
// 1. The method's receiver type currently implements an interface `I`.
// 2. `I` explicitly defines a method with the same name as `method`.
//
// method: The function object being targeted for refactoring (must be a method).
//
// Returns a slice of InterfaceConflict if issues are found, or nil if safe.
func (r *InterfaceRegistry) CheckCompliance(method *types.Func) ([]InterfaceConflict, error) {
	sig, ok := method.Type().(*types.Signature)
	if !ok {
		return nil, fmt.Errorf("object is not a function signature")
	}

	recv := sig.Recv()
	if recv == nil {
		// Not a method (just a standalone function), so it cannot implement an interface directly.
		return nil, nil
	}

	recvType := recv.Type()
	// types.Implements expects the exact type.
	// If the method is on *S, we check implicit compliance of *S.
	// Note: We do not need to check S separately because if *S satisfies it,
	// and we break *S, we break the contract.

	var conflicts []InterfaceConflict

	for _, ifaceName := range r.interfaces {
		iface, ok := ifaceName.Type().Underlying().(*types.Interface)
		if !ok {
			continue
		}

		// 1. Check if the type currently implements the interface
		if types.Implements(recvType, iface) {
			// 2. Check if the interface actually *uses* this method
			// (A type might implement an interface, but the method we are changing
			// is extraneous to that specific interface).
			if matches, ifaceMethod := interfaceHasMethod(iface, method.Name()); matches {
				conflicts = append(conflicts, InterfaceConflict{
					Method:          method,
					Interface:       ifaceName,
					InterfaceMethod: ifaceMethod,
				})
			}
		}
	}

	return conflicts, nil
}

// interfaceHasMethod checks if the interface definition includes a method with the given name.
//
// iface: The interface to inspect.
// name: The method name to look for.
//
// Returns true and the interface method object if found.
func interfaceHasMethod(iface *types.Interface, name string) (bool, *types.Func) {
	// Scan explicit methods
	for i := 0; i < iface.NumMethods(); i++ {
		m := iface.Method(i)
		if m.Name() == name {
			return true, m
		}
	}
	// Embeds are flattened in .NumMethods / .Method in go/types usually,
	// checking NumMethods should suffice for complete method set.
	return false, nil
}
