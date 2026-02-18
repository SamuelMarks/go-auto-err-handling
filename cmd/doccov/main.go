// Command doccov reports documentation coverage for exported identifiers.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// coverageStats holds the documentation coverage statistics.
type coverageStats struct {
	// Total is the total number of exported identifiers found.
	Total int
	// Documented is the number of exported identifiers that have documentation.
	Documented int
	// Percent is the documentation coverage percentage (0-100).
	Percent float64
	// Uncovered is the number of exported identifiers missing documentation.
	Uncovered int
	// PackageDocs is the number of packages that have package-level documentation.
	PackageDocs int
}

// missingDoc represents an exported identifier that lacks documentation.
type missingDoc struct {
	// Pkg is the package path.
	Pkg string
	// File is the file path definition.
	File string
	// Kind is the type of identifier (e.g., func, type, var).
	Kind string
	// Name is the name of the identifier.
	Name string
}

// main is the CLI entry point.
func main() {
	osExit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// osExit is a hook for os.Exit to allow testing.
var osExit = os.Exit

// packagesLoad is a hook for packages.Load to allow testing.
var packagesLoad = packages.Load

// filepathRel is a hook for filepath.Rel to allow testing.
var filepathRel = filepath.Rel

// run executes the doccov command logic.
//
// args: The command line arguments (excluding the program name).
// stdout: The writer for standard output.
// stderr: The writer for standard error.
//
// Returns the exit code (0 for success, non-zero for error).
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doccov", flag.ContinueOnError)
	fs.SetOutput(stderr)

	format := fs.String("format", "summary", "Output format: summary, percent, json")
	dir := fs.String("dir", ".", "Root directory to scan")
	min := fs.Float64("min", 100, "Minimum doc coverage percent required")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	stats, missing, err := computeCoverage(*dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	stats.Percent = percent(stats.Documented, stats.Total)
	stats.Uncovered = stats.Total - stats.Documented

	switch *format {
	case "summary":
		fmt.Fprintf(stdout, "doc coverage: %.1f%% (%d/%d)\n", stats.Percent, stats.Documented, stats.Total)
	case "percent":
		fmt.Fprintf(stdout, "%.1f\n", stats.Percent)
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(stats); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	default:
		fmt.Fprintf(stderr, "unknown format: %s\n", *format)
		return 2
	}

	if len(missing) > 0 {
		printMissing(stderr, missing, *dir)
	}

	if stats.Percent < *min {
		return 1
	}

	return 0
}

// computeCoverage analyzes documentation coverage for the given directory.
//
// dir: The directory to analyze.
//
// Returns the coverage statistics, a list of missing documentation items, and any error encountered.
func computeCoverage(dir string) (coverageStats, []missingDoc, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles,
		Dir:  dir,
		Env:  append(os.Environ(), "GOWORK=off"),
	}

	pkgs, err := packagesLoad(cfg, "./...")
	if err != nil {
		return coverageStats{}, nil, err
	}
	if len(pkgs) == 0 {
		return coverageStats{}, nil, fmt.Errorf("no packages found")
	}

	var loadErrs []string
	for _, pkg := range pkgs {
		for _, pkgErr := range pkg.Errors {
			loadErrs = append(loadErrs, pkgErr.Error())
		}
		syntax, fset, parseErrs := parsePackageSyntax(pkg)
		if len(parseErrs) > 0 {
			loadErrs = append(loadErrs, parseErrs...)
		}
		if fset != nil {
			pkg.Fset = fset
		}
		if syntax != nil {
			pkg.Syntax = syntax
		}
	}
	if len(loadErrs) > 0 {
		return coverageStats{}, nil, fmt.Errorf("package load errors:\n%s", strings.Join(loadErrs, "\n"))
	}

	var stats coverageStats
	var missing []missingDoc

	for _, pkg := range pkgs {
		if len(pkg.Syntax) == 0 {
			continue
		}

		stats.Total++
		if hasPackageDoc(pkg.Syntax) {
			stats.Documented++
			stats.PackageDocs++
		} else {
			missing = append(missing, missingDoc{
				Pkg:  pkg.PkgPath,
				File: packageDocFile(pkg),
				Kind: "package",
				Name: pkg.Name,
			})
		}

		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if !d.Name.IsExported() {
						continue
					}
					stats.Total++
					if hasDoc(d.Doc) {
						stats.Documented++
						continue
					}
					missing = append(missing, missingDoc{
						Pkg:  pkg.PkgPath,
						File: fileForPos(pkg, d.Pos()),
						Kind: funcKind(d),
						Name: d.Name.Name,
					})
				case *ast.GenDecl:
					if d.Tok == token.IMPORT {
						continue
					}
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							if !s.Name.IsExported() {
								continue
							}
							stats.Total++
							if hasDoc(s.Doc) || hasDoc(d.Doc) {
								stats.Documented++
								continue
							}
							missing = append(missing, missingDoc{
								Pkg:  pkg.PkgPath,
								File: fileForPos(pkg, s.Pos()),
								Kind: "type",
								Name: s.Name.Name,
							})
						case *ast.ValueSpec:
							for _, name := range s.Names {
								if !name.IsExported() {
									continue
								}
								stats.Total++
								if hasDoc(s.Doc) || hasDoc(d.Doc) {
									stats.Documented++
									continue
								}
								missing = append(missing, missingDoc{
									Pkg:  pkg.PkgPath,
									File: fileForPos(pkg, name.Pos()),
									Kind: valueKind(d.Tok),
									Name: name.Name,
								})
							}
						}
					}
				}
			}
		}
	}

	return stats, missing, nil
}

// parsePackageSyntax parses the Go files in a package to AST.
//
// pkg: The package to parse.
//
// Returns the AST files, FileSet, and any errors encountered during parsing.
func parsePackageSyntax(pkg *packages.Package) ([]*ast.File, *token.FileSet, []string) {
	if pkg == nil {
		return nil, nil, nil
	}
	files := append([]string{}, pkg.GoFiles...)
	if len(files) == 0 {
		files = append(files, pkg.CompiledGoFiles...)
	}
	if len(files) == 0 {
		return nil, nil, nil
	}

	fset := token.NewFileSet()
	syntax := make([]*ast.File, 0, len(files))
	var parseErrs []string
	for _, file := range files {
		parsed, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
		if err != nil {
			parseErrs = append(parseErrs, err.Error())
			continue
		}
		syntax = append(syntax, parsed)
	}
	return syntax, fset, parseErrs
}

// percent calculates the percentage of documented items.
//
// documented: The number of documented items.
// total: The total number of items.
//
// Returns the percentage as a float (0-100).
func percent(documented, total int) float64 {
	if total == 0 {
		return 100
	}
	return (float64(documented) / float64(total)) * 100
}

// hasPackageDoc checks if any file in the package has package-level documentation.
//
// files: The list of AST files in the package.
//
// Returns true if package documentation is present.
func hasPackageDoc(files []*ast.File) bool {
	for _, file := range files {
		if hasDoc(file.Doc) {
			return true
		}
	}
	return false
}

// hasDoc checks if a comment group contains text.
//
// doc: The comment group to check.
//
// Returns true if the comment group is non-nil and not empty.
func hasDoc(doc *ast.CommentGroup) bool {
	if doc == nil {
		return false
	}
	return strings.TrimSpace(doc.Text()) != ""
}

// funcKind returns the kind of function ("method" or "func").
//
// decl: The function declaration.
//
// Returns "method" if the function has a receiver, "func" otherwise.
func funcKind(decl *ast.FuncDecl) string {
	if decl.Recv != nil {
		return "method"
	}
	return "func"
}

// valueKind returns the kind of value ("const" or "var").
//
// tok: The token type.
//
// Returns "const" for const declarations, "var" otherwise.
func valueKind(tok token.Token) string {
	if tok == token.CONST {
		return "const"
	}
	return "var"
}

// packageDocFile returns the likely file path for package documentation.
//
// pkg: The package to inspect.
//
// Returns the path of the first file in the package or empty string.
func packageDocFile(pkg *packages.Package) string {
	if pkg == nil {
		return ""
	}
	if pkg.Fset == nil || len(pkg.Syntax) == 0 {
		if len(pkg.GoFiles) > 0 {
			return pkg.GoFiles[0]
		}
		return ""
	}
	pos := pkg.Syntax[0].Package
	return pkg.Fset.Position(pos).Filename
}

// fileForPos returns the filename for a given position.
//
// pkg: The package context.
// pos: The token position.
//
// Returns the filename associated with the position.
func fileForPos(pkg *packages.Package, pos token.Pos) string {
	if pkg == nil || pkg.Fset == nil || pos == token.NoPos {
		return ""
	}
	return pkg.Fset.Position(pos).Filename
}

// missingLess compares two missingDoc items for sorting.
//
// a: The first item.
// b: The second item.
//
// Returns true if a < b.
func missingLess(a, b missingDoc) bool {
	if a.Pkg != b.Pkg {
		return a.Pkg < b.Pkg
	}
	if a.File != b.File {
		return a.File < b.File
	}
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	return a.Name < b.Name
}

// printMissing outputs the list of missing documentation.
//
// stderr: The writer to print to.
// missing: The list of missing documents.
// root: The root directory for relative path calculation.
func printMissing(stderr io.Writer, missing []missingDoc, root string) {
	sort.Slice(missing, func(i, j int) bool {
		return missingLess(missing[i], missing[j])
	})

	fmt.Fprintln(stderr, "missing docs:")
	for _, item := range missing {
		file := item.File
		if file != "" && root != "" {
			if rel, err := filepathRel(root, file); err == nil {
				file = rel
			}
		}
		if file == "" {
			file = "<unknown>"
		}
		fmt.Fprintf(stderr, "- %s: %s %s (%s)\n", item.Pkg, item.Kind, item.Name, file)
	}
}
