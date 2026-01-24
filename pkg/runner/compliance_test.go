package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_InterfaceCompliance checks if the runner correctly skips refactoring
// methods that satisfy an interface.
//
// It simulates a scenario where a struct 'Job' implements an interface 'Runner'.
// The 'Job.Run' method (which initially returns void) contains an unhandled error inside.
// Standard analysis would try to change 'Job.Run() error' to handle the inner error.
// Compliance logic should detect that changing the signature breaks 'Runner', and thus
// fall back to logging the error instead.
func TestRun_InterfaceCompliance(t *testing.T) {
	// 1. Setup Environment
	tmpDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module interface_check\ngo 1.22\n"), 0644)

	src := `package main

type Runner interface { 
  Run() 
} 

type Job struct{} 

// Run implements Runner. 
func (j *Job) Run() { 
  fail() 
} 

// fail is a helper that returns error, triggering detection. 
func fail() error { return nil } 

func main() { 
  var r Runner = &Job{} 
  r.Run() 
} 
`

	srcPath := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(srcPath, []byte(src), 0644); err != nil {
		t.Fatalf("failed to write source: %v", err)
	}

	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change dir: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	// 2. Configure Runner
	// Enable NonExistingErr (Level 1) to authorize signature changes.
	opts := Options{
		EnableNonExistingErr: true,
		Paths:                []string{"."},
		DryRun:               true, // dry-run to capture logic without disk churn
	}

	// 3. Execution
	out, err := captureRunLocally(t, opts)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// 4. Verification
	// The output should show a diff that adds logging but does NOT change the signature

	// Should NOT contain a signature change
	if strings.Contains(out, "func (j *Job) Run() error") {
		t.Error("Compliance failed: Job.Run signature was modified despite interface constraint")
	}

	// Should contain the logging injection
	if !strings.Contains(out, "if err := fail(); err != nil") {
		t.Error("Expected error handling injection for the fail() call")
	}

	if !strings.Contains(out, "log.Printf") && !strings.Contains(out, "ignored error") {
		t.Error("Expected log.Printf call for ignored error")
	}
}
