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
	"sort"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/analysis"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/filter"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/report"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/rewrite"
	"github.com/dave/dst"
	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/imports" // Added for imports.Process
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
	PanicToReturn        bool
	RetainPanics         bool // New option
	Paths                []string
	MainHandler          string
	NonErrorFallback     string
	ErrorTemplate        string
	Reporter             *report.Reporter
}

// Run executes analysis and rewrites based on the provided options.
func Run(opts Options) error {
	if opts.Check {
		opts.DryRun = true
	}
	if opts.Reporter == nil {
		opts.Reporter = report.New()
	}

	const maxIterations = 5
	for i := 0; i < maxIterations; i++ {
		prefix := fmt.Sprintf("[%d/%d]", i+1, maxIterations)
		if opts.Check {
			log.Printf("%s Analysis mode...", prefix)
		} else {
			log.Printf("%s Loading packages...", prefix)
		}

		pkgs, err := loadPackagesFn(opts.Paths, ".")
		if err != nil {
			return fmt.Errorf("load failed: %w", err)
		}
		if len(pkgs) == 0 {
			log.Println("No packages found.")
			return nil
		}

		globs := opts.ExcludeSymbolGlob
		if opts.UseDefaultExclusions {
			globs = append(globs, filter.GetDefaults()...)
		}
		flt := filter.New(opts.ExcludeGlob, globs)

		registry := newInterfaceRegistryFn(pkgs)

		points, err := detectFn(pkgs, flt, opts.DryRun)
		if err != nil {
			return fmt.Errorf("analysis failed: %w", err)
		}

		if opts.Check {
			if len(points) > 0 {
				log.Printf("[FAIL] Found %d unhandled errors.", len(points))
				return fmt.Errorf("check failed: %d unhandled errors found", len(points))
			}
			log.Println("[PASS] No unhandled errors.")
			return nil
		}

		hasPanics := false
		if opts.PanicToReturn {
			hasPanics = true
		}

		if len(points) == 0 && !hasPanics {
			log.Println("Codebase is stable.")
			break
		}

		log.Printf("Found %d unhandled errors.", len(points))

		mgr := newDstManagerFn(pkgs)
		count, err := applyRefactorsFn(mgr, points, opts, registry)
		if err != nil {
			return err
		}

		if count == 0 {
			log.Println("No changes applied (filtered or stable).")
			break
		}

		if opts.DryRun {
			if err := mgr.PrintDiffs(os.Stdout); err != nil {
				return err
			}
			break
		} else {
			if err := mgr.Save(); err != nil {
				return err
			}
		}
	}
	return nil
}

type dstManager struct {
	pkgs     map[string]*packages.Package
	cache    map[string]*dst.File
	fset     *token.FileSet
	modified map[string]bool
}

func newDstManager(pkgs []*packages.Package) *dstManager {
	m := &dstManager{
		pkgs:     make(map[string]*packages.Package),
		cache:    make(map[string]*dst.File),
		modified: make(map[string]bool),
	}
	if len(pkgs) > 0 {
		m.fset = pkgs[0].Fset
	}
	for _, p := range pkgs {
		m.pkgs[p.ID] = p
	}
	return m
}

// Get returns the decorated DST file for the given AST file, caching it on first use.
func (m *dstManager) Get(pkg *packages.Package, astFile *ast.File) (*dst.File, error) {
	tokFile := m.fset.File(astFile.Pos())
	if tokFile == nil {
		return nil, fmt.Errorf("file not found in fset")
	}
	name := tokFile.Name()

	if d, ok := m.cache[name]; ok {
		return d, nil
	}

	d, err := decorateFileFn(m.fset, astFile)
	if err != nil {
		return nil, err
	}
	m.cache[name] = d
	return d, nil
}

// MarkModified records that the given AST file was modified.
func (m *dstManager) MarkModified(astFile *ast.File) {
	tokFile := m.fset.File(astFile.Pos())
	if tokFile != nil {
		m.modified[tokFile.Name()] = true
	}
}

// PrintDiffs writes diffs for modified files to the provided writer.
func (m *dstManager) PrintDiffs(w *os.File) error {
	paths := make([]string, 0, len(m.modified))
	for k := range m.modified {
		paths = append(paths, k)
	}
	sort.Strings(paths)

	for _, path := range paths {
		orig, err := readFileFn(path)
		if err != nil {
			return err
		}

		d := m.cache[path]
		var buf bytes.Buffer
		if err := restorerFprintFn(&buf, d); err != nil {
			return err
		}

		edits := myers.ComputeEdits(span.URIFromPath(path), string(orig), buf.String())
		unified := gotextdiff.ToUnified(path, path, string(orig), edits)
		fmt.Fprint(w, unified)
	}
	return nil
}

// Save writes modified files back to disk.
func (m *dstManager) Save() error {
	for path := range m.modified {
		d := m.cache[path]
		f, err := createFileFn(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := restorerFprintFn(f, d); err != nil {
			return err
		}
	}
	return nil
}

func applyRefactors(mgr *dstManager, points []analysis.InjectionPoint, opts Options, registry *analysis.InterfaceRegistry) (int, error) {
	totalChanges := 0

	for _, p := range points {
		if !opts.EnableThirdPartyErr && isThirdParty(p) {
			continue
		}

		dstFile, err := mgr.Get(p.Pkg, p.File)
		if err != nil {
			return totalChanges, err
		}

		ctx := findEnclosingFuncFn(p.Pkg, p.File, p.Pos)
		if ctx == nil {
			continue
		}

		// Respect test refactoring flag.
		// If disable flag is set, skip any test handling.
		if !opts.EnableTestRefactor {
			if ctx.TestParam != "" {
				continue
			}
		}

		hasErr := hasErrorReturn(ctx.Sig)
		injector := rewrite.NewInjector(p.Pkg, opts.ErrorTemplate, opts.MainHandler, opts.RetainPanics)
		injector.NonErrorFallback = opts.NonErrorFallback

		if hasErr {
			if opts.EnablePreexistingErr {
				if ctx.TestParam != "" {
					injector.TestParam = ctx.TestParam
				}
				applied, err := rewriteFileFn(injector, dstFile, p.File, []analysis.InjectionPoint{p})
				if err != nil {
					return totalChanges, err
				}
				if applied {
					totalChanges++
					mgr.MarkModified(p.File)
					opts.Reporter.IncHandled()
					opts.Reporter.AddFile(mgr.fset.Position(p.File.Pos()).Filename)
				}
			}
		} else if opts.EnableNonExistingErr {
			// If it's a Closure literal without a test param context, skip it.
			if ctx.IsLiteral() && ctx.TestParam == "" {
				continue
			}

			// Test Handler Handling (inject t.Fatal instead of signature change)
			if ctx.TestParam != "" {
				if err := handleTestErrorFn(p.Pkg, dstFile, p.Call, p.Stmt, ctx.TestParam); err == nil {
					totalChanges++
					mgr.MarkModified(p.File)
					opts.Reporter.IncHandled()
				}
				continue
			}

			// Main/Init Entry Point Handling
			if ctx.Decl != nil {
				if isEntryPointFn(p.Pkg.TypesInfo.ObjectOf(ctx.Decl.Name).(*types.Func)) {
					if err := handleEntryPointFn(p.Pkg, dstFile, p.Call, p.Stmt, opts.MainHandler); err == nil {
						totalChanges++
						mgr.MarkModified(p.File)
						opts.Reporter.IncHandled()
					}
					continue
				}
			}

			if ctx.Decl == nil {
				continue
			}

			fnObj := p.Pkg.TypesInfo.ObjectOf(ctx.Decl.Name).(*types.Func)
			conflicts, _ := checkComplianceFn(registry, fnObj)
			if len(conflicts) > 0 {
				applied, _ := logFallbackFn(injector, dstFile, p.File, p)
				if applied {
					totalChanges++
					mgr.MarkModified(p.File)
				}
				continue
			}

			changed, _ := addErrorToSignatureFn(p.Pkg.Fset, ctx.Decl)
			if changed {
				patchSignatureFn(p.Pkg.TypesInfo, ctx.Decl, fnObj.Pkg())
				res, _ := findDstNodeFn(mgr.fset, dstFile, p.File, ctx.Decl)
				if dstDecl, ok := res.Node.(*dst.FuncDecl); ok {
					_, _ = addErrorToSignatureDSTFn(dstDecl)
				}

				applied, err := rewriteFileFn(injector, dstFile, p.File, []analysis.InjectionPoint{p})
				if err != nil {
					return totalChanges, err
				}
				if applied {
					totalChanges++
					mgr.MarkModified(p.File)

					// Trigger recursive propagation
					newObj := p.Pkg.TypesInfo.ObjectOf(ctx.Decl.Name)
					propCount, err := propagateCallersFn([]*packages.Package{p.Pkg}, mgr, newObj, opts.MainHandler)
					if err != nil {
						return totalChanges, err
					}
					totalChanges += propCount
				}
			}
		}
	}

	if opts.PanicToReturn {
		for id, pkg := range mgr.pkgs {
			_ = id
			inj := rewrite.NewInjector(pkg, opts.ErrorTemplate, opts.MainHandler, opts.RetainPanics)
			inj.NonErrorFallback = opts.NonErrorFallback
			for _, f := range pkg.Syntax {
				dstFile, err := mgr.Get(pkg, f)
				if err != nil {
					continue
				}
				applied, err := rewritePanicsFn(inj, dstFile, f)
				if err != nil {
					log.Printf("Panic rewrite warning: %v", err)
				}
				if applied {
					totalChanges++
					mgr.MarkModified(f)
				}
			}
		}
	}

	return totalChanges, nil
}

func isThirdParty(p analysis.InjectionPoint) bool {
	if p.Pkg == nil || p.Pkg.TypesInfo == nil {
		return false
	}

	info := p.Pkg.TypesInfo
	var obj types.Object
	switch fun := p.Call.Fun.(type) {
	case *ast.Ident:
		obj = info.ObjectOf(fun)
	case *ast.SelectorExpr:
		obj = info.ObjectOf(fun.Sel)
	}

	if obj == nil || obj.Pkg() == nil || p.Pkg.Types == nil {
		return false
	}

	return obj.Pkg().Path() != p.Pkg.Types.Path()
}

func hasErrorReturn(sig *types.Signature) bool {
	if sig == nil {
		return false
	}
	res := sig.Results()
	if res.Len() == 0 {
		return false
	}
	last := res.At(res.Len() - 1)
	return isError(last.Type())
}

func isError(t types.Type) bool {
	return t.String() == "error" || t.String() == "builtin.error"
}

// formatAST formats the AST node and runs import processing.
// Use for tests requiring manual invocation of formatting logic.
func formatAST(fset *token.FileSet, node interface{}, filename string) ([]byte, error) {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, node); err != nil {
		return nil, err
	}
	// Use imports process to fix missing imports like "fmt"
	return imports.Process(filename, buf.Bytes(), nil)
}
