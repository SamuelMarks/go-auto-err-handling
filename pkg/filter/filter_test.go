package filter

import (
	"go/token"
	"go/types"
	"testing"
)

// TestMatchesFile verifies file exclusion logic using glob patterns.
func TestMatchesFile(t *testing.T) {
	fset := token.NewFileSet()

	// Create dummy files in the FileSet
	f1 := fset.AddFile("/abs/path/to/main.go", -1, 100)
	f2 := fset.AddFile("/abs/path/to/main_test.go", -1, 100)
	f3 := fset.AddFile("relative/vendor/lib.go", -1, 100)

	tests := []struct {
		name      string
		globs     []string
		pos       token.Pos
		wantMatch bool
	}{
		{
			name:      "NoGlobs",
			globs:     nil,
			pos:       f1.Pos(1),
			wantMatch: false,
		},
		{
			name:      "MatchBaseName",
			globs:     []string{"*_test.go"},
			pos:       f2.Pos(1),
			wantMatch: true,
		},
		{
			name:      "NoMatchBaseName",
			globs:     []string{"*_test.go"},
			pos:       f1.Pos(1),
			wantMatch: false,
		},
		{
			name:      "MatchDirectory",
			globs:     []string{"*/vendor/*"},
			pos:       f3.Pos(1),
			wantMatch: true,
		},
		{
			name:      "InvalidPosition",
			globs:     []string{"*"},
			pos:       token.NoPos,
			wantMatch: false,
		},
		{
			name:      "NilFileSet",
			globs:     []string{"*"},
			pos:       f1.Pos(1),
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := New(tt.globs, nil)

			var fs *token.FileSet
			if tt.name != "NilFileSet" {
				fs = fset
			}

			if got := f.MatchesFile(fs, tt.pos); got != tt.wantMatch {
				t.Errorf("MatchesFile() = %v, want %v", got, tt.wantMatch)
			}
		})
	}
}

// TestMatchesSymbol verifies symbol exclusion logic using fully qualified names.
func TestMatchesSymbol(t *testing.T) {
	// Create packages and functions for testing
	pkgFmt := types.NewPackage("fmt", "fmt")
	pkgMy := types.NewPackage("example.com/my/pkg", "pkg")

	fnPrintln := types.NewFunc(token.NoPos, pkgFmt, "Println", nil) // fmt.Println
	fnRun := types.NewFunc(token.NoPos, pkgMy, "Run", nil)          // example.com/my/pkg.Run
	fnInternal := types.NewFunc(token.NoPos, nil, "panic", nil)     // panic (builtin, no pkg)

	tests := []struct {
		name      string
		globs     []string
		fn        *types.Func
		wantMatch bool
	}{
		{
			name:      "NoGlobs",
			globs:     nil,
			fn:        fnPrintln,
			wantMatch: false,
		},
		{
			name:      "ExactMatch",
			globs:     []string{"fmt.Println"},
			fn:        fnPrintln,
			wantMatch: true,
		},
		{
			name:      "PackageWildcard",
			globs:     []string{"fmt.*"},
			fn:        fnPrintln,
			wantMatch: true,
		},
		{
			name: "DomainWildcard",
			// Update: Must handle separators. example.com has slashes.
			globs:     []string{"example.com/my/pkg.*"},
			fn:        fnRun,
			wantMatch: true,
		},
		{
			name:      "RecursiveWildcardNotSupportedByMatch",
			globs:     []string{"pkg.Run"},
			fn:        fnRun,
			wantMatch: false,
		},
		{
			name: "SuffixMatch",
			// Update: filepath.Match respects separators. We need strict matching.
			// *.Run won't match a/b.Run. We must use a glob that matches the path structure.
			// example.com/my/pkg.Run has 3 slashes.
			globs:     []string{"*/*/*.Run"},
			fn:        fnRun,
			wantMatch: true,
		},
		{
			name:      "BuiltinMatch",
			globs:     []string{"panic"},
			fn:        fnInternal,
			wantMatch: true,
		},
		{
			name:      "NilFunc",
			globs:     []string{"*"},
			fn:        nil,
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := New(nil, tt.globs)
			if got := f.MatchesSymbol(tt.fn); got != tt.wantMatch {
				t.Errorf("MatchesSymbol() = %v, want %v (symbol: %v name: %s.%s)",
					got, tt.wantMatch, tt.fn,
					func() string {
						if tt.fn.Pkg() != nil {
							return tt.fn.Pkg().Path()
						} else {
							return ""
						}
					}(),
					func() string {
						if tt.fn != nil {
							return tt.fn.Name()
						} else {
							return "nil"
						}
					}())
			}
		})
	}
}
