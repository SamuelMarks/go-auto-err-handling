package loader

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// LoadPackages loads Go packages matching the provided patterns from the specified directory.
// It uses a configuration that ensures AST syntax trees, type information, and type-checked objects are loaded.
//
// It implements "Smart Module Recursion": if the initial scan fails because the root directory
// contains no Go files (common in repo roots with only sub-packages), but a go.mod file exists,
// it automatically retries with a recursive pattern ("./...").
//
// patterns: A list of package patterns to load (e.g., ".", "./...", "github.com/pkg/errors").
// dir: The working directory from which to run the go list command. If empty, defaults to current working directory.
//
// Returns a slice of loaded packages or an error if the loader tool itself fails.
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

	// Check if we need to apply Smart Module Recursion.
	// This happens if the user likely targeted a repo root (e.g., ".") that has no Go files itself,
	// but contains a module definition and sub-packages.
	if shouldRetryRecursive(pkgs, patterns, dir) {
		log.Println("[INFO] Module root detected with no source files. Switching to recursive mode ('./...').")

		// Adjust patterns to be recursive.
		// We specifically target "." -> "./..." transformation.
		recursivePatterns := make([]string, len(patterns))
		for i, p := range patterns {
			if p == "." {
				recursivePatterns[i] = "./..."
			} else {
				// For other explicit paths, blindly appending /... might be risky,
				// but if the user pointed to a dir that failed, they likely wanted that.
				// For safety, we only automatically mutate ".".
				recursivePatterns[i] = p
			}
		}

		pkgs, err = packages.Load(cfg, recursivePatterns...)
		if err != nil {
			return nil, fmt.Errorf("failed to retry packages.Load: %w", err)
		}
	}

	// packages.Load is permissive; it often returns a package struct even if type checking fails.
	// We iterate checking for errors to warn the user, as incomplete types cause silent analysis failures.
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			for _, e := range pkg.Errors {
				// We log as warning. The analysis might still be able to proceed partially,
				// or the user might be running on a dirty tree.
				log.Printf("[WARN] Package %q error: %v", pkg.PkgPath, e)
			}
		}
	}

	return pkgs, nil
}

// shouldRetryRecursive determines if the loader should attempt to reload with recursion.
// It returns true if:
// 1. One of the loaded packages reports "no Go files".
// 2. The patterns include ".".
// 3. A "go.mod" file exists in the target directory.
//
// It avoids recursion if any valid package (with Go files) was successfully loaded,
// to support test-only directories where the primary package is empty but the test variant is valid.
//
// pkgs: The packages returned from the initial load.
// patterns: The patterns used for the initial load.
// dir: The directory context.
func shouldRetryRecursive(pkgs []*packages.Package, patterns []string, dir string) bool {
	hasDotPattern := false
	for _, p := range patterns {
		if p == "." {
			hasDotPattern = true
			break
		}
	}
	if !hasDotPattern {
		return false
	}

	// Check if we successfully loaded ANY package with files.
	// If we did (e.g. the test variant of ".", or another package in the list),
	// then strict recursion might not be needed or might be redundant.
	hasValidPackage := false
	hasNoFilesError := false

	for _, pkg := range pkgs {
		if len(pkg.GoFiles) > 0 {
			hasValidPackage = true
		}

		// Check for the specific failure mode "no Go files in <path>"
		if len(pkg.GoFiles) == 0 {
			for _, e := range pkg.Errors {
				if strings.Contains(e.Msg, "no Go files") {
					hasNoFilesError = true
				}
			}
		}
	}

	// If we found at least one valid package, we assume looking at "." gave us what we wanted
	// (e.g. the test package in a test-only directory).
	if hasValidPackage {
		return false
	}

	if !hasNoFilesError {
		return false
	}

	// Check for go.mod
	target := dir
	if target == "" {
		target = "."
	}
	goModPath := filepath.Join(target, "go.mod")
	if _, err := os.Stat(goModPath); err == nil {
		return true
	}

	return false
}
