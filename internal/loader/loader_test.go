// Package loader provides tests for the package loading functionality.
package loader

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadPackages tests the LoadPackages function with various scenarios.
// It covers successful loading, loading with syntax errors, and invalid directories.
//
// t: the testing context.
func TestLoadPackages(t *testing.T) {
	tests := []struct {
		// name is the name of the test case.
		name string
		// setup prepares the test environment.
		setup func(dir string) error
		// dir overrides the directory passed to LoadPackages.
		dir string
		// wantErr indicates if an error is expected from LoadPackages.
		wantErr bool
		// wantPkgs is the expected number of packages returned.
		wantPkgs int
		// wantErrors indicates if the returned packages should contain errors (e.g. syntax).
		wantErrors bool
	}{
		{
			name: "successful load simple package",
			setup: func(dir string) error {
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644); err != nil {
					return err
				}
				return nil
			},
			wantPkgs: 1,
		},
		{
			name: "multiple packages",
			setup: func(dir string) error {
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
					return err
				}
				if err := os.Mkdir(filepath.Join(dir, "sub"), 0755); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nimport \"test/sub\"\nfunc main() { sub.Foo() }\n"), 0644); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(dir, "sub", "sub.go"), []byte("package sub\nfunc Foo() {}\n"), 0644); err != nil {
					return err
				}
				return nil
			},
			wantPkgs: 2,
		},
		{
			name: "syntax error in file",
			setup: func(dir string) error {
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n invalid syntax\n"), 0644); err != nil {
					return err
				}
				return nil
			},
			wantErrors: true,
			wantPkgs:   1, // Still returns the package just with errors attached
		},
		{
			name:    "invalid directory",
			dir:     "/nonexistent/directory",
			wantErr: true,
		},
		{
			name: "empty directory no go files",
			setup: func(dir string) error {
				return nil
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir, err := os.MkdirTemp("", "loader-test-*")
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

			got, err := LoadPackages(dir)
			if (err != nil) != tt.wantErr {
				t.Errorf("LoadPackages() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if len(got) != tt.wantPkgs {
				t.Errorf("LoadPackages() len(pkgs) = %d, want %d", len(got), tt.wantPkgs)
			}
			if tt.wantErrors {
				hasErrors := false
				for _, pkg := range got {
					if len(pkg.Errors) > 0 {
						hasErrors = true
						break
					}
				}
				if !hasErrors {
					t.Errorf("LoadPackages() expected errors in package, got none")
				}
			} else {
				for _, pkg := range got {
					if len(pkg.Errors) > 0 {
						t.Errorf("LoadPackages() unexpected errors in package: %v", pkg.Errors)
					}
					if len(pkg.Syntax) == 0 && tt.wantPkgs > 0 {
						// Some test setups might yield packages without syntax (like just a mod file? unlikely with load ./...)
						// But here we expect valid loading.
					}
					if pkg.TypesInfo == nil {
						t.Errorf("LoadPackages() no types info for package %s", pkg.PkgPath)
					}
				}
			}
		})
	}
}