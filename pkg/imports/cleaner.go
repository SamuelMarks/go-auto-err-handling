package imports

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// ConflictResolver identifies naming collisions between intended package imports
// and existing identifiers in a file scope. It determines safe aliases for imports
// to ensure injected code does not break local variable references.
type ConflictResolver struct {
	pkg  *packages.Package
	file *ast.File
}

// NewConflictResolver creates a resolver for the given package and file.
//
// pkg: The loaded package containing type information.
// file: The AST file to analyze.
func NewConflictResolver(pkg *packages.Package, file *ast.File) *ConflictResolver {
	return &ConflictResolver{
		pkg:  pkg,
		file: file,
	}
}

// ResolveAlias checks if a requested package name (e.g. "errors") is shadowed
// by any local definition in the scope of the file. If shadowed, it generates
// a unique alias (e.g. "errors1") and ensures the import is added with that alias.
//
// requestedPkg: The default package name (e.g. "fmt", "errors").
// importPath: The full import path (e.g. "fmt", "github.com/pkg/errors").
//
// Returns the name to use in the code. If no conflict, returns requestedPkg.
// If conflict, returns the alias.
func (c *ConflictResolver) ResolveAlias(requestedPkg, importPath string) (string, bool) {
	// 1. Check if the name is already used in the file-level scope or any function scope.
	// We scan the Entire File for definitions of `identifier == requestedPkg`.

	conflictFound := false

	// Scan AST for any Ident matching the package name that is NOT the package usage itself
	ast.Inspect(c.file, func(n ast.Node) bool {
		if conflictFound {
			return false
		}

		if ident, ok := n.(*ast.Ident); ok {
			if ident.Name == requestedPkg {
				// We found an identifier with the same name.
				// We need to check if it refers to the imported package Object or something else.
				if c.pkg.TypesInfo != nil {
					obj := c.pkg.TypesInfo.ObjectOf(ident)
					if obj != nil {
						// If it's a PkgName object, it means it IS the import. Not a conflict, reuse it.
						if _, isPkg := obj.(*types.PkgName); isPkg {
							return true
						}
						// If it's Var, Const, Func, etc., it's a conflict.
						conflictFound = true
						return false
					}
					// If obj is nil (declarations in some AST passes), check definitions
					if _, ok := c.pkg.TypesInfo.Defs[ident]; ok {
						conflictFound = true
						return false
					}
				} else {
					// Fallback if TypesInfo missing: Assume any usage is a potential conflict
					// unless it appears in an ImportSpec.
					conflictFound = true
				}
			}
		}

		return true
	})

	if !conflictFound {
		// Ensure import exists (helper logic typically handles this, but we are just resolving name)
		return requestedPkg, false
	}

	// 2. Generate Safe Alias
	alias := generateSafeAlias(c.file, requestedPkg)

	// 3. Inject Aliased Import
	// We manually modify AST to add the import with alias to ensure availability
	addAliasedImport(c.pkg.Fset, c.file, alias, importPath)

	return alias, true
}

// generateSafeAlias finds a name like "fmt1", "fmt2" that is not used in the file.
func generateSafeAlias(file *ast.File, base string) string {
	count := 1
	for {
		candidate := fmt.Sprintf("std_%s_%d", base, count) // Use usage-safe prefix
		if !identifierExists(file, candidate) {
			return candidate
		}
		count++
	}
}

// identifierExists checks if an identifier is present anywhere in the AST.
func identifierExists(file *ast.File, name string) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		if id, ok := n.(*ast.Ident); ok {
			if id.Name == name {
				found = true
			}
		}
		return true
	})
	return found
}

// addAliasedImport adds the import spec to the file's import declarations.
func addAliasedImport(fset *token.FileSet, file *ast.File, name, path string) {
	// Check if already imported
	if astutil.UsesImport(file, path) {
		// It's imported, but maybe not with our alias.
		// astutil.StartImport/AddNamedImport handles deduplication well usually,
		// but explicit aliasing might need force.
	}

	astutil.AddNamedImport(fset, file, name, path)
}
