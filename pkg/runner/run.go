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
// Fields use positive logic ("Enable...") to explicitly state intended behavior.
type Options struct {
	// EnablePreexistingErr enables fixes for functions that already return an error. (Level 0)
	EnablePreexistingErr bool

	// EnableNonExistingErr enables signature refactoring for functions that do not return an error. (Level 1)
	EnableNonExistingErr bool

	// EnableThirdPartyErr enables checking errors from third-party packages. (Level 2)
	EnableThirdPartyErr bool

	// EnableTestRefactor enables refactoring within test functions.
	EnableTestRefactor bool

	// Check enables verification mode.
	// If true, the tool will exit with an error if any unhandled errors are detected, without modifying files.
	Check bool

	// ExcludeGlob is a list of file glob patterns to ignore.
	ExcludeGlob []string

	// ExcludeSymbolGlob is a list of symbol patterns to ignore.
	ExcludeSymbolGlob []string

	// DryRun prints diffs instead of writing files.
	// If Check is true, DryRun is implicitly true.
	DryRun bool

	// UseDefaultExclusions applies the default list of ignored symbols.
	UseDefaultExclusions bool

	// Paths to analyze.
	Paths []string

	// MainHandler injection strategy.
	MainHandler string

	// ErrorTemplate return statement template.
	ErrorTemplate string
}

// Run executes the analysis and refactoring.
//
// opts: specific configuration options for the run.
// Returns an error if analysis fails or if Check mode finds unhandled errors.
func Run(opts Options) error {
	// Enforce DryRun if Check is enabled to prevent modification
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
		// If "UseDefaultExclusions" is true, we APPEND the defaults to the glob list
		if opts.UseDefaultExclusions {
			globs = append(globs, filter.GetDefaults()...)
		}
		flt := filter.New(opts.ExcludeGlob, globs)

		// Analyze
		// We use opts.DryRun as a proxy for debug verbosity.
		debugAnalyze := opts.DryRun

		log.Println("Analyzing codebase for unhandled errors ...")
		points, err := analysis.Detect(pkgs, flt, debugAnalyze)
		if err != nil {
			return fmt.Errorf("analysis failed: %v", err)
		}

		// Check Logic (Verification Mode)
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
			return nil // Success
		}

		// Standard Logic
		if len(points) == 0 {
			log.Println("No unhandled errors found. Codebase is stable.")
			break
		}
		log.Printf("Found %d unhandled errors.", len(points))

		// Apply Changes
		changesCount, err := applyChanges(pkgSliceMap(pkgs), points, opts)
		if err != nil {
			return err
		}
		if changesCount == 0 {
			log.Println("No changes could be applied associated with active levels.")
			break
		}

		// Dry Run Output (Preview)
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

// pkgSliceMap converts a slice of packages into a map keyed by ID.
// Helper for efficient lookups during global analysis (like propagation).
func pkgSliceMap(pkgs []*packages.Package) map[string]*packages.Package {
	m := make(map[string]*packages.Package)
	for _, p := range pkgs {
		m[p.ID] = p
	}
	return m
}

// applyChanges processes injection points and applies refactorings based on options.
// It returns the number of changes applied and any fatal error encountered.
func applyChanges(pkgMap map[string]*packages.Package, points []analysis.InjectionPoint, opts Options) (int, error) {
	changes := 0

	for _, p := range points {
		// 1. Filter Third Party
		if !opts.EnableThirdPartyErr {
			if isThirdPartyCall(p) {
				continue
			}
		}

		// 2. Identify Context (Decl or Lit)
		ctx := FindEnclosingFunc(p.Pkg, p.File, p.Pos)
		if ctx == nil {
			// Without context, we can't safely refactor returns.
			continue
		}

		// Check Test Filter
		if !opts.EnableTestRefactor {
			if ctx.Decl != nil && filter.IsTestHandler(ctx.Decl) {
				continue
			}
		}

		// Determine if function already returns error using TypesInfo or AST fallback
		returnsErr := hasErrorReturn(ctx.Sig)
		if !returnsErr && ctx.Decl != nil {
			// TypesInfo fallback if stale
			returnsErr = hasErrorReturnAST(ctx.Decl)
		}

		// Special Handling: Entry Points (main/init)
		// We cannot change signatures here, so we inject terminal handling directly.
		if ctx.Decl != nil && isEntryPoint(ctx.Decl) {
			// Ensure we are allowed to refactor this level. Since main doesn't return error,
			// this technically falls under NonExistingErr logic, but PreexistingErr user intent usually
			// covers "fix my code". Use relaxed check for entry points.
			if opts.EnableNonExistingErr || opts.EnablePreexistingErr {
				if err := refactor.HandleEntryPoint(p.File, p.Call, p.Stmt, opts.MainHandler); err != nil {
					return changes, err
				}
				changes++
			}
			continue
		}

		// Case A: Level 0 (Preexisting Error in signature)
		if returnsErr {
			if opts.EnablePreexistingErr {
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

		// Case B: Level 1 (NonExisting Error - Signature change required)
		if opts.EnableNonExistingErr {
			// Cannot change signature of Literals (Closures) easily
			if ctx.IsLiteral() {
				continue
			}
			if ctx.Decl == nil {
				continue
			}

			// Prohibit signature refactoring of Test Handlers even if enabled,
			// as it breaks the go testing runner.
			if filter.IsTestHandler(ctx.Decl) {
				// We can't change `func TestX(t *T)` to `func TestX(t *T) error`.
				continue
			}

			// 1. Mutate Signature
			changed, err := refactor.AddErrorToSignature(p.Pkg.Fset, ctx.Decl)
			if err != nil {
				return changes, err
			}
			if changed {
				changes++

				// 2. Mutate Body
				injector := rewrite.NewInjector(p.Pkg, opts.ErrorTemplate)
				appliedBody, err := injector.RewriteFile(p.File, []analysis.InjectionPoint{p})
				if err != nil {
					return changes, err
				}
				if appliedBody {
					changes++
				}

				// 3. Propagation
				funcObj := p.Pkg.TypesInfo.ObjectOf(ctx.Decl.Name)
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

// isEntryPoint checks if the declaration is a main or init function.
func isEntryPoint(decl *ast.FuncDecl) bool {
	if decl.Name.Name == "init" {
		return true
	}
	if decl.Name.Name == "main" {
		return true
	}
	return false
}

// hasErrorReturn checks if the function signature includes an error return value.
func hasErrorReturn(sig *types.Signature) bool {
	if sig.Results().Len() == 0 {
		return false
	}
	last := sig.Results().At(sig.Results().Len() - 1)
	return isErrorType(last.Type())
}

// hasErrorReturnAST checks if the function declaration AST includes an error return value.
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

// isThirdPartyCall determines if the injection point originates from a third-party package.
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

// resolveCallObject attempts to find the definition object of the called function.
func resolveCallObject(info *types.Info, call *ast.CallExpr) types.Object {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return info.ObjectOf(fun)
	case *ast.SelectorExpr:
		return info.ObjectOf(fun.Sel)
	}
	return nil
}

// isErrorType checks if the type matches the error interface.
func isErrorType(t types.Type) bool {
	return t.String() == "error" || t.String() == "builtin.error" ||
		types.Identical(t, types.Universe.Lookup("error").Type())
}

// printDiffs generates and prints a unified diff of changes made to the files.
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

// savePackages writes modified files back to disk.
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

// savePackageFiles writes individual files of a package.
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

// formatAST formats the AST node using go/format and goimports.
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
