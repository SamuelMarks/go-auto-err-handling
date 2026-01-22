package runner

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"go/types"
	"log"
	"os"
	"strings"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/analysis"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/filter"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/loader"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/refactor"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/rewrite"
	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/imports"
)

type Options struct {
	LocalPreexistingErr bool
	LocalNonExistingErr bool
	ThirdPartyErr       bool
	ExcludeGlob         []string
	ExcludeSymbolGlob   []string
	DryRun              bool
	NoDefaultExclusion  bool
	Paths               []string
	MainHandler         string
	ErrorTemplate       string
}

func Run(opts Options) error {
	const maxIterations = 5
	for i := 0; i < maxIterations; i++ {
		log.Printf("Iteration %d: Loading packages...", i+1)
		pkgs, err := loader.LoadPackages(opts.Paths, ".")
		if err != nil {
			return fmt.Errorf("failed to load packages: %v", err)
		}
		if len(pkgs) == 0 {
			log.Println("No packages found.")
			return nil
		}

		globs := opts.ExcludeSymbolGlob
		if !opts.NoDefaultExclusion {
			globs = append(globs, filter.GetDefaults()...)
		}
		flt := filter.New(opts.ExcludeGlob, globs)

		log.Println("Analyzing codebase for unhandled errors ...")
		points, err := analysis.Detect(pkgs, flt)
		if err != nil {
			return fmt.Errorf("analysis failed: %v", err)
		}
		if len(points) == 0 {
			log.Println("No unhandled errors found. Codebase is stable.")
			break
		}
		log.Printf("Found %d unhandled errors.", len(points))

		changesCount, err := applyChanges(pkgSliceMap(pkgs), points, opts)
		if err != nil {
			return err
		}
		if changesCount == 0 {
			log.Println("No changes could be applied (configuration may restrict edits). Stopping.")
			break
		}

		if opts.DryRun {
			log.Printf("Dry Run: Generated %d changes. printing diffs...", changesCount)
			if err := printDiffs(pkgs); err != nil {
				return fmt.Errorf("failed to print diffs: %v", err)
			}
			break
		}

		log.Printf("Applied %d changes. Saving files...", changesCount)
		if err := savePackages(pkgs); err != nil {
			return fmt.Errorf("failed to save files: %v", err)
		}
	}
	return nil
}

func pkgSliceMap(pkgs []*packages.Package) map[string]*packages.Package {
	m := make(map[string]*packages.Package)
	for _, p := range pkgs {
		m[p.ID] = p
	}
	return m
}

func applyChanges(pkgMap map[string]*packages.Package, points []analysis.InjectionPoint, opts Options) (int, error) {
	changes := 0
	// We handle points. We use a map to keep track of modifications if needed, but Injector modifies AST in place.
	// Since points can be in the same file, updating one modifies the underlying AST for subsequent points in that loop/iteration.

	for _, p := range points {
		if !opts.ThirdPartyErr {
			if isThirdPartyCall(p) {
				continue
			}
		}

		enclosingSig, enclosingFuncDecl := findEnclosingFunc(p)
		if enclosingSig == nil {
			continue
		}

		returnsErr := hasErrorReturn(enclosingSig)

		// Level 0
		if returnsErr {
			if opts.LocalPreexistingErr || opts.LocalNonExistingErr || opts.ThirdPartyErr {
				injector := rewrite.NewInjector(p.Pkg, opts.ErrorTemplate)
				applied, err := injector.RewriteFile(p.File, []analysis.InjectionPoint{p})
				if err != nil {
					return changes, err
				}
				if applied {
					changes++
				}
			}
			continue
		}

		// Level 1
		if opts.LocalNonExistingErr || opts.ThirdPartyErr {
			if enclosingFuncDecl == nil {
				continue
			}

			// 1. Signature Mutation
			changed, err := refactor.AddErrorToSignature(p.Pkg.Fset, enclosingFuncDecl)
			if err != nil {
				return changes, err
			}
			if changed {
				changes++

				// 2. Atomic Body Mutation
				// Now that signature is updated in AST (but not TypesInfo), we immediately try to fix the call site.
				injector := rewrite.NewInjector(p.Pkg, opts.ErrorTemplate)
				appliedBody, err := injector.RewriteFile(p.File, []analysis.InjectionPoint{p})
				if err != nil {
					return changes, err
				}
				if appliedBody {
					changes++
				}

				// 3. Propagation
				funcObj := p.Pkg.TypesInfo.ObjectOf(enclosingFuncDecl.Name)
				if funcObj != nil {
					if targetFunc, ok := funcObj.(*types.Func); ok {
						var allPkgs []*packages.Package
						for _, pkg := range pkgMap {
							allPkgs = append(allPkgs, pkg)
						}
						propagated, err := refactor.PropagateCallers(allPkgs, targetFunc, opts.MainHandler)
						if err != nil {
							return changes, fmt.Errorf("propagation failed: %v", err)
						}
						changes += propagated
					}
				}
			}
		}
	}
	return changes, nil
}

func printDiffs(pkgs []*packages.Package) error {
	for _, pkg := range pkgs {
		for i, astFile := range pkg.Syntax {
			if i >= len(pkg.GoFiles) {
				continue
			}
			filename := pkg.GoFiles[i]
			originalContent, err := os.ReadFile(filename)
			if err != nil {
				return fmt.Errorf("failed to read original file %s: %w", filename, err)
			}

			// FormatAST handles goimports
			newContentBytes, err := formatAST(pkg.Fset, astFile, filename)
			if err != nil {
				return fmt.Errorf("failed to format AST for %s: %w", filename, err)
			}
			newContent := string(newContentBytes)

			if string(originalContent) == newContent {
				continue
			}

			edits := myers.ComputeEdits(span.URIFromPath(filename), string(originalContent), newContent)
			diff := fmt.Sprint(gotextdiff.ToUnified(filename, filename, string(originalContent), edits))
			fmt.Print(diff)
		}
	}
	return nil
}

func savePackages(pkgs []*packages.Package) error {
	written := make(map[string]bool)

	// Pass 1: Prioritize saving from non-test packages to ensure canonical AST is used.
	for _, pkg := range pkgs {
		isTestVariant := strings.HasSuffix(pkg.ID, ".test]") || strings.HasSuffix(pkg.ID, ".test")
		if !isTestVariant {
			if err := savePackageFiles(pkg, written); err != nil {
				return err
			}
		}
	}

	// Pass 2: Save from test variants if file wasn't written yet (handling cases where only test pkg is loaded)
	for _, pkg := range pkgs {
		isTestVariant := strings.HasSuffix(pkg.ID, ".test]") || strings.HasSuffix(pkg.ID, ".test")
		if isTestVariant {
			if err := savePackageFiles(pkg, written); err != nil {
				return err
			}
		}
	}
	return nil
}

func savePackageFiles(pkg *packages.Package, written map[string]bool) error {
	for i, astFile := range pkg.Syntax {
		if i >= len(pkg.GoFiles) {
			continue
		}
		filename := pkg.GoFiles[i]

		if written[filename] {
			continue
		}

		// Use formatAST which applies goimports
		formatted, err := formatAST(pkg.Fset, astFile, filename)
		if err != nil {
			return err
		}

		if err := os.WriteFile(filename, formatted, 0644); err != nil {
			return err
		}

		written[filename] = true
	}
	return nil
}

// formatAST renders the AST to a buffer and then applies imports.Process (goimports).
func formatAST(fset *token.FileSet, node interface{}, filename string) ([]byte, error) {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, node); err != nil {
		return nil, fmt.Errorf("ast format failure: %w", err)
	}

	// Apply goimports
	// imports.Process() can fix missing imports even if the current source is valid
	out, err := imports.Process(filename, buf.Bytes(), nil)
	if err != nil {
		// If imports processing fails (e.g. syntax error in generated code),
		// return the error but also maybe the raw code for debugging could be useful.
		// For now, strict error return.
		return nil, fmt.Errorf("goimports processing failure: %w", err)
	}

	return out, nil
}

func isThirdPartyCall(p analysis.InjectionPoint) bool {
	fn := getCalledFunction(p.Pkg.TypesInfo, p.Call)
	if fn == nil || fn.Pkg() == nil {
		return false
	}
	if p.Pkg.Module != nil {
		if strings.HasPrefix(fn.Pkg().Path(), p.Pkg.Module.Path) {
			return false
		}
	}
	callerPkg := strings.TrimSuffix(p.Pkg.PkgPath, ".test")
	targetPkg := strings.TrimSuffix(fn.Pkg().Path(), ".test")
	if strings.Contains(callerPkg, " ") {
		parts := strings.Fields(callerPkg)
		callerPkg = parts[0]
	}
	if targetPkg == callerPkg {
		return false
	}
	return true
}

func findEnclosingFunc(p analysis.InjectionPoint) (*types.Signature, *ast.FuncDecl) {
	path, _ := astutil.PathEnclosingInterval(p.File, p.Pos, p.Pos)
	for _, node := range path {
		if fd, ok := node.(*ast.FuncDecl); ok {
			obj := p.Pkg.TypesInfo.ObjectOf(fd.Name)
			if obj != nil {
				if sig, ok := obj.Type().(*types.Signature); ok {
					return sig, fd
				}
			}
		}
	}
	return nil, nil
}

func hasErrorReturn(sig *types.Signature) bool {
	if sig.Results().Len() == 0 {
		return false
	}
	last := sig.Results().At(sig.Results().Len() - 1)
	return isErrorType(last.Type())
}

func getCalledFunction(info *types.Info, call *ast.CallExpr) *types.Func {
	if ident, ok := call.Fun.(*ast.Ident); ok {
		if obj := info.ObjectOf(ident); obj != nil {
			if fn, ok := obj.(*types.Func); ok {
				return fn
			}
		}
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if obj := info.ObjectOf(sel.Sel); obj != nil {
			if fn, ok := obj.(*types.Func); ok {
				return fn
			}
		}
	}
	return nil
}

func isErrorType(t types.Type) bool {
	return t.String() == "error" || t.String() == "builtin.error" ||
		types.Identical(t, types.Universe.Lookup("error").Type())
}
