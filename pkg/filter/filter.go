package filter

import (
	"fmt"
	"go/token"
	"go/types"
	"path/filepath"
)

// Filter determines whether specific files or symbols should be excluded from analysis.
// It uses glob patterns for file paths and symbol names.
type Filter struct {
	fileGlobs   []string
	symbolGlobs []string
}

// New creates a new Filter with the provided glob patterns.
//
// fileGlobs: A list of patterns to match against file paths (e.g., "*_test.go", "vendor/*").
// symbolGlobs: A list of patterns to match against fully qualified symbol names (e.g., "fmt.*", "github.com/pkg/errors.Wrap").
func New(fileGlobs, symbolGlobs []string) *Filter {
	return &Filter{
		fileGlobs:   fileGlobs,
		symbolGlobs: symbolGlobs,
	}
}

// MatchesFile checks if the file corresponding to the provided token.Pos is excluded.
// It resolves the filename using the provided FileSet and checks against the configured file globs.
// Returns false if the position is invalid or the file cannot be determined.
//
// fset: The file set containing the position.
// pos: The position within the file to check.
func (f *Filter) MatchesFile(fset *token.FileSet, pos token.Pos) bool {
	if fset == nil || !pos.IsValid() {
		return false
	}

	tf := fset.File(pos)
	if tf == nil {
		return false
	}

	path := tf.Name()

	for _, pattern := range f.fileGlobs {
		// We check against the full path.
		// A more advanced implementation might check relative paths or base names,
		// but standard filepath.Match behavior is applied here.
		matched, err := filepath.Match(pattern, path)
		if err == nil && matched {
			return true
		}

		// Also check against base name for convenience (e.g. "*.pb.go" matching "/abs/path/to/foo.pb.go")
		base := filepath.Base(path)
		matchedBase, errBase := filepath.Match(pattern, base)
		if errBase == nil && matchedBase {
			return true
		}
	}
	return false
}

// MatchesSymbol checks if the provided function symbol is excluded.
// It constructs the fully qualified name (<package-path>.<function-name>) and checks against symbol globs.
//
// fn: The function object to check.
func (f *Filter) MatchesSymbol(fn *types.Func) bool {
	if fn == nil {
		return false
	}

	pkgName := ""
	if fn.Pkg() != nil {
		pkgName = fn.Pkg().Path()
	}

	// Construct fully qualified name: "package/path.FuncName"
	// For methods, it effectively uses the function name.
	// Note: types.Func only holds the name. For methods, it doesn't automatically include the receiver type in the Name().
	// Uniquely identifying methods (e.g. (*T).M) requires Type() analysis,
	// but mostly users filter packages or specific function names (e.g. "os.Exit").
	fullName := fn.Name()
	if pkgName != "" {
		fullName = fmt.Sprintf("%s.%s", pkgName, fn.Name())
	}

	for _, pattern := range f.symbolGlobs {
		matched, err := filepath.Match(pattern, fullName)
		if err == nil && matched {
			return true
		}
	}

	return false
}
