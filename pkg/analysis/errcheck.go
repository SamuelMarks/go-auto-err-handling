package analysis

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// ErrcheckParser provides functionality to parse text output from the 'errcheck' tool
// and convert it into structured InjectionPoints for refactoring.
type ErrcheckParser struct {
	// fileMap maps absolute file paths to a context struct containing the Package and AST File.
	// This enables O(1) lookup when processing file paths from log output.
	fileMap map[string]fileContext
}

// fileContext internal holder for quick lookups.
type fileContext struct {
	pkg  *packages.Package
	file *ast.File
}

// NewErrcheckParser initializes a parser by indexing the provided loaded packages.
//
// pkgs: The packages loaded by the loader that correspond to the analysis target.
func NewErrcheckParser(pkgs []*packages.Package) *ErrcheckParser {
	fm := make(map[string]fileContext)
	for _, pkg := range pkgs {
		for _, syntax := range pkg.Syntax {
			if pos := pkg.Fset.Position(syntax.Pos()); pos.Filename != "" {
				// We normalize to absolute path to ensure matching works regardless of
				// how errcheck reports paths (relative vs abs).
				abs, err := filepath.Abs(pos.Filename)
				if err == nil {
					fm[abs] = fileContext{pkg: pkg, file: syntax}
				}
			}
		}
	}
	return &ErrcheckParser{fileMap: fm}
}

// Parse reads the errcheck output from the provided reader and generates injection points.
//
// reader: Source of the errcheck stdout (e.g. strings.NewReader or os.Stdin).
//
// Returns a slice of valid InjectionPoints where unhandled errors were reported.
// Lines that cannot be parsed or point to files not currently loaded are skipped silently
// or with a warning depending on implementation desires (current: skip).
func (p *ErrcheckParser) Parse(reader io.Reader) ([]InjectionPoint, error) {
	var points []InjectionPoint
	scanner := bufio.NewScanner(reader)

	for scanner.Scan() {
		line := scanner.Text()
		// errcheck format: path/to/file.go:line:col: <rest>
		// e.g. main.go:12:15:	func()
		parts := strings.SplitN(line, ":", 4)
		if len(parts) < 3 {
			continue // Invalid line format
		}

		path := parts[0]
		lineNumStr := parts[1]
		colNumStr := parts[2]

		lineNum, err1 := strconv.Atoi(lineNumStr)
		colNum, err2 := strconv.Atoi(colNumStr)
		if err1 != nil || err2 != nil {
			continue
		}

		// Resolve File
		absPath, err := filepath.Abs(path)
		if err != nil {
			continue
		}

		ctx, ok := p.fileMap[absPath]
		if !ok {
			// File reported by errcheck is not in the loaded package set.
			continue
		}

		// Locate the exact AST node and create the InjectionPoint
		// errcheck reports the position of the identifier being called.
		// We need to resolve that position to a token.Pos in our FileSet.
		//
		// We can't trust simple line conversion because FileSet base might vary.
		// We iterate the file's LineInfo to find the Pos.
		// Alternatively, since we have the AST file, we can iterate or use a faster lookup if Fset/File is utilized correctly.
		// The safest, standard way given a *token.File (which we can get from Fset) is LineStart + offset.
		// However, ast.File doesn't expose strict offsets easily without the token.File.
		// We use the package's Fset.
		tokenFile := findTokenFile(ctx.pkg.Fset, absPath)
		if tokenFile == nil {
			continue
		}

		// Calculate Pos from line/col
		// Add line offset
		if lineNum > tokenFile.LineCount() {
			continue
		}
		lineStart := tokenFile.LineStart(lineNum)
		// Col is byte offset on line usually. Pos is simply lineStart + col - 1.
		pos := lineStart + token.Pos(colNum-1)

		point, found := resolveNodeContext(ctx, pos)
		if found {
			points = append(points, point)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading errcheck output: %w", err)
	}

	return points, nil
}

// findTokenFile locates the token.File correspondence for a filename in a FileSet.
//
// fset: The file set.
// name: The absolute path of the file.
func findTokenFile(fset *token.FileSet, name string) *token.File {
	var found *token.File
	fset.Iterate(func(f *token.File) bool {
		if f.Name() == name {
			found = f
			return false
		}
		return true
	})
	return found
}

// resolveNodeContext traverses the AST at the specific position to build the InjectionPoint.
//
// ctx: The file context (pkg/file).
// pos: The target position.
//
// Returns an InjectionPoint and true if a valid call site was found.
func resolveNodeContext(ctx fileContext, pos token.Pos) (InjectionPoint, bool) {
	// Look for the exact node.
	// errcheck points to the identifier. e.g. "Scan" in "fmt.Scan()".
	path, _ := astutil.PathEnclosingInterval(ctx.file, pos, pos)

	var call *ast.CallExpr
	var stmt ast.Stmt
	var assign *ast.AssignStmt

	// Traverse up from the leaf to find the CallExpr and Enclosing Stmt
	for _, node := range path {
		if c, ok := node.(*ast.CallExpr); ok && call == nil {
			call = c
		}
		if s, ok := node.(ast.Stmt); ok && stmt == nil {
			stmt = s
			if a, ok := s.(*ast.AssignStmt); ok {
				assign = a
			}
			// Once we have statement and call, we can usually stop,
			// unless the call is nested deep in an expression statement.
			// But for injection, we need the statement that wraps the call.
			if call != nil {
				break
			}
		}
	}

	if call == nil || stmt == nil {
		return InjectionPoint{}, false
	}

	return InjectionPoint{
		Pkg:    ctx.pkg,
		File:   ctx.file,
		Call:   call,
		Stmt:   stmt,
		Assign: assign,
		Pos:    call.Pos(), // Use call position for consistency
	}, true
}
