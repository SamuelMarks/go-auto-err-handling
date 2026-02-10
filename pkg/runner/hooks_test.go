package runner

import (
	"go/ast"
	"go/token"
	"go/types"
	"io"
	"os"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/analysis"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/filter"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/refactor"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/rewrite"
	"github.com/dave/dst"
	"golang.org/x/tools/go/packages"
)

type runnerHooks struct {
	loadPackagesFn           func([]string, string) ([]*packages.Package, error)
	detectFn                 func([]*packages.Package, *filter.Filter, bool) ([]analysis.InjectionPoint, error)
	newInterfaceRegistryFn   func([]*packages.Package) *analysis.InterfaceRegistry
	applyRefactorsFn         func(*dstManager, []analysis.InjectionPoint, Options, *analysis.InterfaceRegistry) (int, error)
	newDstManagerFn          func([]*packages.Package) *dstManager
	findEnclosingFuncFn      func(*packages.Package, *ast.File, token.Pos) *FuncContext
	rewriteFileFn            func(*rewrite.Injector, *dst.File, *ast.File, []analysis.InjectionPoint) (bool, error)
	logFallbackFn            func(*rewrite.Injector, *dst.File, *ast.File, analysis.InjectionPoint) (bool, error)
	rewritePanicsFn          func(*rewrite.Injector, *dst.File, *ast.File) (bool, error)
	handleTestErrorFn        func(*packages.Package, *dst.File, *ast.CallExpr, ast.Stmt, string) error
	isEntryPointFn           func(*types.Func) bool
	handleEntryPointFn       func(*packages.Package, *dst.File, *ast.CallExpr, ast.Stmt, string) error
	addErrorToSignatureFn    func(*token.FileSet, *ast.FuncDecl) (bool, error)
	patchSignatureFn         func(*types.Info, *ast.FuncDecl, *types.Package) error
	findDstNodeFn            func(*token.FileSet, *dst.File, *ast.File, ast.Node) (rewrite.DstMapResult, error)
	addErrorToSignatureDSTFn func(*dst.FuncDecl) (bool, error)
	propagateCallersFn       func([]*packages.Package, refactor.DstProvider, types.Object, string) (int, error)
	checkComplianceFn        func(*analysis.InterfaceRegistry, *types.Func) ([]analysis.InterfaceConflict, error)
	decorateFileFn           func(*token.FileSet, *ast.File) (*dst.File, error)
	restorerFprintFn         func(io.Writer, *dst.File) error
	readFileFn               func(string) ([]byte, error)
	createFileFn             func(string) (*os.File, error)
}

func saveRunnerHooks() runnerHooks {
	return runnerHooks{
		loadPackagesFn:           loadPackagesFn,
		detectFn:                 detectFn,
		newInterfaceRegistryFn:   newInterfaceRegistryFn,
		applyRefactorsFn:         applyRefactorsFn,
		newDstManagerFn:          newDstManagerFn,
		findEnclosingFuncFn:      findEnclosingFuncFn,
		rewriteFileFn:            rewriteFileFn,
		logFallbackFn:            logFallbackFn,
		rewritePanicsFn:          rewritePanicsFn,
		handleTestErrorFn:        handleTestErrorFn,
		isEntryPointFn:           isEntryPointFn,
		handleEntryPointFn:       handleEntryPointFn,
		addErrorToSignatureFn:    addErrorToSignatureFn,
		patchSignatureFn:         patchSignatureFn,
		findDstNodeFn:            findDstNodeFn,
		addErrorToSignatureDSTFn: addErrorToSignatureDSTFn,
		propagateCallersFn:       propagateCallersFn,
		checkComplianceFn:        checkComplianceFn,
		decorateFileFn:           decorateFileFn,
		restorerFprintFn:         restorerFprintFn,
		readFileFn:               readFileFn,
		createFileFn:             createFileFn,
	}
}

func (h runnerHooks) restore() {
	loadPackagesFn = h.loadPackagesFn
	detectFn = h.detectFn
	newInterfaceRegistryFn = h.newInterfaceRegistryFn
	applyRefactorsFn = h.applyRefactorsFn
	newDstManagerFn = h.newDstManagerFn
	findEnclosingFuncFn = h.findEnclosingFuncFn
	rewriteFileFn = h.rewriteFileFn
	logFallbackFn = h.logFallbackFn
	rewritePanicsFn = h.rewritePanicsFn
	handleTestErrorFn = h.handleTestErrorFn
	isEntryPointFn = h.isEntryPointFn
	handleEntryPointFn = h.handleEntryPointFn
	addErrorToSignatureFn = h.addErrorToSignatureFn
	patchSignatureFn = h.patchSignatureFn
	findDstNodeFn = h.findDstNodeFn
	addErrorToSignatureDSTFn = h.addErrorToSignatureDSTFn
	propagateCallersFn = h.propagateCallersFn
	checkComplianceFn = h.checkComplianceFn
	decorateFileFn = h.decorateFileFn
	restorerFprintFn = h.restorerFprintFn
	readFileFn = h.readFileFn
	createFileFn = h.createFileFn
}
