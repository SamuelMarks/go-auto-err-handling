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
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/imports"
)

// Options configuration for the runner.
type Options struct {
	EnablePreexistingErr bool
	EnableNonExistingErr bool
	EnableThirdPartyErr  bool
	EnableTestRefactor   bool
	Check                bool
	ExcludeGlob          []string
	ExcludeSymbolGlob    []string
	DryRun               bool
	UseDefaultExclusions bool
	Paths                []string
	MainHandler          string
	ErrorTemplate        string
}

// Run executes the analysis and refactoring.
func Run(opts Options) error {
	if opts.Check {
		opts.DryRun = true
	}

	const maxIterations = 5
	for i := 0; i < maxIterations; i++ {
		if !opts.Check {
			log.Printf("Iteration %d: Loading packages...", i+1)
		} else {
			log.Printf("Loading packages for verification...")
		}

		pkgs, err := loader.LoadPackages(opts.Paths, ".")
		if err != nil {
			return fmt.Errorf("failed to load packages: %v", err)
		}
		if len(pkgs) == 0 {
			log.Println("No packages found.")
			return nil
		}

		// Configure Filter
		globs := opts.ExcludeSymbolGlob
		if opts.UseDefaultExclusions {
			globs = append(globs, filter.GetDefaults()...)
		}
		flt := filter.New(opts.ExcludeGlob, globs)

		// Initialize Interface Registry
		log.Println("Building interface compliance registry...")
		ifaceRegistry := analysis.NewInterfaceRegistry(pkgs)

		// Analyze
		debugAnalyze := opts.DryRun
		log.Println("Analyzing codebase for unhandled errors ...")
		points, err := analysis.Detect(pkgs, flt, debugAnalyze)
		if err != nil {
			return fmt.Errorf("analysis failed: %v", err)
		}

		if opts.Check {
			if len(points) > 0 {
				log.Printf("[FAIL] Found %d unhandled errors:", len(points))
				for _, p := range points {
					pos := p.Pkg.Fset.Position(p.Pos)
					log.Printf("  %s:%d: Unhandled error from call to %s", pos.Filename, pos.Line, p.Call.Fun)
				}
				return fmt.Errorf("check failed: %d unhandled errors found", len(points))
			}
			log.Println("No unhandled errors found. Codebase is stable.")
			return nil
		}

		if len(points) == 0 {
			log.Println("No unhandled errors found. Codebase is stable.")
			break
		}
		log.Printf("Found %d unhandled errors.", len(points))

		// Apply Changes
		changesCount, err := applyChanges(pkgSliceMap(pkgs), points, opts, ifaceRegistry)
		if err != nil {
			return err
		}
		if changesCount == 0 {
			log.Println("No changes could be applied associated with active levels.")
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

// applyChanges processes injection points.
func applyChanges(pkgMap map[string]*packages.Package, points []analysis.InjectionPoint, opts Options, registry *analysis.InterfaceRegistry) (int, error) {
	changes := 0

	for _, p := range points {
		if !opts.EnableThirdPartyErr {
			if isThirdPartyCall(p) {
				continue
			}
		}

		ctx := FindEnclosingFunc(p.Pkg, p.File, p.Pos)
		if ctx == nil {
			continue
		}

		if !opts.EnableTestRefactor {
			if ctx.Decl != nil && filter.IsTestHandler(ctx.Decl) {
				continue
			}
		}

		returnsErr := hasErrorReturn(ctx.Sig)
		if !returnsErr && ctx.Decl != nil {
			returnsErr = hasErrorReturnAST(ctx.Decl)
		}

		// Entry Points
		if ctx.Decl != nil && isEntryPoint(ctx.Decl) {
			if opts.EnableNonExistingErr || opts.EnablePreexistingErr {
				if err := refactor.HandleEntryPoint(p.File, p.Call, p.Stmt, opts.MainHandler); err != nil {
					return changes, err
				}
				changes++
			}
			continue
		}

		// Level 0: Preexisting Error
		if returnsErr {
			if opts.EnablePreexistingErr {
				injector := rewrite.NewInjector(p.Pkg, opts.ErrorTemplate, opts.MainHandler)
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

		// Level 1: NonExisting Error (Signature Change)
		if opts.EnableNonExistingErr {
			if ctx.IsLiteral() || ctx.Decl == nil {
				continue
			}
			if filter.IsTestHandler(ctx.Decl) {
				continue
			}

			// --- Interface Compliance Check ---
			isBlockedByInterface := false

			// We use ObjectOf to look up the function definition.
			funcObj := p.Pkg.TypesInfo.ObjectOf(ctx.Decl.Name)

			if funcObj != nil {
				if method, ok := funcObj.(*types.Func); ok {
					conflicts, err := registry.CheckCompliance(method)
					if err != nil && opts.DryRun {
						log.Printf("[WARN] Compliance check error for %s: %v", method.Name(), err)
					}
					if len(conflicts) > 0 {
						if opts.DryRun {
							log.Printf("[SKIP] Cannot refactor %s.%s because it implements interface %s.%s",
								p.Pkg.Name, ctx.Decl.Name.Name, conflicts[0].Interface.Pkg().Name(), conflicts[0].Interface.Name())
						}
						isBlockedByInterface = true
					}
				}
			}

			if isBlockedByInterface {
				// FIX: Fallback to logging if interface prevents refactoring.
				// This restores 'ignored error' logging for methods like handlers where changing
				// the signature would break the interface contract.
				injector := rewrite.NewInjector(p.Pkg, opts.ErrorTemplate, opts.MainHandler)
				applied, err := injector.LogFallback(p.File, p)
				if err != nil {
					return changes, err
				}
				if applied {
					changes++
				}
				continue
			}
			// ----------------------------------

			// 1. Mutate Signature
			changed, err := refactor.AddErrorToSignature(p.Pkg.Fset, ctx.Decl)
			if err != nil {
				return changes, err
			}
			if changed {
				var pkg *types.Package
				if ctx.Sig.Params().Len() > 0 && ctx.Sig.Params().At(0).Pkg() != nil {
					pkg = ctx.Sig.Params().At(0).Pkg()
				} else {
					pkg = p.Pkg.Types
				}

				if err := PatchSignature(p.Pkg.TypesInfo, ctx.Decl, pkg); err != nil {
					if err1 := PatchSignature(p.Pkg.TypesInfo, ctx.Decl, p.Pkg.Types); err1 != nil {
						return changes, err1
					}
				}

				changes++

				// 2. Mutate Body
				injector := rewrite.NewInjector(p.Pkg, opts.ErrorTemplate, opts.MainHandler)
				appliedBody, err := injector.RewriteFile(p.File, []analysis.InjectionPoint{p})
				if err != nil {
					return changes, err
				}
				if appliedBody {
					changes++
				}

				// 3. Propagation
				// Resolve the *new* function object from TypesInfo after patching to ensure PropagateCallers sees the updated signature.
				newObj := p.Pkg.TypesInfo.ObjectOf(ctx.Decl.Name)
				if newObj != nil {
					if targetFunc, ok := newObj.(*types.Func); ok {
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
		} else if !returnsErr {
			// If we're not in Level 1 mode but the function doesn't return error,
			// we should still apply the log fallback to handle the unhandled error
			injector := rewrite.NewInjector(p.Pkg, opts.ErrorTemplate, opts.MainHandler)
			applied, err := injector.LogFallback(p.File, p)
			if err != nil {
				return changes, err
			}
			if applied {
				changes++
			}
		}
	}
	return changes, nil
}

func isEntryPoint(decl *ast.FuncDecl) bool {
	if decl.Name.Name == "init" {
		return true
	}
	if decl.Name.Name == "main" {
		return true
	}
	return false
}

func hasErrorReturn(sig *types.Signature) bool {
	if sig.Results().Len() == 0 {
		return false
	}
	last := sig.Results().At(sig.Results().Len() - 1)
	return isErrorType(last.Type())
}

func hasErrorReturnAST(decl *ast.FuncDecl) bool {
	if decl.Type.Results == nil || len(decl.Type.Results.List) == 0 {
		return false
	}
	lastField := decl.Type.Results.List[len(decl.Type.Results.List)-1]
	if id, ok := lastField.Type.(*ast.Ident); ok {
		return id.Name == "error"
	}
	return false
}

func isThirdPartyCall(p analysis.InjectionPoint) bool {
	callObj := resolveCallObject(p.Pkg.TypesInfo, p.Call)
	if callObj == nil {
		return false
	}
	if callObj.Pkg() == nil {
		return false
	}
	if p.Pkg.Module != nil {
		if strings.HasPrefix(callObj.Pkg().Path(), p.Pkg.Module.Path) {
			return false
		}
	}
	callerPkg := strings.TrimSuffix(p.Pkg.PkgPath, ".test")
	targetPkg := strings.TrimSuffix(callObj.Pkg().Path(), ".test")
	if strings.Contains(callerPkg, " ") {
		parts := strings.Fields(callerPkg)
		callerPkg = parts[0]
	}
	if targetPkg == callerPkg {
		return false
	}
	return true
}

func resolveCallObject(info *types.Info, call *ast.CallExpr) types.Object {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return info.ObjectOf(fun)
	case *ast.SelectorExpr:
		return info.ObjectOf(fun.Sel)
	}
	return nil
}

func isErrorType(t types.Type) bool {
	return t.String() == "error" || t.String() == "builtin.error" ||
		types.Identical(t, types.Universe.Lookup("error").Type())
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
				return fmt.Errorf("read error: %w", err)
			}
			newContentBytes, err := formatAST(pkg.Fset, astFile, filename)
			if err != nil {
				return err
			}
			if string(originalContent) == string(newContentBytes) {
				continue
			}
			edits := myers.ComputeEdits(span.URIFromPath(filename), string(originalContent), string(newContentBytes))
			diff := fmt.Sprint(gotextdiff.ToUnified(filename, filename, string(originalContent), edits))
			fmt.Print(diff)
		}
	}
	return nil
}

func savePackages(pkgs []*packages.Package) error {
	written := make(map[string]bool)
	for _, pkg := range pkgs {
		isTestVariant := strings.HasSuffix(pkg.ID, ".test]") || strings.HasSuffix(pkg.ID, ".test")
		if !isTestVariant {
			if err := savePackageFiles(pkg, written); err != nil {
				return err
			}
		}
	}
	for _, pkg := range pkgs {
		if strings.HasSuffix(pkg.ID, ".test]") || strings.HasSuffix(pkg.ID, ".test") {
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

func formatAST(fset *token.FileSet, node interface{}, filename string) ([]byte, error) {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, node); err != nil {
		return nil, fmt.Errorf("ast format error: %w", err)
	}
	out, err := imports.Process(filename, buf.Bytes(), nil)
	if err != nil {
		return nil, fmt.Errorf("goimports error: %w", err)
	}
	return out, nil
}
