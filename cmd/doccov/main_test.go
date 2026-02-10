package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

type errWriter struct{}

func (errWriter) Write(p []byte) (int, error) {
	return 0, errors.New("write failed")
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/test\n\ngo 1.22\n")
	for rel, contents := range files {
		writeFile(t, filepath.Join(dir, rel), contents)
	}
	return dir
}

func withPackagesLoadStub(t *testing.T, stub func(*packages.Config, ...string) ([]*packages.Package, error), fn func()) {
	t.Helper()
	prev := packagesLoad
	packagesLoad = stub
	t.Cleanup(func() { packagesLoad = prev })
	fn()
}

func TestMain_UsesExitCode(t *testing.T) {
	moduleDir := writeModule(t, map[string]string{
		"pkgdoc/doc.go": "// Package pkgdoc docs.\npackage pkgdoc\n\n// Foo docs.\nfunc Foo() {}\n",
	})

	prevArgs := os.Args
	os.Args = []string{"doccov", "-format", "percent", "-min", "0", "-dir", moduleDir}
	t.Cleanup(func() { os.Args = prevArgs })

	var gotCode int
	prevExit := osExit
	osExit = func(code int) { gotCode = code }
	t.Cleanup(func() { osExit = prevExit })

	main()

	if gotCode != 0 {
		t.Fatalf("expected exit 0, got %d", gotCode)
	}
}

func TestRun_ParseError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-unknown-flag"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected code 2, got %d", code)
	}
	if stderr.Len() == 0 {
		t.Fatalf("expected stderr output")
	}
}

func TestRun_ComputeError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-dir", filepath.Join(t.TempDir(), "missing")}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}
	if stderr.Len() == 0 {
		t.Fatalf("expected stderr output")
	}
}

func TestRun_UnknownFormat(t *testing.T) {
	moduleDir := writeModule(t, map[string]string{
		"pkgdoc/doc.go": "// Package pkgdoc docs.\npackage pkgdoc\n\n// Foo docs.\nfunc Foo() {}\n",
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{"-dir", moduleDir, "-format", "nope"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected code 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "unknown format") {
		t.Fatalf("expected unknown format message")
	}
}

func TestRun_JSONEncodeError(t *testing.T) {
	moduleDir := writeModule(t, map[string]string{
		"pkgdoc/doc.go": "// Package pkgdoc docs.\npackage pkgdoc\n\n// Foo docs.\nfunc Foo() {}\n",
	})

	var stderr bytes.Buffer
	code := run([]string{"-dir", moduleDir, "-format", "json"}, errWriter{}, &stderr)
	if code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}
	if stderr.Len() == 0 {
		t.Fatalf("expected stderr output")
	}
}

func TestRun_SummaryAndPercent(t *testing.T) {
	moduleDir := writeModule(t, map[string]string{
		"pkgdoc/doc.go":  "// Package pkgdoc docs.\npackage pkgdoc\n\n// Foo docs.\nfunc Foo() {}\n",
		"pkgdoc/vars.go": "package pkgdoc\n\n// Bar docs.\nvar Bar = 1\n",
	})

	var summaryOut, summaryErr bytes.Buffer
	code := run([]string{"-dir", moduleDir, "-format", "summary"}, &summaryOut, &summaryErr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	if !strings.Contains(summaryOut.String(), "doc coverage") {
		t.Fatalf("expected summary output")
	}

	var percentOut, percentErr bytes.Buffer
	code = run([]string{"-dir", moduleDir, "-format", "percent"}, &percentOut, &percentErr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	if strings.TrimSpace(percentOut.String()) != "100.0" {
		t.Fatalf("expected 100.0, got %q", strings.TrimSpace(percentOut.String()))
	}
}

func TestRun_JSON(t *testing.T) {
	moduleDir := writeModule(t, map[string]string{
		"pkgdoc/doc.go":  "// Package pkgdoc docs.\npackage pkgdoc\n\n// Foo docs.\nfunc Foo() {}\n",
		"pkgdoc/vars.go": "package pkgdoc\n\n// Bar docs.\nvar Bar = 1\n",
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{"-dir", moduleDir, "-format", "json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}

	var stats coverageStats
	if err := json.Unmarshal(stdout.Bytes(), &stats); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if stats.Total != 3 || stats.Documented != 3 {
		t.Fatalf("expected 3/3, got %d/%d", stats.Documented, stats.Total)
	}
}

func TestRun_MinThreshold(t *testing.T) {
	moduleDir := writeModule(t, map[string]string{
		"pkgmissing/missing.go": "package pkgmissing\n\nfunc Foo() {}\n",
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{"-dir", moduleDir, "-format", "percent"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "missing docs") {
		t.Fatalf("expected missing docs output")
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"-dir", moduleDir, "-format", "percent", "-min", "0"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	if strings.TrimSpace(stdout.String()) != "0.0" {
		t.Fatalf("expected 0.0, got %q", strings.TrimSpace(stdout.String()))
	}
}

func TestComputeCoverage_AllDocumented(t *testing.T) {
	moduleDir := writeModule(t, map[string]string{
		"pkgdoc/doc.go":  "// Package pkgdoc docs.\npackage pkgdoc\n\n// Foo docs.\nfunc Foo() {}\n",
		"pkgdoc/vars.go": "package pkgdoc\n\n// Bar docs.\nvar Bar = 1\n",
	})

	stats, missing, err := computeCoverage(moduleDir)
	if err != nil {
		t.Fatalf("computeCoverage error: %v", err)
	}
	if stats.Total != 3 || stats.Documented != 3 {
		t.Fatalf("expected 3/3, got %d/%d", stats.Documented, stats.Total)
	}
	if len(missing) != 0 {
		t.Fatalf("expected no missing, got %d", len(missing))
	}
}

func TestComputeCoverage_MissingDocs(t *testing.T) {
	moduleDir := writeModule(t, map[string]string{
		"pkgmissing/missing.go": "package pkgmissing\n\nimport \"fmt\"\n\nconst Answer = 42\nvar Value = 1\ntype Thing struct{}\n\nfunc Do() {}\n\nfunc (Thing) Method() { fmt.Println() }\n",
	})

	stats, missing, err := computeCoverage(moduleDir)
	if err != nil {
		t.Fatalf("computeCoverage error: %v", err)
	}
	if stats.Total != 6 || stats.Documented != 0 {
		t.Fatalf("expected 0/6, got %d/%d", stats.Documented, stats.Total)
	}
	if len(missing) != 6 {
		t.Fatalf("expected 6 missing, got %d", len(missing))
	}
}

func TestComputeCoverage_LoadErrors(t *testing.T) {
	withPackagesLoadStub(t, func(cfg *packages.Config, patterns ...string) ([]*packages.Package, error) {
		return []*packages.Package{{Errors: []packages.Error{{Msg: "boom"}}}}, nil
	}, func() {
		_, _, err := computeCoverage(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("expected load error")
		}
	})
}

func TestComputeCoverage_NoPackages(t *testing.T) {
	withPackagesLoadStub(t, func(cfg *packages.Config, patterns ...string) ([]*packages.Package, error) {
		return []*packages.Package{}, nil
	}, func() {
		_, _, err := computeCoverage(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "no packages found") {
			t.Fatalf("expected no packages error")
		}
	})
}

func TestComputeCoverage_EmptySyntax(t *testing.T) {
	withPackagesLoadStub(t, func(cfg *packages.Config, patterns ...string) ([]*packages.Package, error) {
		return []*packages.Package{{Syntax: nil}}, nil
	}, func() {
		stats, missing, err := computeCoverage(t.TempDir())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stats.Total != 0 || len(missing) != 0 {
			t.Fatalf("expected empty stats")
		}
	})
}

func TestComputeCoverage_ParseErrors(t *testing.T) {
	moduleDir := writeModule(t, map[string]string{
		"badpkg/bad.go": "package badpkg\n\nfunc Broken( {\n",
	})
	badFile := filepath.Join(moduleDir, "badpkg", "bad.go")

	withPackagesLoadStub(t, func(cfg *packages.Config, patterns ...string) ([]*packages.Package, error) {
		return []*packages.Package{{GoFiles: []string{badFile}}}, nil
	}, func() {
		_, _, err := computeCoverage(moduleDir)
		if err == nil {
			t.Fatalf("expected parse error")
		}
		if !strings.Contains(err.Error(), "package load errors") {
			t.Fatalf("expected load errors prefix")
		}
		if !strings.Contains(err.Error(), "bad.go") {
			t.Fatalf("expected file name in error")
		}
	})
}

func TestComputeCoverage_DocumentedTypesAndSkips(t *testing.T) {
	moduleDir := writeModule(t, map[string]string{
		"pkgdoc/doc.go":   "// Package pkgdoc docs.\npackage pkgdoc\n",
		"pkgdoc/mixed.go": "package pkgdoc\n\nimport \"fmt\"\n\n// ExportedType docs.\ntype ExportedType struct{}\n\ntype unexportedType struct{}\n\ntype (\n\t// ExportedGroup docs.\n\tExportedGroup struct{}\n\tunexportedGroup struct{}\n)\n\n// ExportedConst docs.\nconst ExportedConst = 1\n\nconst (\n\t// ExportedConstGroup docs.\n\tExportedConstGroup = 2\n\tunexportedConst = 3\n)\n\n// ExportedVar docs.\nvar ExportedVar = 1\n\nvar (\n\t// ExportedVarGroup docs.\n\tExportedVarGroup = 2\n\tunexportedVar = 3\n)\n\n// ExportedFunc docs.\nfunc ExportedFunc() { fmt.Println() }\n\nfunc unexportedFunc() {}\n",
	})

	stats, missing, err := computeCoverage(moduleDir)
	if err != nil {
		t.Fatalf("computeCoverage error: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("expected no missing, got %d", len(missing))
	}
	if stats.Total == 0 || stats.Documented != stats.Total {
		t.Fatalf("expected full coverage, got %d/%d", stats.Documented, stats.Total)
	}
}

func TestHelpers(t *testing.T) {
	syntax, fset, errs := parsePackageSyntax(nil)
	if syntax != nil || fset != nil || len(errs) != 0 {
		t.Fatalf("expected nil parse results for nil package")
	}
	if percent(0, 0) != 100 {
		t.Fatalf("expected 100 for empty total")
	}
	if !hasDoc(&ast.CommentGroup{List: []*ast.Comment{{Text: "// hi"}}}) {
		t.Fatalf("expected doc true")
	}
	if hasDoc(nil) {
		t.Fatalf("expected doc false")
	}
	if hasDoc(&ast.CommentGroup{}) {
		t.Fatalf("expected empty doc false")
	}
	if funcKind(&ast.FuncDecl{Recv: nil}) != "func" {
		t.Fatalf("expected func kind")
	}
	if funcKind(&ast.FuncDecl{Recv: &ast.FieldList{}}) != "method" {
		t.Fatalf("expected method kind")
	}
	if valueKind(token.CONST) != "const" {
		t.Fatalf("expected const kind")
	}
	if valueKind(token.VAR) != "var" {
		t.Fatalf("expected var kind")
	}
	if fileForPos(nil, token.NoPos) != "" {
		t.Fatalf("expected empty filename")
	}
	if packageDocFile(nil) != "" {
		t.Fatalf("expected empty package doc file")
	}
	if packageDocFile(&packages.Package{}) != "" {
		t.Fatalf("expected empty package doc file for empty package")
	}
	if packageDocFile(&packages.Package{GoFiles: []string{"foo.go"}}) != "foo.go" {
		t.Fatalf("expected fallback go file")
	}
	var buf bytes.Buffer
	printMissing(&buf, []missingDoc{{Pkg: "p", Kind: "func", Name: "Foo"}}, "")
	if !strings.Contains(buf.String(), "<unknown>") {
		t.Fatalf("expected unknown file")
	}
	buf.Reset()
	root := t.TempDir()
	printMissing(&buf, []missingDoc{{Pkg: "p", Kind: "func", Name: "Foo", File: filepath.Join(root, "pkg", "file.go")}}, root)
	if strings.Contains(buf.String(), root) {
		t.Fatalf("expected relative path output")
	}
}

func TestPrintMissing_RelError(t *testing.T) {
	prev := filepathRel
	filepathRel = func(base, target string) (string, error) {
		return "", errors.New("rel error")
	}
	t.Cleanup(func() { filepathRel = prev })

	var buf bytes.Buffer
	printMissing(&buf, []missingDoc{{Pkg: "p", Kind: "func", Name: "Foo", File: "/tmp/file.go"}}, "/root")
	if !strings.Contains(buf.String(), "/tmp/file.go") {
		t.Fatalf("expected original file path on rel error")
	}
}

func TestMissingLess(t *testing.T) {
	if !missingLess(missingDoc{Pkg: "a"}, missingDoc{Pkg: "b"}) {
		t.Fatalf("expected pkg compare")
	}
	if !missingLess(missingDoc{Pkg: "a", File: "a.go"}, missingDoc{Pkg: "a", File: "b.go"}) {
		t.Fatalf("expected file compare")
	}
	if !missingLess(missingDoc{Pkg: "a", File: "a.go", Kind: "a"}, missingDoc{Pkg: "a", File: "a.go", Kind: "b"}) {
		t.Fatalf("expected kind compare")
	}
	if !missingLess(missingDoc{Pkg: "a", File: "a.go", Kind: "a", Name: "a"}, missingDoc{Pkg: "a", File: "a.go", Kind: "a", Name: "b"}) {
		t.Fatalf("expected name compare")
	}
	if missingLess(missingDoc{Pkg: "a", File: "a.go", Kind: "a", Name: "b"}, missingDoc{Pkg: "a", File: "a.go", Kind: "a", Name: "a"}) {
		t.Fatalf("expected name compare false")
	}
}
