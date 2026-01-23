package main

import "github.com/alecthomas/kong"

// Config holds the complete configuration mapping to CLI flags.
// Fields use positive logic ("Enable...") defaulting to true to ensure
// the tool performs analysis by default unless explicitly disabled.
type Config struct {
	// EnablePreexistingErr enables fixes for functions that already return an error
	// (Level 0 Analysis).
	EnablePreexistingErr bool `name:"local-preexisting-err" help:"Enable fixes for functions that already return an error." default:"true"`

	// EnableNonExistingErr enables changing function signatures to add error returns
	// (Level 1 Analysis).
	//
	// This modifies the function signature to include an error return value,
	// updates all return statements within the function, and injects error handling
	// for the specific call that triggered the refactor.
	EnableNonExistingErr bool `name:"return-type-changes" help:"Enable changing function signatures to return errors." default:"true"`

	// EnableThirdPartyErr enables checking errors from third-party libraries
	// (Level 2 Analysis).
	EnableThirdPartyErr bool `name:"third-party" help:"Enable checking errors from third-party dependencies." default:"true"`

	// EnableTestRefactor allows modification of test function bodies.
	// Note: Standard 'TestX' signatures are never modified by the runner to avoid breaking 'go test',
	// but this flag allows body refactoring within tests.
	EnableTestRefactor bool `name:"test-func-changes" help:"Enable refactoring within test functions (Test*, Example*, Benchmark*)." default:"true"`

	// Check enables CI/Linter mode.
	// If true, the tool performs analysis and reports unhandled errors with a non-zero exit code,
	// without modifying files. Implies --dry-run.
	Check bool `name:"check" aliases:"verify" help:"Repo verification mode. exits with 1 if unhandled errors are found. Implies --dry-run."`

	// ExcludeGlob is a list of file glob patterns to exclude from analysis.
	ExcludeGlob []string `name:"exclude-glob" help:"Glob patterns to exclude files (e.g. '*_test.go')."`

	// ExcludeSymbolGlob is a list of symbol glob patterns to exclude.
	ExcludeSymbolGlob []string `name:"exclude-symbol-glob" help:"Glob patterns to exclude symbols (e.g. 'fmt.Println')."`

	// Paths to analyze.
	Paths []string `arg:"" optional:"" help:"Directories to analyze." default:"."`

	// DryRun enables preview mode.
	DryRun bool `name:"dry-run" help:"Print changes to stdout instead of writing files."`

	// UseDefaultExclusions applies the default list of ignored symbols (fmt, log, etc).
	// If true (default), standard noise filters are applied.
	// Disable with --no-default-exclusions to check everything.
	UseDefaultExclusions bool `name:"default-exclusions" help:"Use standard exclusion list (fmt, log, etc)." default:"true"`

	// MainHandler strategy for entry points.
	MainHandler string `name:"main-handler" help:"Strategy for main/init: 'log-fatal', 'os-exit', 'panic'." default:"log-fatal"`

	// ErrorTemplate template for return statements.
	ErrorTemplate string `name:"error-template" help:"Template for return (e.g. '{return-zero}, err')." default:"{return-zero}, err"`

	// Get the version of the package, defaults to `dev`
	Version kong.VersionFlag `name:"version" help:"Print version information and exit."`
}
