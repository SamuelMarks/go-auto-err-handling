// Package checker provides functionality for configuring and using errcheck to find unchecked errors in Go code.
package checker

import (
	"errors"
	"os"
	"regexp"
	"strings"

	"github.com/kisielk/errcheck/errcheck"
)

// ParseExcludeSymbolGlob parses a symbol glob into a package name and a compiled regex for matching functions.
// It splits the glob by the first '.', treating the left side as the package name and the right side as a
// glob pattern for function names. It replaces '*' with '.*' to create a regex.
//
// If the glob has no package part (no '.'), it returns the package name and a nil regex (ignore entire package).
// If the function part is empty (trailing '.'), it also returns a nil regex.
// Returns an error if the glob is empty, has no package part, or if regex compilation fails.
//
// glob: the symbol glob string to parse (e.g., "fmt.Printf", "fmt.*", "os.Open").
func ParseExcludeSymbolGlob(glob string) (pkg string, re *regexp.Regexp, err error) {
	if glob == "" {
		return "", nil, errors.New("empty glob")
	}
	parts := strings.SplitN(glob, ".", 2)
	pkg = parts[0]
	if pkg == "" {
		return "", nil, errors.New("no package in glob")
	}
	if len(parts) == 1 {
		return pkg, nil, nil // Ignore whole package
	}
	funcGlob := parts[1]
	if funcGlob == "" {
		return pkg, nil, nil // Ignore whole package if '.' at end
	}
	regexStr := strings.Replace(funcGlob, "*", ".*", -1)
	re, err = regexp.Compile("^" + regexStr + "$") // Anchor for exact match
	if err != nil {
		return "", nil, err
	}
	return pkg, re, nil
}

// SetupChecker creates and configures an errcheck.Checker based on the provided exclude symbol globs.
// It sets the Blank flag to true and populates the Ignore map based on the parsed globs.
// Invalid globs are skipped. If multiple globs target the same package, the last one processed wins (overwrites).
//
// excludeSymbolGlobs: list of symbol globs to exclude from error checking.
func SetupChecker(excludeSymbolGlobs []string) *errcheck.Checker {
	c := new(errcheck.Checker)
	c.Blank = true
	c.Ignore = make(map[string]*regexp.Regexp)
	for _, glob := range excludeSymbolGlobs {
		pkg, re, err := ParseExcludeSymbolGlob(glob)
		if err != nil {
			continue // Skip invalid
		}
		c.Ignore[pkg] = re
	}
	return c
}

// GetUncheckedErrors runs the errcheck.Checker on the packages in the given directory.
// It temporarily changes the working directory to dir to ensure relative path resolution works for "./...",
// calls CheckPackages, and restores the original working directory upon completion.
//
// dir: the directory to change to and check packages in.
// c: the configured errcheck.Checker to use.
func GetUncheckedErrors(dir string, c *errcheck.Checker) ([]errcheck.UncheckedError, error) {
	origWd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if err := os.Chdir(dir); err != nil {
		return nil, err
	}
	defer func() {
		_ = os.Chdir(origWd) // Ignore error, best effort to restore
	}()
	unchecked, err := c.CheckPackages("./...")
	if err != nil {
		return nil, err
	}
	return unchecked, nil
}