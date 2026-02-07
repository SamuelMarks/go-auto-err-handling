package rewrite

import (
	"strings"
	"testing"
)

func TestRewriteDefers_DST(t *testing.T) {
	src := `package main

// Removed unused fmt import to pass strict type check in test logic

func Close() error { return nil }

// Anonymous signatures modified & normalized
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

	changed, err := injector.RewriteDefers(dstFile, astFile)
	if err != nil {
		t.Fatalf("RewriteDefers failed: %v", err)
	}
	if !changed {
		t.Fatal("Expected changes")
	}

	out := renderDstFile(t, dstFile)
	norm := normalizeStr(out)

	// Case 1: DoWork (Anonymous) -> Should be named and normalized
	// Expect: func DoWork() (i int, err error)
	// Expect: i, err = 1, nil; return
	if strings.Contains(out, "func DoWork() (int, error)") {
		t.Error("DoWork signature not updated")
	}
	if !strings.Contains(norm, "i int, err error") {
		t.Error("DoWork signature naming incorrect")
	}
	// Check for normalized assignment "i, err = 1, nil"
	if !strings.Contains(norm, "i, err = 1, nil") {
		t.Errorf("DoWork return normalization failed. Got: %s", norm)
	}

	// Case 2: DoNamed -> Rewritten (but returns not normalized as they were already named, unless tool force-applies it anyway check)
	// EnsureNamedReturnsDST returns false if already named, so normalizer is skipped.
	if !strings.Contains(norm, `defer func() { err = errors.Join(err, Close()) }()`) {
		t.Errorf("DoNamed not rewritten. Got:\n%s", out)
	}

	// Case 3: Closure -> Rewritten
	if !strings.Contains(norm, `func() (err error) { defer func() { err = errors.Join(err, Close()) }() return nil }`) {
		t.Errorf("Closure not rewritten. Got:\n%s", out)
	}
}

func TestRewriteDefers_Normalization(t *testing.T) {
	// Tests explicit -> naked normalization
	src := `package main
func Close() error { return nil }
// Explicit return should be normalized when named returns are forced
func NormalizeMe() error {
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

	// Signature should be named
	if !strings.Contains(norm, "func NormalizeMe() (err error)") {
		t.Error("Signature not named")
	}
	// Return should be normalized: err = nil; return
	if !strings.Contains(norm, "err = nil") {
		t.Error("Assignment missing from normalization")
	}
	if !strings.Contains(norm, "return }") { // Check naked return at end
		t.Error("Naked return missing")
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
