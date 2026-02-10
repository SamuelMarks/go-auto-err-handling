package runner

import (
	"go/ast"
	"go/token"
	"go/types"
	"io"
	"os"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/analysis"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/loader"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/refactor"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/rewrite"
	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
)

// Test hooks (overridden in tests).
var (
	loadPackagesFn         = loader.LoadPackages
	detectFn               = analysis.Detect
	newInterfaceRegistryFn = analysis.NewInterfaceRegistry
	applyRefactorsFn       = applyRefactors
	newDstManagerFn        = newDstManager
	findEnclosingFuncFn    = FindEnclosingFunc
	rewriteFileFn          = func(inj *rewrite.Injector, dstFile *dst.File, astFile *ast.File, points []analysis.InjectionPoint) (bool, error) {
		return inj.RewriteFile(dstFile, astFile, points)
	}
	logFallbackFn = func(inj *rewrite.Injector, dstFile *dst.File, astFile *ast.File, p analysis.InjectionPoint) (bool, error) {
		return inj.LogFallback(dstFile, astFile, p)
	}
	rewritePanicsFn = func(inj *rewrite.Injector, dstFile *dst.File, astFile *ast.File) (bool, error) {
		return inj.RewritePanics(dstFile, astFile)
	}
	handleTestErrorFn        = refactor.HandleTestError
	isEntryPointFn           = refactor.IsEntryPoint
	handleEntryPointFn       = refactor.HandleEntryPoint
	addErrorToSignatureFn    = refactor.AddErrorToSignature
	patchSignatureFn         = refactor.PatchSignature
	findDstNodeFn            = rewrite.FindDstNode
	addErrorToSignatureDSTFn = refactor.AddErrorToSignatureDST
	propagateCallersFn       = refactor.PropagateCallers
	checkComplianceFn        = func(reg *analysis.InterfaceRegistry, fn *types.Func) ([]analysis.InterfaceConflict, error) {
		return reg.CheckCompliance(fn)
	}
	decorateFileFn = func(fset *token.FileSet, file *ast.File) (*dst.File, error) {
		dec := decorator.NewDecorator(fset)
		return dec.DecorateFile(file)
	}
	restorerFprintFn = func(w io.Writer, file *dst.File) error {
		return decorator.NewRestorer().Fprint(w, file)
	}
	readFileFn   = os.ReadFile
	createFileFn = os.Create
)
