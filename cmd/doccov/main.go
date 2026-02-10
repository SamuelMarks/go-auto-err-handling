// Command doccov reports documentation coverage for exported identifiers.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

type coverageStats struct {
	Total       int
	Documented  int
	Percent     float64
	Uncovered   int
	PackageDocs int
}

type missingDoc struct {
	Pkg  string
	File string
	Kind string
	Name string
}

func main() {
	osExit(run(os.Args[1:], os.Stdout, os.Stderr))
}

var osExit = os.Exit
var packagesLoad = packages.Load
var filepathRel = filepath.Rel

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

func computeCoverage(dir string) (coverageStats, []missingDoc, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedSyntax | packages.NeedFiles,
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

func percent(documented, total int) float64 {
	if total == 0 {
		return 100
	}
	return (float64(documented) / float64(total)) * 100
}

func hasPackageDoc(files []*ast.File) bool {
	for _, file := range files {
		if hasDoc(file.Doc) {
			return true
		}
	}
	return false
}

func hasDoc(doc *ast.CommentGroup) bool {
	if doc == nil {
		return false
	}
	return strings.TrimSpace(doc.Text()) != ""
}

func funcKind(decl *ast.FuncDecl) string {
	if decl.Recv != nil {
		return "method"
	}
	return "func"
}

func valueKind(tok token.Token) string {
	if tok == token.CONST {
		return "const"
	}
	return "var"
}

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

func fileForPos(pkg *packages.Package, pos token.Pos) string {
	if pkg == nil || pkg.Fset == nil || pos == token.NoPos {
		return ""
	}
	return pkg.Fset.Position(pos).Filename
}

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
