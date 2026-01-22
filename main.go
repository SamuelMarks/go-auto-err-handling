package main

import (
	"io"
	"log"
	"os"

	"github.com/alecthomas/kong"
)

// main is the entry point for the application.
// It delegates execution to run() and exits with a fatal error if execution fails.
func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// run initializes the configuration parser, parses the arguments, and sets up logging.
// It serves as the testable entry point for the application.
//
// args: The command line arguments (excluding the executable name).
// stdout: The writer to use for regular output (logging goes to standard log).
func run(args []string, stdout io.Writer) error {
	var cfg Config

	// Initialize Kong parser with the Config struct
	parser, err := kong.New(&cfg,
		kong.Name("go-auto-err-handling"),
		kong.Description("A tool to automatically inject error handling into Go code."),
		kong.Writers(stdout, io.Discard), // Redirect stdout, suppress stderr for clean tests unless needed
		kong.Exit(func(int) {}),          // Prevent os.Exit during tests
	)
	if err != nil {
		return err
	}

	// Parse the provided arguments
	_, err = parser.Parse(args)
	if err != nil {
		return err
	}

	// Setup initial logging based on configuration (could be expanded later)
	log.SetOutput(stdout)
	log.Printf("Starting analysis on paths: %v", cfg.Paths)
	log.Printf("Mode: Preexisting=%v, NonExisting=%v, ThirdParty=%v",
		cfg.LocalPreexistingErr, cfg.LocalNonExistingErr, cfg.ThirdPartyErr)

	// Future: Invoke the runner.Run(cfg) here
	return nil
}
