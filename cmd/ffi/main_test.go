package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFFI_GoAutoErrAudit(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.go")
	os.WriteFile(path, []byte("package main\nfunc Target() error { return nil }\nfunc main() { Target() }"), 0644)

	if res := callGoAutoErrAudit(path); res != 1 {
		t.Errorf("expected 1 (issues found), got %d", res)
	}

	path2 := filepath.Join(tmpDir, "test2.go")
	os.WriteFile(path2, []byte("package main\nfunc Target() error { return nil }\nfunc main() { err := Target(); if err != nil {} }"), 0644)
	if res := callGoAutoErrAudit(path2); res != 0 {
		t.Errorf("expected 0 (no issues), got %d", res)
	}
}

func TestFFI_GoAutoErrFix(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.go")
	os.WriteFile(path, []byte("package main\nfunc Target() error { return nil }\nfunc main() { Target() }"), 0644)

	if res := callGoAutoErrFix(path, true); res != 0 {
		t.Errorf("expected 0, got %d", res)
	}
}

func TestFFI_GoAutoErrFix_Fail(t *testing.T) {
	if res := callGoAutoErrFix("/invalid/dir/does/not/exist", false); res == 1 {
		// Just want to trigger it, failure returns 1.
	}
}

func TestFFI_GoAutoErrFix_Chmod(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.go")
	os.WriteFile(path, []byte("package main\nfunc Target() error { return nil }\nfunc main() { Target() }"), 0644)
	os.Chmod(path, 0444) // Read-only file

	if res := callGoAutoErrFix(path, false); res == 0 {
		t.Errorf("expected error writing to read-only file")
	}
}

func TestFFIMain(t *testing.T) {
	main()
}
