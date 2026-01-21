// Package main provides the entry point for the autoerr tool.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/kisielk/errcheck/errcheck"

	"github.com/SamuelMarks/go-auto-err-handling/internal/checker"
	"github.com/SamuelMarks/go-auto-err-handling/internal/inserter"
	"github.com/SamuelMarks/go-auto-err-handling/internal/loader"
)

// CLI represents the command-line interface for autoerr.
type CLI struct {
	Dir                 string   `arg:"" help:"Directory to process." default:"."`
	LocalPreexistingErr bool     `name:"local-preexisting-err" help:"Level 1: Insert checks for local functions already returning error."`
	LocalNonexistingErr bool     `name:"local-nonexisting-err" help:"Level 2: Insert checks for local functions, modifying signature and propagating if needed."`
	ThirdPartyErr       bool     `name:"third-party-err" help:"Level 3: Insert checks for third-party functions."`
	ExcludeGlob         []string `name:"exclude-glob" help:"Glob patterns for files/folders to exclude."`
	ExcludeSymbolGlob   []string `name:"exclude-symbol-glob" help:"Glob patterns for symbols to exclude (e.g. fmt.Println, fmt.*)."`
}

// Run executes the main logic of the application.
// It parses the CLI config, loads packages, runs the checker, filters errors, and applies changes.
// Returns an error if any step fails.
//
// cli: the parsed CLI configuration.
func Run(cli CLI) error {
	// Validate mutual exclusivity roughly (though they can technically run together in sequence, prompt implied separate levels)
	// Prompt says "With three levels...". Usually levels imply hierarchy or distinct modes.
	// We will treat them as additive flags or ensure they don't conflict logic-wise.
	// However, ProcessLevel2 covers Level 1 logic usually? No, Level 2 does nonexisting err.
	// Let's assume user picks one mode or multiple.
	// Based on prompts description "0... 1... 2...", they act like levels.
	// We will run them in order if multiple selected? Or just one?
	// The implementation functions are separated.
	
	count := 0
	if cli.LocalPreexistingErr { count++ }
	if cli.LocalNonexistingErr { count++ }
	if cli.ThirdPartyErr { count++ }
	if count == 0 {
		return fmt.Errorf("at least one level flag must be provided")
	}

	pkgs, err := loader.LoadPackages(cli.Dir)
	if err != nil {
		return fmt.Errorf("error loading packages: %w", err)
	}
	if len(pkgs) == 0 {
		return fmt.Errorf("no packages found in %s", cli.Dir)
	}

	chk := checker.SetupChecker(cli.ExcludeSymbolGlob)
	unchecked, err := checker.GetUncheckedErrors(cli.Dir, chk)
	if err != nil {
		// If errcheck fails (e.g. build error), report it.
		// errcheck returns error if it can't check.
		return fmt.Errorf("error checking packages: %w", err)
	}

	if len(unchecked) == 0 {
		fmt.Println("No unchecked errors found.")
		return nil
	}

	// Filter based on file globs
	absDir, err := filepath.Abs(cli.Dir)
	if err != nil {
		return err
	}
	
	var workList []errcheck.UncheckedError
	for _, u := range unchecked {
		// Filename in u.Pos is absolute usually from errcheck/packages
		rel, err := filepath.Rel(absDir, u.Pos.Filename)
		if err != nil {
			// Should be in src tree, keep it? Or skip if outside?
			// If outside module root, probably shouldn't edit.
			continue
		}
		
		excluded := false
		for _, g := range cli.ExcludeGlob {
			if match, _ := filepath.Match(g, rel); match {
				excluded = true
				break
			}
		}
		if !excluded {
			workList = append(workList, u)
		}
	}

	if len(workList) == 0 {
		fmt.Println("No unchecked errors found after exclusions.")
		return nil
	}

	var allModified []string
	
	// Apply levels
	// Note: Applying one might invalidate AST positions for others if done sequentially on same file without reload.
	// Our inserter uses `pkgs` which holds AST. Modifying AST affects `pkgs`.
	// If the functions modify the AST in place correctly, subsequent calls *might* work if offsets don't drift?
	// But `UncheckedError` relies on file offsets. Inserting code changes offsets.
	// Therefore, running multiple levels in one pass is risky without re-analysis.
	// For this tool, we will error if mutually exclusive or attempt best effort logic:
	// If multiple flags, we should prioritize?
	// Given constraints, better to allow only one "mode" or warn.
	// Re-reading prompt: "With three levels of aggressiveness".
	// Usually invalidates previous offsets.
	if count > 1 {
		return fmt.Errorf("please select only one level flag to avoid offset invalidation")
	}

	var modFiles []string
	if cli.LocalPreexistingErr {
		modFiles, err = inserter.ProcessLevel1(workList, pkgs)
	} else if cli.LocalNonexistingErr {
		modFiles, err = inserter.ProcessLevel2(workList, pkgs)
	} else if cli.ThirdPartyErr {
		modFiles, err = inserter.ProcessLevel3(workList, pkgs)
	}
	
	if err != nil {
		return fmt.Errorf("error processing files: %w", err)
	}
	
	allModified = append(allModified, modFiles...)

	if len(allModified) > 0 {
		fmt.Printf("Modified files:\n")
		for _, f := range removeDuplicates(allModified) {
			fmt.Printf("- %s\n", f)
		}
	} else {
		fmt.Println("No changes made.")
	}

	return nil
}

func removeDuplicates(s []string) []string {
	keys := make(map[string]bool)
	var list []string
	for _, entry := range s {
		if _, value := keys[entry]; !value {
			keys[entry] = true
			list = append(list, entry)
		}
	}
	return list
}

// main verifies the flags and calls Run.
func main() {
	var cli CLI
	ctx := kong.Parse(&cli)
	err := Run(cli)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		ctx.Exit(1)
	}
}