package main

import (
	"io"
	"log"
	"os"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/runner"
	"github.com/alecthomas/kong"
)

// main is the CLI entry point.
// It executes the runner and handles fatal errors (including check failures) by exiting with status 1.
func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// run parses arguments and executes the analysis runner.
//
// args: Command line arguments.
// stdout: Writer for logs and output.
func run(args []string, stdout io.Writer) error {
	var cfg Config
	parser, err := kong.New(&cfg,
		kong.Name("auto-err"),
		kong.Description("Automatically inject error handling into Go code."),
		kong.Writers(stdout, io.Discard),
		kong.Exit(func(int) {}),
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
		ErrorTemplate:        cfg.ErrorTemplate,
	}

	// Log active modes.
	log.Printf("Active Levels: Preexisting=%v, ReturnTypeChanges=%v, ThirdParty=%v",
		opts.EnablePreexistingErr, opts.EnableNonExistingErr, opts.EnableThirdPartyErr)

	if opts.Check {
		log.Printf("Mode: CI Check (Verification)")
	}

	return runner.Run(opts)
}
