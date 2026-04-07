// Package main provides C bindings (FFI) for go-auto-err-handling functions.
package main

/*
#include <stdlib.h>
#include <stdbool.h>
*/
import "C"
import (
	"io"
	"log"
	"unsafe"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/runner"
)

// GoAutoErrAudit is the FFI entry point for checking a file for unhandled errors.
// It returns 0 on success (no issues) and 1 on failure (issues found or execution error).
//
//export GoAutoErrAudit
func GoAutoErrAudit(targetPath *C.char) C.int {
	path := C.GoString(targetPath)
	err := runTool(path, true, true)
	if err != nil {
		return 1 // Issues found or error occurred
	}
	return 0 // Success (no unhandled errors)
}

// GoAutoErrFix is the FFI entry point for automatically fixing unhandled errors in a file.
// It returns 0 on success and 1 on failure.
//
//export GoAutoErrFix
func GoAutoErrFix(targetPath *C.char, dryRun C.bool) C.int {
	path := C.GoString(targetPath)
	isDryRun := bool(dryRun)
	err := runTool(path, isDryRun, false)
	if err != nil {
		return 1
	}
	return 0
}

// runTool configures and executes the runner logic.
func runTool(path string, isDryRun, isCheck bool) error {
	// Suppress standard log output for FFI to avoid polluting bridle-ctl unless necessary.
	log.SetOutput(io.Discard)

	opts := runner.Options{
		EnablePreexistingErr: true,
		EnableNonExistingErr: true,
		EnableThirdPartyErr:  true,
		EnableTestRefactor:   true,
		Check:                isCheck,
		DryRun:               isDryRun,
		UseDefaultExclusions: true,
		Paths:                []string{path},
		MainHandler:          "log-fatal",
		NonErrorFallback:     "log",
		ErrorTemplate:        "{return-zero}, err",
		PanicToReturn:        false,
		RetainPanics:         false,
		Recursive:            false,
	}

	return runner.Run(opts)
}

func main() {}

// test helpers to bridge cgo calls for tests without needing cgo in the _test.go file.

// callGoAutoErrAudit is a test helper.
func callGoAutoErrAudit(path string) int {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	return int(GoAutoErrAudit(cPath))
}

// callGoAutoErrFix is a test helper.
func callGoAutoErrFix(path string, dryRun bool) int {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	return int(GoAutoErrFix(cPath, C.bool(dryRun)))
}
