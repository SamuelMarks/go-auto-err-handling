package loader

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestLoadPackages_PackagesLoadError(t *testing.T) {
	orig := packagesLoad
	defer func() { packagesLoad = orig }()

	packagesLoad = func(_ *packages.Config, _ ...string) ([]*packages.Package, error) {
		return nil, fmt.Errorf("boom")
	}

	if _, err := LoadPackages([]string{"."}, "", false); err == nil {
		t.Fatal("expected error from packagesLoad")
	}
}

func TestLoadPackages_RecursiveError(t *testing.T) {
	orig := packagesLoad
	defer func() { packagesLoad = orig }()

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example.com/x\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	calls := 0
	packagesLoad = func(_ *packages.Config, _ ...string) ([]*packages.Package, error) {
		calls++
		if calls == 1 {
			return []*packages.Package{{Errors: []packages.Error{{Msg: "no Go files"}}}}, nil
		}
		return nil, fmt.Errorf("retry failed")
	}

	if _, err := LoadPackages([]string{"."}, tmpDir, false); err == nil {
		t.Fatal("expected error from recursive load")
	}
}

func TestLoadPackages_WarnsOnPackageErrors(t *testing.T) {
	orig := packagesLoad
	defer func() { packagesLoad = orig }()

	packagesLoad = func(_ *packages.Config, _ ...string) ([]*packages.Package, error) {
		return []*packages.Package{
			{PkgPath: "example.com/p", GoFiles: []string{"p.go"}, Errors: []packages.Error{{Msg: "typecheck failed"}}},
		}, nil
	}

	var buf strings.Builder
	origOutput := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(origOutput)

	pkgs, err := LoadPackages([]string{"./..."}, "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("expected 1 package, got %d", len(pkgs))
	}
	if buf.Len() == 0 {
		t.Fatal("expected warning log output")
	}
}

func TestLoadPackages_RecursiveSuccess(t *testing.T) {
	orig := packagesLoad
	defer func() { packagesLoad = orig }()

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example.com/x\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	var secondPatterns []string
	calls := 0
	packagesLoad = func(_ *packages.Config, patterns ...string) ([]*packages.Package, error) {
		calls++
		if calls == 1 {
			if !reflect.DeepEqual(patterns, []string{".", "./pkg"}) {
				t.Fatalf("unexpected initial patterns: %v", patterns)
			}
			return []*packages.Package{{Errors: []packages.Error{{Msg: "no Go files"}}}}, nil
		}
		secondPatterns = append([]string(nil), patterns...)
		return []*packages.Package{{PkgPath: "example.com/p", GoFiles: []string{"p.go"}}}, nil
	}

	var buf strings.Builder
	origOutput := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(origOutput)

	pkgs, err := LoadPackages([]string{".", "./pkg"}, tmpDir, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(secondPatterns, []string{"./...", "./pkg"}) {
		t.Fatalf("unexpected recursive patterns: %v", secondPatterns)
	}
	if len(pkgs) != 1 {
		t.Fatalf("expected 1 package, got %d", len(pkgs))
	}
}

func TestLoadPackages_StripsCoverDir(t *testing.T) {
	orig := packagesLoad
	defer func() { packagesLoad = orig }()

	if err := os.Setenv("GOCOVERDIR", t.TempDir()); err != nil {
		t.Fatalf("set env: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("GOCOVERDIR") })

	var gotEnv []string
	packagesLoad = func(cfg *packages.Config, _ ...string) ([]*packages.Package, error) {
		gotEnv = append([]string(nil), cfg.Env...)
		return []*packages.Package{{PkgPath: "example.com/p", GoFiles: []string{"p.go"}}}, nil
	}

	if _, err := LoadPackages([]string{"."}, "", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedCache := filepath.Join(os.TempDir(), "go-auto-err-handling-gocache")
	foundCache := false
	for _, entry := range gotEnv {
		if strings.HasPrefix(entry, "GOCOVERDIR=") {
			t.Fatalf("expected GOCOVERDIR to be stripped from env")
		}
		if entry == "GOCACHE="+expectedCache {
			foundCache = true
		}
	}
	if !foundCache {
		t.Fatalf("expected GOCACHE to be set to %s", expectedCache)
	}
}

func TestFilterEnv_NoKeys(t *testing.T) {
	env := []string{"A=1", "B=2"}
	got := filterEnv(env)
	if !reflect.DeepEqual(got, env) {
		t.Fatalf("expected env unchanged, got %v", got)
	}
}

func TestFilterEnv_DropsKeys(t *testing.T) {
	env := []string{"A=1", "GOCOVERDIR=/tmp/cover", "B=2", "NOEQUALS"}
	got := filterEnv(env, "GOCOVERDIR")
	if strings.Join(got, ",") == strings.Join(env, ",") {
		t.Fatalf("expected filtered env, got %v", got)
	}
	for _, entry := range got {
		if strings.HasPrefix(entry, "GOCOVERDIR=") {
			t.Fatalf("expected GOCOVERDIR to be removed")
		}
	}
	if !strings.Contains(strings.Join(got, ","), "NOEQUALS") {
		t.Fatalf("expected entry without '=' to be preserved")
	}
}

func TestShouldRetryRecursive_Cases(t *testing.T) {
	tmpDir := t.TempDir()
	goModPath := filepath.Join(tmpDir, "go.mod")
	if err := os.WriteFile(goModPath, []byte("module example.com/x\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	cases := []struct {
		name     string
		pkgs     []*packages.Package
		patterns []string
		want     bool
	}{
		{name: "NoDotPattern", pkgs: nil, patterns: []string{"./..."}, want: false},
		{name: "HasValidPackage", pkgs: []*packages.Package{{GoFiles: []string{"a.go"}}}, patterns: []string{"."}, want: false},
		{name: "NoFilesNoError", pkgs: []*packages.Package{{GoFiles: nil}}, patterns: []string{"."}, want: false},
		{name: "NoGoMod", pkgs: []*packages.Package{{GoFiles: nil, Errors: []packages.Error{{Msg: "no Go files"}}}}, patterns: []string{"."}, want: false},
		{name: "RetryTrue", pkgs: []*packages.Package{{GoFiles: nil, Errors: []packages.Error{{Msg: "no Go files"}}}}, patterns: []string{"."}, want: true},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			dir := tmpDir
			if tt.name == "NoGoMod" {
				dir = t.TempDir()
			}
			got := shouldRetryRecursive(tt.pkgs, tt.patterns, dir)
			if got != tt.want {
				t.Fatalf("shouldRetryRecursive()=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldRetryRecursive_EmptyDirUsesCwd(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example.com/x\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	pkgs := []*packages.Package{
		{GoFiles: nil, Errors: []packages.Error{{Msg: "no Go files"}}},
	}
	if got := shouldRetryRecursive(pkgs, []string{"."}, ""); !got {
		t.Fatal("expected retry when dir is empty and go.mod exists in cwd")
	}
}
