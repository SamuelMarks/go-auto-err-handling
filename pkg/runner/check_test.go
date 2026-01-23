package runner

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRun_CheckMode verifies that verification mode works effectively.
func TestRun_CheckMode(t *testing.T) {
	tmpDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module checktest\ngo 1.22\n"), 0644)

	// Source with unhandled error
	src := `package main
func fail() error { return nil } 
func main() { 
  fail() 
} 
`
	srcPath := filepath.Join(tmpDir, "main.go")
	_ = os.WriteFile(srcPath, []byte(src), 0644)

	oldWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer interface{}(func() { _ = os.Chdir(oldWd) }).(func())()

	// 1. Run in Check Mode -> Should Fail
	optsFail := Options{
		Check:                true,
		EnablePreexistingErr: true,
		EnableNonExistingErr: true,
		Paths:                []string{"."},
	}

	err := Run(optsFail)
	if err == nil {
		t.Error("Expected error in Check mode when issues exist, got nil")
	} else if err.Error() != "check failed: 1 unhandled errors found" {
		t.Errorf("Unexpected error message: %v", err)
	}

	// Verify file NOT modified
	content, _ := os.ReadFile(srcPath)
	if string(content) != src {
		t.Error("File was modified in Check mode")
	}

	// 2. Run in Standard Mode -> Should Fix
	optsFix := Options{
		EnablePreexistingErr: true,
		EnableNonExistingErr: true,
		Paths:                []string{"."},
	}
	if err := Run(optsFix); err != nil {
		t.Fatalf("Fix run failed: %v", err)
	}

	// 3. Run in Check Mode again -> Should Pass
	optsPass := Options{
		Check:                true,
		EnablePreexistingErr: true,
		EnableNonExistingErr: true,
		Paths:                []string{"."},
	}
	if err := Run(optsPass); err != nil {
		t.Errorf("Expected pass in Check mode after fix, got error: %v", err)
	}
}
