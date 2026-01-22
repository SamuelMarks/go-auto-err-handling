package imports

import (
	"go/ast"
	"go/token"

	"golang.org/x/tools/go/ast/astutil"
)

// Add adds the import path to the file f, if absent.
//
// fset: The file set containing the file.
// file: The AST file to modify.
// pkgPath: The import path to add (e.g., "errors").
//
// Returns true if the import was added, false if it already existed.
func Add(fset *token.FileSet, file *ast.File, pkgPath string) bool {
	return astutil.AddImport(fset, file, pkgPath)
}

// AddNamed adds the import with the given name and path to the file f, if absent.
//
// fset: The file set containing the file.
// file: The AST file to modify.
// name: The local name for the import (e.g., "jsonpkg"). If empty, behaves like Add.
// pkgPath: The import path to add.
//
// Returns true if the import was added, false if it already existed.
func AddNamed(fset *token.FileSet, file *ast.File, name, pkgPath string) bool {
	return astutil.AddNamedImport(fset, file, name, pkgPath)
}
