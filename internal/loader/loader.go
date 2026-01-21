// Package loader provides functionality for loading Go packages for AST analysis.
package loader

import (
	"golang.org/x/tools/go/packages"
)

// LoadPackages loads Go packages from the specified directory using the go/packages library.
// It configures the load with modes for syntax, types, types info, and module information,
// and loads all packages in the directory tree via "./...".
// Returns the loaded packages or an error if loading fails.
//
// dir: the directory from which to load packages.
func LoadPackages(dir string) ([]*packages.Package, error) {
	config := &packages.Config{
		Mode: packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedModule,
		Dir:  dir,
	}
	return packages.Load(config, "./...")
}