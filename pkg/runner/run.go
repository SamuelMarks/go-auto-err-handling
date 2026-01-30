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
	"github.com/SamuelMarks/go-auto-err-handling/pkg/loader"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/refactor"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/report"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/rewrite"
	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
	"golang.org/x/tools/go/ast/astutil"
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
	Paths                []string
	MainHandler          string
	ErrorTemplate        string
	Reporter             *report.Reporter
}

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

		pkgs, err := loader.LoadPackages(opts.Paths, ".")
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

		registry := analysis.NewInterfaceRegistry(pkgs)

		points, err := analysis.Detect(pkgs, flt, opts.DryRun)
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

		mgr := newDstManager(pkgs)
		count, err := applyRefactors(mgr, points, opts, registry)
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

func (m *dstManager) Get(pkg *packages.Package, astFile *ast.File) (*dst.File, error) {
	tokFile := m.fset.File(astFile.Pos())
	if tokFile == nil {
		return nil, fmt.Errorf("file not found in fset")
	}
	name := tokFile.Name()

	if d, ok := m.cache[name]; ok {
		return d, nil
	}

	dec := decorator.NewDecorator(m.fset)
	d, err := dec.DecorateFile(astFile)
	if err != nil {
		return nil, err
	}
	m.cache[name] = d
	return d, nil
}

func (m *dstManager) MarkModified(astFile *ast.File) {
	tokFile := m.fset.File(astFile.Pos())
	if tokFile != nil {
		m.modified[tokFile.Name()] = true
	}
}

func (m *dstManager) PrintDiffs(w *os.File) error {
	paths := make([]string, 0, len(m.modified))
	for k := range m.modified {
		paths = append(paths, k)
	}
	sort.Strings(paths)

	for _, path := range paths {
		orig, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		d := m.cache[path]
		var buf bytes.Buffer
		if err := decorator.NewRestorer().Fprint(&buf, d); err != nil {
			return err
		}

		edits := myers.ComputeEdits(span.URIFromPath(path), string(orig), buf.String())
		unified := gotextdiff.ToUnified(path, path, string(orig), edits)
		fmt.Fprint(w, unified)
	}
	return nil
}

func (m *dstManager) Save() error {
	for path := range m.modified {
		d := m.cache[path]
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := decorator.NewRestorer().Fprint(f, d); err != nil {
			return err
		}
	}
	return nil
}

func applyRefactors(mgr *dstManager, points []analysis.InjectionPoint, opts Options, registry *analysis.InterfaceRegistry) (int, error) {
	totalChanges := 0

	propQueue := make([]*types.Func, 0)
	visited := make(map[*types.Func]bool)

	for _, p := range points {
		if !opts.EnableThirdPartyErr && isThirdParty(p) {
			continue
		}

		dstFile, err := mgr.Get(p.Pkg, p.File)
		if err != nil {
			return totalChanges, err
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

		hasErr := hasErrorReturn(ctx.Sig)
		injector := rewrite.NewInjector(p.Pkg, opts.ErrorTemplate, opts.MainHandler)

		if hasErr {
			if opts.EnablePreexistingErr {
				applied, err := injector.RewriteFile(dstFile, p.File, []analysis.InjectionPoint{p})
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
			if ctx.Decl == nil || ctx.IsLiteral() {
				continue
			}
			if filter.IsTestHandler(ctx.Decl) {
				continue
			}
			if refactor.IsEntryPoint(p.Pkg.TypesInfo.ObjectOf(ctx.Decl.Name).(*types.Func)) {
				if err := refactor.HandleEntryPoint(p.Pkg, dstFile, p.Call, p.Stmt, opts.MainHandler); err == nil {
					totalChanges++
					mgr.MarkModified(p.File)
					opts.Reporter.IncHandled()
				}
				continue
			}

			fnObj := p.Pkg.TypesInfo.ObjectOf(ctx.Decl.Name).(*types.Func)
			conflicts, _ := registry.CheckCompliance(fnObj)
			if len(conflicts) > 0 {
				applied, _ := injector.LogFallback(dstFile, p.File, p)
				if applied {
					totalChanges++
					mgr.MarkModified(p.File)
				}
				continue
			}

			changed, _ := refactor.AddErrorToSignature(p.Pkg.Fset, ctx.Decl)
			if changed {
				refactor.PatchSignature(p.Pkg.TypesInfo, ctx.Decl, fnObj.Pkg())
				res, _ := rewrite.FindDstNode(mgr.fset, dstFile, p.File, ctx.Decl)
				if dstDecl, ok := res.Node.(*dst.FuncDecl); ok {
					refactor.AddErrorToSignatureDST(dstDecl)
				}

				applied, err := injector.RewriteFile(dstFile, p.File, []analysis.InjectionPoint{p})
				if err != nil {
					return totalChanges, err
				}
				if applied {
					totalChanges++
					mgr.MarkModified(p.File)

					newObj := p.Pkg.TypesInfo.ObjectOf(ctx.Decl.Name).(*types.Func)
					if !visited[newObj] {
						visited[newObj] = true
						propQueue = append(propQueue, newObj)
					}
				}
			}
		}
	}

	if opts.PanicToReturn {
		for id, pkg := range mgr.pkgs {
			_ = id
			inj := rewrite.NewInjector(pkg, opts.ErrorTemplate, opts.MainHandler)
			for _, f := range pkg.Syntax {
				dstFile, err := mgr.Get(pkg, f)
				if err != nil {
					continue
				}
				applied, err := inj.RewritePanics(dstFile, f)
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

	for len(propQueue) > 0 {
		target := propQueue[0]
		propQueue = propQueue[1:]

		for _, pkg := range mgr.pkgs {
			for id, obj := range pkg.TypesInfo.Uses {
				if obj == target {
					f := findFileInPkg(pkg, id.Pos())
					if f == nil {
						continue
					}
					dstFile, _ := mgr.Get(pkg, f)

					ctx := FindEnclosingFunc(pkg, f, id.Pos())
					if ctx == nil {
						continue
					}

					path, _ := astutil.PathEnclosingInterval(f, id.Pos(), id.Pos())
					var call *ast.CallExpr
					var stmt ast.Stmt
					var assign *ast.AssignStmt

					for _, n := range path {
						if c, ok := n.(*ast.CallExpr); ok && call == nil {
							call = c
						}
						if s, ok := n.(ast.Stmt); ok && call != nil {
							stmt = s
							if a, ok := n.(*ast.AssignStmt); ok {
								assign = a
							}
							break
						}
					}

					if call == nil || stmt == nil {
						continue
					}

					point := analysis.InjectionPoint{
						Pkg: pkg, File: f, Call: call, Stmt: stmt, Assign: assign, Pos: call.Pos(),
					}

					isTerm := false
					if ctx.Decl != nil {
						fnObj := pkg.TypesInfo.ObjectOf(ctx.Decl.Name).(*types.Func)
						if refactor.IsEntryPoint(fnObj) || filter.IsTestHandler(ctx.Decl) {
							isTerm = true
						}
					}

					inj := rewrite.NewInjector(pkg, opts.ErrorTemplate, opts.MainHandler)

					if isTerm {
						refactor.HandleEntryPoint(pkg, dstFile, call, stmt, opts.MainHandler)
						mgr.MarkModified(f)
						totalChanges++
						continue
					}

					if ctx.Decl != nil && !hasErrorReturn(ctx.Sig) {
						refactor.AddErrorToSignature(pkg.Fset, ctx.Decl)
						refactor.PatchSignature(pkg.TypesInfo, ctx.Decl, pkg.Types)

						res, _ := rewrite.FindDstNode(mgr.fset, dstFile, f, ctx.Decl)
						if dstDecl, ok := res.Node.(*dst.FuncDecl); ok {
							refactor.AddErrorToSignatureDST(dstDecl)
						}

						newObj := pkg.TypesInfo.ObjectOf(ctx.Decl.Name).(*types.Func)
						if !visited[newObj] {
							visited[newObj] = true
							propQueue = append(propQueue, newObj)
						}
					}

					applied, err := inj.RewriteFile(dstFile, f, []analysis.InjectionPoint{point})
					if err == nil && applied {
						mgr.MarkModified(f)
						totalChanges++
					}
				}
			}
		}
	}

	return totalChanges, nil
}

func isThirdParty(p analysis.InjectionPoint) bool {
	info := p.Pkg.TypesInfo
	var obj types.Object
	if id, ok := p.Call.Fun.(*ast.Ident); ok {
		obj = info.ObjectOf(id)
	} else if sel, ok := p.Call.Fun.(*ast.SelectorExpr); ok {
		obj = info.ObjectOf(sel.Sel)
	}

	if obj != nil && obj.Pkg() != nil {
		return false
	}
	return false
}

func findFileInPkg(pkg *packages.Package, pos token.Pos) *ast.File {
	for _, f := range pkg.Syntax {
		if f.Pos() <= pos && pos < f.End() {
			return f
		}
	}
	return nil
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
