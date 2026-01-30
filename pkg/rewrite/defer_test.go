package rewrite

import (
	"strings"
	"testing"
)

func TestRewriteDefers_DST(t *testing.T) {
	src := `package main

// Removed unused fmt import to pass strict type check in test logic

func Close() error { return nil }

// Anonymous signatures ignored
func DoWork() (int, error) {
	defer Close()
	return 1, nil
}

// Named signatures modified
func DoNamed() (i int, err error) {
	defer Close()
	return 1, nil
}

// Closures
func Top() {
	_ = func() (err error) {
		defer Close()
		return nil
	}
}
`
	injector, astFile, dstFile := setupDstEnv(t, src, false)

	// Mock valid type info for Close (which looks like error return)
	// setupDstEnv basic check might fail to resolve 'Close' return type without func body analysis in strict mode,
	// but our Injector logic relies on Pkg.TypesInfo.
	// Since 'Close' is in same package, it should resolve.

	changed, err := injector.RewriteDefers(dstFile, astFile)
	if err != nil {
		t.Fatalf("RewriteDefers failed: %v", err)
	}
	if !changed {
		t.Fatal("Expected changes")
	}

	out := renderDstFile(t, dstFile)
	norm := normalizeStr(out)

	// Case 1: DoWork (Anonymous) -> Ignored
	if strings.Contains(out, "func DoWork() (i int, err error)") {
		t.Error("DoWork should not be modified (anonymous return)")
	}

	// Case 2: DoNamed -> Rewritten
	if !strings.Contains(norm, `defer func() { err = errors.Join(err, Close()) }()`) {
		t.Errorf("DoNamed not rewritten. Got:\n%s", out)
	}

	// Case 3: Closure -> Rewritten
	if !strings.Contains(norm, `func() (err error) { defer func() { err = errors.Join(err, Close()) }() return nil }`) {
		t.Errorf("Closure not rewritten. Got:\n%s", out)
	}
}

func TestRewriteDefers_CustomName(t *testing.T) {
	src := `package main
func Close() error { return nil }
func Custom() (e error) {
	defer Close()
	return nil
}`
	injector, astFile, dstFile := setupDstEnv(t, src, false)
	changed, err := injector.RewriteDefers(dstFile, astFile)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("Expected change")
	}

	out := renderDstFile(t, dstFile)
	norm := normalizeStr(out)

	if !strings.Contains(norm, `e = errors.Join(e, Close())`) {
		t.Errorf("Did not use custom error name 'e'. Got:\n%s", out)
	}
}

func TestRewriteDefers_NilInputs(t *testing.T) {
	injector := &Injector{}
	_, err := injector.RewriteDefers(nil, nil)
	if err == nil {
		t.Error("Expected error for nil")
	}
}
