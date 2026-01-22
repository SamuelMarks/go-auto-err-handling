package main

// Config holds the complete configuration for the auto-error handling execution.
// It maps directly to command line flags provided by the user.
type Config struct {
	// LocalPreexistingErr toggles Level 0 analysis.
	// If true, it edits functions within the target directory that already return an error,
	// checking unhandled errors and returning them.
	LocalPreexistingErr bool `name:"local-preexisting-err" help:"Edit functions within a given directory tree adding checks and handling (incl. functions that return more than one variable) ensuring good defaults are chosen for the non error vars). Only edit functions that already have an err type in their return type."`

	// LocalNonExistingErr toggles Level 1 analysis.
	// If true, it performs Level 0 operations and also adds error return types to functions
	// that do not currently return an error, propagating these changes up the call stack.
	LocalNonExistingErr bool `name:"local-nonexisting-err" help:"Same as local-preexisting-err, but add err to functions that don't have err in their return type and percolate those changes up."`

	// ThirdPartyErr toggles Level 2 analysis.
	// If true, it performs Level 1 operations and also handles errors specifically from
	// third-party dependencies outside the local codebase.
	ThirdPartyErr bool `name:"third-party-err" help:"Same as local-nonexisting-err but also handle errors that aren't from this codebase."`

	// ExcludeGlob is a list of file glob patterns to exclude from analysis and modification.
	ExcludeGlob []string `name:"exclude-glob" help:"Glob patterns to exclude specific files or folders from changes and analysis."`

	// ExcludeSymbolGlob is a list of symbol glob patterns (e.g., 'fmt.*') to exclude from analysis.
	ExcludeSymbolGlob []string `name:"exclude-symbol-glob" help:"Glob patterns to exclude specific symbol / symbol hierarchy (e.g., 'fmt.*')."`

	// Paths indicates the directories or packages to analyze. Defaults to the current directory.
	Paths []string `arg:"" optional:"" help:"Directories or package paths to analyze." type:"path" default:"."`

	// DryRun enables preview mode.
	// If true, no files are written to disk. Instead, a diff of changes is printed to stdout.
	DryRun bool `name:"dry-run" help:"Print changes to stdout instead of rewriting files."`

	// NoDefaultExclusion disables the default list of ignored symbols.
	// By default, commonly ignored functions like fmt.Print* are excluded from analysis to reduce noise.
	// Setting this flag ensures all unhandled errors are reported/fixed.
	NoDefaultExclusion bool `name:"no-default-exclusion" help:"Disable the default exclusion list (e.g., fmt.Print*, strings.Builder.Write)."`

	// MainHandler specifies the strategy for handling errors in entry points (main/init).
	// Options: "log-fatal" (default), "os-exit", "panic".
	MainHandler string `name:"main-handler" help:"Strategy for handling errors in main/init functions. Options: 'log-fatal', 'os-exit', 'panic'." default:"log-fatal"`

	// ErrorTemplate specifies the wrapping template for return statements.
	// Placeholders: {return-zero} (zero values of other returns), {func_name} (called function), err (error var).
	// Default: "{return-zero}, err"
	ErrorTemplate string `name:"error-template" help:"Template for the return statement. Use {return-zero}, {func_name}, and err placeholders." default:"{return-zero}, err"`
}
