// Package checker provides tests for the errcheck configuration and execution functions.
package checker

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/kisielk/errcheck/errcheck"
)

// TestParseExcludeSymbolGlob tests the ParseExcludeSymbolGlob function with various inputs.
// It covers valid globs, whole package ignores, invalid cases, and regex compilation errors.
//
// t: the testing context.
func TestParseExcludeSymbolGlob(t *testing.T) {
	tests := []struct {
		// name identifies the test case.
		name string
		// glob is the input string.
		glob string
		// wantPkg is the expected package name.
		wantPkg string
		// wantRe is the expected compiled regex (nil if logic implies full package ignore).
		wantRe *regexp.Regexp
		// wantErr indicates if an error is expected.
		wantErr bool
	}{
		{
			name:    "empty glob",
			glob:    "",
			wantErr: true,
		},
		{
			name:    "no package (starts with dot)",
			glob:    ".Main",
			wantErr: true,
		},
		{
			name:    "whole package",
			glob:    "fmt",
			wantPkg: "fmt",
			wantRe:  nil,
		},
		{
			name:    "trailing dot whole package",
			glob:    "fmt.",
			wantPkg: "fmt",
			wantRe:  nil,
		},
		{
			name:    "function wildcard",
			glob:    "fmt.*",
			wantPkg: "fmt",
			wantRe:  regexp.MustCompile("^.*$"),
		},
		{
			name:    "specific function prefix",
			glob:    "fmt.Print*",
			wantPkg: "fmt",
			wantRe:  regexp.MustCompile("^Print.*$"),
		},
		{
			name:    "invalid regex",
			glob:    "fmt.[",
			wantErr: true,
		},
		{
			name:    "complex glob",
			glob:    "os.Open*",
			wantPkg: "os",
			wantRe:  regexp.MustCompile("^Open.*$"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPkg, gotRe, err := ParseExcludeSymbolGlob(tt.glob)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseExcludeSymbolGlob() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}
			if gotPkg != tt.wantPkg {
				t.Errorf("ParseExcludeSymbolGlob() pkg = %v, want %v", gotPkg, tt.wantPkg)
			}
			if tt.wantRe == nil {
				if gotRe != nil {
					t.Errorf("ParseExcludeSymbolGlob() re = %v, want nil", gotRe)
				}
			} else {
				if gotRe == nil || gotRe.String() != tt.wantRe.String() {
					t.Errorf("ParseExcludeSymbolGlob() re = %v, want %v", gotRe, tt.wantRe)
				}
			}
		})
	}
}

// TestSetupChecker tests the SetupChecker function.
// It verifies that the Ignore map is correctly populated based on globs, including skips for invalid.
//
// t: the testing context.
func TestSetupChecker(t *testing.T) {
	tests := []struct {
		// name identifies the test case.
		name string
		// excludeSymbolGlobs is the input list of globs.
		excludeSymbolGlobs []string
		// wantIgnore is the expected map of ignored packages and regexes.
		wantIgnore map[string]*regexp.Regexp
	}{
		{
			name:               "empty",
			excludeSymbolGlobs: []string{},
			wantIgnore:         map[string]*regexp.Regexp{},
		},
		{
			name:               "whole package",
			excludeSymbolGlobs: []string{"fmt"},
			wantIgnore: map[string]*regexp.Regexp{
				"fmt": nil,
			},
		},
		{
			name:               "function regex",
			excludeSymbolGlobs: []string{"os.Open*"},
			wantIgnore: map[string]*regexp.Regexp{
				"os": regexp.MustCompile("^Open.*$"),
			},
		},
		{
			name:               "invalid skipped",
			excludeSymbolGlobs: []string{"invalid[", "fmt.*"},
			wantIgnore: map[string]*regexp.Regexp{
				"fmt": regexp.MustCompile("^.*$"),
			},
		},
		{
			name:               "overwrite same package",
			excludeSymbolGlobs: []string{"fmt.Print*", "fmt.Scan*"},
			wantIgnore: map[string]*regexp.Regexp{
				"fmt": regexp.MustCompile("^Scan.*$"), // Last wins
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SetupChecker(tt.excludeSymbolGlobs)
			if got.Blank != true {
				t.Errorf("SetupChecker() Blank = %v, want true", got.Blank)
			}
			if len(got.Ignore) != len(tt.wantIgnore) {
				t.Errorf("SetupChecker() Ignore len = %d, want %d", len(got.Ignore), len(tt.wantIgnore))
			}
			for pkg, wantRe := range tt.wantIgnore {
				gotRe, ok := got.Ignore[pkg]
				if !ok {
					t.Errorf("SetupChecker() missing pkg %s", pkg)
					continue
				}
				if wantRe == nil {
					if gotRe != nil {
						t.Errorf("SetupChecker() re for %s = %v, want nil", pkg, gotRe)
					}
				} else {
					if gotRe == nil || gotRe.String() != wantRe.String() {
						t.Errorf("SetupChecker() re for %s = %v, want %v", pkg, gotRe, wantRe)
					}
				}
			}
		})
	}
}

// TestGetUncheckedErrors tests the GetUncheckedErrors function.
// It sets up temporary Go projects with unchecked errors and verifies the returned UncheckedErrors.
// Also tests error cases like invalid directory.
//
// t: the testing context.
func TestGetUncheckedErrors(t *testing.T) {
	tests := []struct {
		// name identifies the test case.
		name string
		// setup prepares the test directory.
		setup func(dir string) error
		// dir overrides the directory passed to GetUncheckedErrors.
		dir string
		// checker is the configured checker to use.
		checker *errcheck.Checker
		// wantCount is the expected number of unchecked errors.
		wantCount int
		// wantErr indicates if an error is expected.
		wantErr bool
	}{
		{
			name: "simple unchecked error",
			setup: func(dir string) error {
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main
import "os"
func main() {
  os.Open("nonexistent")
}
`), 0644); err != nil {
					return err
				}
				return nil
			},
			checker:   SetupChecker([]string{}),
			wantCount: 1,
		},
		{
			name: "excluded unchecked",
			setup: func(dir string) error {
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main
import "os"
func main() {
  os.Open("nonexistent")
}
`), 0644); err != nil {
					return err
				}
				return nil
			},
			// Check against Open* to ensure regex matching works
			checker:   SetupChecker([]string{"os.Open*"}),
			wantCount: 0,
		},
		{
			name:    "invalid dir",
			dir:     "/nonexistent",
			checker: SetupChecker([]string{}),
			wantErr: true,
		},
		{
			name: "no unchecked",
			setup: func(dir string) error {
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main
func main() {}
`), 0644); err != nil {
					return err
				}
				return nil
			},
			checker:   SetupChecker([]string{}),
			wantCount: 0,
		},
		{
			name: "check packages error",
			setup: func(dir string) error {
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
					return err
				}
				// Syntax error causes CheckPackages to fail
				if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(`invalid syntax`), 0644); err != nil {
					return err
				}
				return nil
			},
			checker: SetupChecker([]string{}),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir, err := os.MkdirTemp("", "checker-test-*")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(tempDir)

			dir := tempDir
			if tt.dir != "" {
				dir = tt.dir
			} else if tt.setup != nil {
				if err := tt.setup(tempDir); err != nil {
					t.Fatal(err)
				}
			}

			got, err := GetUncheckedErrors(dir, tt.checker)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetUncheckedErrors() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if len(got) != tt.wantCount {
				t.Errorf("GetUncheckedErrors() len = %d, want %d", len(got), tt.wantCount)
			}
			if tt.wantCount > 0 {
				// Verify position detection (using Pos field in newer errcheck)
				if got[0].Pos.Line != 4 {
					t.Errorf("GetUncheckedErrors() line = %d, want 4", got[0].Pos.Line)
				}
			}
		})
	}
}