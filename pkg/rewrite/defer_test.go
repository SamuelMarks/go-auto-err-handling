package rewrite

import (
	"strings"
	"testing"
)

// TestRewriteDefers_DST validates defer rewriting logic.
// We split scenarios into sub-tests with fresh environments to ensure that mutations
// in one scenario (like signature changes or imports) do not desynchronize the AST/DST mapping
// for subsequent scenarios when they share the same source file.
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
	t.Run("Anonymous", func(t *testing.T) {
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

		// Expect DoWork: func DoWork() (i int, err error)
		if strings.Contains(out, "func DoWork() (int, error)") {
			t.Error("DoWork signature not updated")
		}
		if !strings.Contains(norm, "i int, err error") {
			t.Error("DoWork signature naming incorrect")
		}
		// Expect normalization: i, err = 1, nil; return
		if !strings.Contains(norm, "i, err = 1, nil") {
			t.Errorf("DoWork return normalization failed. Got: %s", norm)
		}
	})

	t.Run("Named", func(t *testing.T) {
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

		// Expect DoNamed rewrite
		if !strings.Contains(norm, `defer func() { err = errors.Join(err, Close()) }()`) {
			t.Errorf("DoNamed not rewritten. Got:\n%s", out)
		}
	})

	t.Run("Closure", func(t *testing.T) {
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

		// Expect Closure rewrite
		if !strings.Contains(norm, `func() (err error) { defer func() { err = errors.Join(err, Close()) }() return nil }`) {
			t.Errorf("Closure not rewritten. Got:\n%s", out)
		}
	})
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
