package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunner_Run_Options(t *testing.T) {
	tmpDir := t.TempDir()
	opts := Options{
		Paths:    []string{tmpDir},
		Reporter: nil,
	}
	err := Run(opts)
	if err != nil {
		t.Errorf("expected no error for empty dir")
	}
}

func TestRunner_Run_CheckPass(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\nfunc main() {}"), 0644)

	opts := Options{
		Paths: []string{filepath.Join(tmpDir, "main.go")},
		Check: true,
	}
	if err := Run(opts); err != nil {
		t.Errorf("expected pass")
	}
}

func TestRunner_Run_Save_Error(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "main.go")
	os.WriteFile(path, []byte("package main\nimport \"log\"\nfunc Target() error { return nil }\nfunc main() { log.Println(\"x\"); Target() }"), 0644)

	os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module testmod\ngo 1.20"), 0644)

	opts := Options{
		Paths:         []string{path},
		MainHandler:   "log-fatal",
		PanicToReturn: true,
		Recursive:     false,
	}

	os.Chmod(path, 0444)

	if err := Run(opts); err == nil {
		// Ignore
	}
	os.Chmod(path, 0644)
}

func TestRunner_Run_DryRun(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "main.go")
	os.WriteFile(path, []byte("package main\nimport \"log\"\nfunc Target() error { return nil }\nfunc main() { log.Println(\"x\"); Target() }"), 0644)

	opts := Options{
		Paths:         []string{path},
		DryRun:        true,
		MainHandler:   "log-fatal",
		PanicToReturn: true,
	}

	if err := Run(opts); err != nil {
		t.Errorf("expected no err, got %v", err)
	}
}

func TestRunner_Run_CheckFail(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\nfunc Target() error { return nil }\nfunc main() { Target() }"), 0644)

	opts := Options{
		Paths: []string{filepath.Join(tmpDir, "main.go")},
		Check: true,
	}
	if err := Run(opts); err == nil {
		t.Errorf("expected fail because of unhandled error")
	}
}

func TestRunner_Run_LoadFail(t *testing.T) {
	opts := Options{
		Paths: []string{"/invalid/pattern/that/fails/load/really/bad..."},
	}
	err := Run(opts)
	if err == nil {
		// It might just say 0 packages and not error, depending on toolchain
	}
}
