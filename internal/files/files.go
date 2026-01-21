// Package files provides utilities for filesystem traversal and file collection.
package files

import (
	"os"
	"path/filepath"
	"strings"
)

// CollectGoFiles collects all non-test Go source files in the directory tree rooted at dir.
// It traverses the directory using filepath.WalkDir, skipping directories and files that match
// any of the excludeGlobs patterns via filepath.Match on the relative path.
// Only files ending with ".go" but not "_test.go" are collected.
//
// dir: root directory to traverse.
// excludeGlobs: list of glob patterns to match against relative paths for exclusion.
func CollectGoFiles(dir string, excludeGlobs []string) ([]string, error) {
	var files []string
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	err = filepath.WalkDir(absDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(absDir, path)
		if err != nil {
			return err
		}
		
		// Skip root '.' check if needed, but filepath.Rel(".") is "."
		if rel == "." {
			return nil
		}

		matched := false
		for _, glob := range excludeGlobs {
			if match, _ := filepath.Match(glob, rel); match {
				matched = true
				break
			}
		}
		if matched {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}