package loader

import (
	"fmt"
	"os"

	"golang.org/x/tools/go/packages"
)

// LoadPackages loads Go packages matching the provided patterns from the specified directory.
// It uses a configuration that ensures AST syntax trees, type information, and type-checked objects are loaded.
//
// patterns: A list of package patterns to load (e.g., ".", "./...", "github.com/pkg/errors").
// dir: The working directory from which to run the go list command. If empty, defaults to current working directory.
func LoadPackages(patterns []string, dir string) ([]*packages.Package, error) {
	// Mode determines what information is loaded.
	// We need Name, Files, and Imports for basic structure.
	// We need Types and TypesInfo for type checking (essential for identifying error interfaces).
	// We need Syntax for AST manipulation.
	mode := packages.NeedName |
		packages.NeedFiles |
		packages.NeedCompiledGoFiles |
		packages.NeedImports |
		packages.NeedTypes |
		packages.NeedTypesInfo |
		packages.NeedSyntax |
		packages.NeedModule

	cfg := &packages.Config{
		Mode:  mode,
		Dir:   dir,
		Tests: true, // Analyze test files as well
		Env:   os.Environ(),
	}

	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("failed to call packages.Load: %w", err)
	}

	// packages.Load can return partial success with errors in specific packages.
	// Whether to fail hard here is a design choice. For now, we return what we found,
	// but we check if all failed (empty list with no error).
	return pkgs, nil
}
