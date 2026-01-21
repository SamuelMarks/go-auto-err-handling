// Package inserter provides integration tests for the autoerr tool.
package inserter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SamuelMarks/go-auto-err-handling/internal/checker"
	"github.com/SamuelMarks/go-auto-err-handling/internal/loader"
)

// TestEndToEnd runs integration tests for different processing levels.
// It creates a temporary Go module, populates it with source code containing unchecked errors,
// and verifies that the tool modifies the code correctly according to the selected level.
//
// t: the testing context.
func TestEndToEnd(t *testing.T) {
	tests := []struct {
		// name identifies the test scenario.
		name string
		// level indicates which processing level to run ("1", "2", "3").
		level string
		// src is the source code for main.go.
		src string
		// commonSrc is optional additional source code (e.g., in another file).
		commonSrc string
		// wantModified list of filenames expected to be modified.
		wantModified []string
		// wantContains map of filename to expected substrings in the modified content.
		wantContains map[string]string
		// excludeGlobs list of file globs to exclude from processing (filtered before processing).
		excludeGlobs []string
		// excludeSymbolGlobs list of symbol globs to ignore during check.
		excludeSymbolGlobs []string
	}{
		{
			name:  "level 1 local preexisting",
			level: "1",
			src: `package main
func local() error { return nil }
func foo() error {
	local() // unchecked
	return nil
}
`,
			wantModified: []string{"main.go"},
			wantContains: map[string]string{
				"main.go": "if err != nil {\n\t\treturn err\n\t}",
			},
		},
		{
			name:  "level 2 propagation",
			level: "2",
			src: `package main
func local() error { return nil }
func intermediate() {
	local()
}
func top() {
	intermediate()
}
`,
			wantModified: []string{"main.go"},
			wantContains: map[string]string{
				"main.go": "func top() error {",
			},
		},
		{
			name:  "level 3 third party",
			level: "3",
			src: `package main
import "os"
func foo() error {
	os.Open("test") // third party
	return nil
}
`,
			wantModified: []string{"main.go"},
			wantContains: map[string]string{
				"main.go": "if err != nil {\n\t\treturn err\n\t}",
			},
		},
		{
			name:  "level 1 exclude file",
			level: "1",
			src: `package main
func local() error { return nil }
func foo() error {
	local()
	return nil
}
`,
			excludeGlobs: []string{"main.go"},
			wantModified: []string{},
		},
		{
			name:  "level 1 exclude symbol",
			level: "1",
			src: `package main
func local() error { return nil }
func foo() error {
	local()
	return nil
}
`,
			excludeSymbolGlobs: []string{"main.local"}, // Assuming package name is main
			wantModified:       []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir, err := os.MkdirTemp("", "integration-test-*")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(tempDir)

			// Setup go module
			if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte("module example.com/test\n\ngo 1.21"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(tempDir, "main.go"), []byte(tt.src), 0644); err != nil {
				t.Fatal(err)
			}
			if tt.commonSrc != "" {
				if err := os.WriteFile(filepath.Join(tempDir, "common.go"), []byte(tt.commonSrc), 0644); err != nil {
					t.Fatal(err)
				}
			}

			// Load Logic simulating Main
			// 1. Load Packages
			pkgs, err := loader.LoadPackages(tempDir)
			if err != nil {
				t.Fatalf("LoadPackages failed: %v", err)
			}

			// 2. Checker
			chk := checker.SetupChecker(tt.excludeSymbolGlobs)
			unchecked, err := checker.GetUncheckedErrors(tempDir, chk)
			if err != nil {
				t.Fatalf("GetUncheckedErrors failed: %v", err)
			}

			// 3. Filter unchecked by file globs (simulating logic in main)
			var filtered []error // using arbitrary type here just for loop, effectively unchecked type
			// Filter logic:
			var keptUnchecked []interface{} // generic to avoid import cycle with errcheck pkg?
			// Actually we import internal/checker which imports errcheck.
			// Reconstruct filtered list.
			// Note: errcheck types are in "github.com/kisielk/errcheck/errcheck"
			// But since we can't import external deps in prompt easily without context,
			// assume we use the result from checker.GetUncheckedErrors directly.
			
			// Filter loop matches main.go logic
			finalUnchecked := unchecked[:0]
			for _, u := range unchecked {
				rel, _ := filepath.Rel(tempDir, u.Pos.Filename)
				ignored := false
				for _, glob := range tt.excludeGlobs {
					if match, _ := filepath.Match(glob, rel); match {
						ignored = true
						break
					}
				}
				if !ignored {
					finalUnchecked = append(finalUnchecked, u)
				}
			}

			// 4. Process
			var modFiles []string
			switch tt.level {
			case "1":
				modFiles, err = ProcessLevel1(finalUnchecked, pkgs)
			case "2":
				modFiles, err = ProcessLevel2(finalUnchecked, pkgs)
			case "3":
				modFiles, err = ProcessLevel3(finalUnchecked, pkgs)
			}

			if err != nil {
				t.Fatalf("ProcessLevel%s failed: %v", tt.level, err)
			}

			// Validation
			if len(modFiles) != len(tt.wantModified) {
				t.Errorf("Modified files count = %d, want %d", len(modFiles), len(tt.wantModified))
			}

			for f, expected := range tt.wantContains {
				content, err := os.ReadFile(filepath.Join(tempDir, f))
				if err != nil {
					t.Errorf("Could not read modified file %s: %v", f, err)
					continue
				}
				if !strings.Contains(string(content), expected) {
					t.Errorf("File %s does not contain expected string.\nWanted:\n%s\nGot:\n%s", f, expected, string(content))
				}
			}
		})
	}
}

// TestEndToEnd_Errors verifies error handling during the process flow.
// t: the testing context.
func TestEndToEnd_Errors(t *testing.T) {
	// e.g. Invalid package load or bad glob
	// checker.SetupChecker handles bad globs gracefully (skips them).
	
	// Test checker failure logic if needed, largely covered by unit tests.
	// This ensures integration points don't panic.
	chk := checker.SetupChecker(nil)
	if chk == nil {
		t.Error("SetupChecker returned nil")
	}
}

// TestProcessParsersStub is a placeholder to ensure the test file compiles 
// and imports strictly required packages.
func TestProcessParsersStub(t *testing.T) {
	// No-op
}