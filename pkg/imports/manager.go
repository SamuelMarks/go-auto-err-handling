package imports

import (
	"go/ast"
	"go/token"

	"golang.org/x/tools/go/ast/astutil"
)

// Add ensures that the specified package path is imported in the AST file.
// It delegates to astutil.AddImport which handles creating the import declaration
// block if missing and deduplicating existing imports.
//
// fset: The file set containing the file source positions.
// file: The AST file to modify.
// pkgPath: The import path to add (e.g., "fmt" or "github.com/pkg/errors").
//
// Returns true if the import was added, false if it was already present.
func Add(fset *token.FileSet, file *ast.File, pkgPath string) bool {
	return astutil.AddImport(fset, file, pkgPath)
}

// AddNamed ensures that the specified package path is imported with the given alias name.
// This is useful for preventing shadowing issues or handling naming conflicts.
// It delegates to astutil.AddNamedImport.
//
// fset: The file set containing the file source positions.
// file: The AST file to modify.
// name: The local package name (alias) to use. If empty, it works like Add.
// pkgPath: The import path to add.
//
// Returns true if the import was added, false if it was already present with the same alias.
func AddNamed(fset *token.FileSet, file *ast.File, name, pkgPath string) bool {
	return astutil.AddNamedImport(fset, file, name, pkgPath)
}
