package main

import (
	"io"
	"log"
	"os"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/runner"
	"github.com/alecthomas/kong"
)

// version is set via linker flags during build (e.g., -ldflags "-X main.version=1.0.0")
var version = "dev"

// runFn and fatalf are package-level hooks to simplify testing main behavior.
var runFn = run
var fatalf = log.Fatal

// main is the CLI entry point.
// It executes the runner and handles fatal errors (including check failures) by exiting with status 1.
func main() {
	if err := runFn(os.Args[1:], os.Stdout); err != nil {
		fatalf(err)
	}
}

// run parses arguments and executes the analysis runner.
//
// args: Command line arguments.
// stdout: Writer for logs and output.
func run(args []string, stdout io.Writer) error {
	var cfg Config
	parser, err := kongNew(&cfg,
		kong.Name("auto-err"),
		kong.Description("Automatically inject error handling into Go code."),
		kong.Writers(stdout, io.Discard),
		// We removed kong.Exit(func(int) {}) here.
		// Use standard behavior (os.Exit) so --version and --help exit cleanly.
		kong.Vars{"version": version}, // Bind the version variable
	)
	if err != nil {
		return err
	}

	_, err = parser.Parse(args)
	if err != nil {
		return err
	}

	log.SetOutput(stdout)
	log.Printf("Starting analysis on paths: %v", cfg.Paths)

	// Map CLI Config to Library Options.
	opts := runner.Options{
		EnablePreexistingErr: cfg.EnablePreexistingErr,
		EnableNonExistingErr: cfg.EnableNonExistingErr,
		EnableThirdPartyErr:  cfg.EnableThirdPartyErr,
		EnableTestRefactor:   cfg.EnableTestRefactor,
		Check:                cfg.Check,
		ExcludeGlob:          cfg.ExcludeGlob,
		ExcludeSymbolGlob:    cfg.ExcludeSymbolGlob,
		DryRun:               cfg.DryRun,
		UseDefaultExclusions: cfg.UseDefaultExclusions,
		Paths:                cfg.Paths,
		MainHandler:          cfg.MainHandler,
		NonErrorFallback:     cfg.NonErrorFallback,
		ErrorTemplate:        cfg.ErrorTemplate,
	}

	// Log active modes.
	log.Printf("Active Levels: Preexisting=%v, ReturnTypeChanges=%v, ThirdParty=%v",
		opts.EnablePreexistingErr, opts.EnableNonExistingErr, opts.EnableThirdPartyErr)

	if opts.Check {
		log.Printf("Mode: CI Check (Verification)")
	}

	return runRunner(opts)
}

// runRunner is a test hook for swapping runner execution.
var runRunner = runner.Run

// kongNew is a test hook for swapping the Kong parser constructor.
var kongNew = kong.New
