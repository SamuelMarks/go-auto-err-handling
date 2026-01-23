package analysis

import (
	"go/ast"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/filter"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/loader"
)

// TestDetect checks if the detector correctly identifies unhandled errors in a sample codebase.
func TestDetect(t *testing.T) {
	// 1. Setup temporary test module
	tmpDir := t.TempDir()

	// go.mod
	goMod := []byte("module example.com/testanalysis\n\ngo 1.22\n")
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), goMod, 0644); err != nil {
		t.Fatalf("failed to write go.mod: %v", err)
	}

	// source file with checked and unchecked errors
	src := []byte(`package main

import "fmt" 

func canFail() error { 
  return nil
} 

func main() { 
  // Ignored error (Expression Statement) 
  canFail() 

  // Ignored error (Blank Assignment) 
  _ = canFail() 

  // Checked error (Should NOT be detected) 
  err := canFail() 
  if err != nil { 
    fmt.Println(err) 
  } 

  // Ignored error from stdlib (filtered later) 
  fmt.Println("hello") 

  // Ignored error in Defer
  defer canFail() 
} 
`)
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), src, 0644); err != nil {
		t.Fatalf("failed to write main.go: %v", err)
	}

	// 2. Load the package
	pkgs, err := loader.LoadPackages([]string{"."}, tmpDir)
	if err != nil {
		t.Fatalf("loader failed: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("no packages loaded")
	}

	// 3. Run Analysis with a filter excluding ignoredFunc and fmt.Println
	// We specifically exclude fmt.Println
	src2 := []byte(`package main
func ignoredFunc() error { return nil } 
func testIgnored() { 
  ignoredFunc() // Should be ignored via filter
} 
`)
	if err := os.WriteFile(filepath.Join(tmpDir, "ignore.go"), src2, 0644); err != nil {
		t.Fatalf("failed to write ignore.go: %v", err)
	}

	// Reload packages to include new file
	pkgs, err = loader.LoadPackages([]string{"."}, tmpDir)
	if err != nil {
		t.Fatalf("loader failed reload: %v", err)
	}

	flt := filter.New(nil, []string{
		"example.com/testanalysis.ignoredFunc", // Exclude based on symbol name
		"fmt.Println",                          // Exclude fmt.Println (returns n, err)
	})

	points, err := Detect(pkgs, flt, false)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	// 4. Validate Results
	// We expect:
	// 1. canFail() in main.go (ExprStmt)
	// 2. _ = canFail() in main.go (AssignStmt)
	// 3. defer canFail() in main.go (DeferStmt)
	// ignoredFunc() should be filtered out.
	// fmt.Println should be filtered out.

	expectedCount := 3
	if len(points) != expectedCount {
		t.Errorf("expected %d injection points, got %d", expectedCount, len(points))
		for i, p := range points {
			t.Logf("Point %d: File=%s Line=%d Call=%s", i, p.Pkg.Fset.Position(p.Pos).Filename, p.Pkg.Fset.Position(p.Pos).Line, p.Call.Fun)
		}
	}

	// Verify specific types
	hasExprStmt := false
	hasAssignStmt := false
	hasDeferStmt := false

	for _, p := range points {
		if p.Stmt != nil {
			switch p.Stmt.(type) {
			case *ast.ExprStmt:
				hasExprStmt = true
			case *ast.DeferStmt:
				hasDeferStmt = true
			case *ast.AssignStmt:
				// Verify it's blank assignment inside
				if p.Assign != nil && len(p.Assign.Lhs) == 1 {
					if id, ok := p.Assign.Lhs[0].(*ast.Ident); ok && id.Name == "_" {
						hasAssignStmt = true
					}
				}
			}
		}
	}

	if !hasExprStmt {
		t.Error("Did not detect bare expression statement 'canFail()'")
	}
	if !hasAssignStmt {
		t.Error("Did not detect blank assignment '_ = canFail()'")
	}
	if !hasDeferStmt {
		t.Error("Did not detect defer statement 'defer canFail()'")
	}
}

// TestDetect_Empty checks behavior on clean code.
func TestDetect_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	src := []byte(`package main
func task() error { return nil } 
func main() { 
  if err := task(); err != nil { panic(err) } 
}`)
	_ = os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module valid\ngo 1.22\n"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "main.go"), src, 0644)

	pkgs, _ := loader.LoadPackages([]string{"."}, tmpDir)
	points, err := Detect(pkgs, nil, false)
	if err != nil {
		t.Fatalf("Detect error: %v", err)
	}
	if len(points) != 0 {
		t.Errorf("Expected 0 issues, got %d", len(points))
	}
}

// TestDetect_FilterFile checks file exclusion.
func TestDetect_FilterFile(t *testing.T) {
	tmpDir := t.TempDir()
	src := []byte(`package main
func fail() error { return nil } 
func main() { 
  fail() 
}`)
	_ = os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module filefilter\ngo 1.22\n"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "skip_me.go"), src, 0644)

	pkgs, _ := loader.LoadPackages([]string{"."}, tmpDir)

	// Filter skipping "skip_me.go"
	flt := filter.New([]string{"skip_me.go"}, nil)

	points, err := Detect(pkgs, flt, true)
	if err != nil {
		t.Fatalf("Detect error: %v", err)
	}
	if len(points) != 0 {
		t.Errorf("Expected file to be skipped, got %d points", len(points))
	}
}

// TestDetect_ResolvesSymbols verifies that getCalledFunction resolves methods and filters them correctly.
func TestDetect_ResolvesSymbols(t *testing.T) {
	// Simple unit test for internal helpers by integration
	// We want to ensure that method calls are also resolved.
	tmpDir := t.TempDir()
	src := []byte(`package main
type S struct{} 
func (s *S) Fail() error { return nil } 
func main() { 
  s := &S{} 
  s.Fail() 
}`)
	_ = os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module methods\ngo 1.22\n"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "main.go"), src, 0644)

	pkgs, _ := loader.LoadPackages([]string{"."}, tmpDir)

	// Filter matches *S.Fail.
	flt := filter.New(nil, []string{"methods.Fail"})

	points, err := Detect(pkgs, flt, false)
	if err != nil {
		t.Fatalf("Detect error: %v", err)
	}

	// Should be 0 if filtered
	if len(points) != 0 {
		t.Errorf("Expected method call to be filtered, got %d points", len(points))

		// Let's verify what symbol name we see if we remove filter
		pointsUnfiltered, _ := Detect(pkgs, nil, false)
		if len(pointsUnfiltered) > 0 {
			info := pointsUnfiltered[0].Pkg.TypesInfo
			call := pointsUnfiltered[0].Call
			fn := getCalledFunction(info, call)
			if fn != nil && fn.Pkg() != nil {
				t.Logf("Detected Symbol: %s.%s", fn.Pkg().Path(), fn.Name())
			}
		}
	}
}

// TestDetect_LocalFuncVariable verifies that function variables (closures) are uniquely identified
// and can be filtered by name.
func TestDetect_LocalFuncVariable(t *testing.T) {
	tmpDir := t.TempDir()
	src := []byte(`package main
func main() { 
  // Define a local function variable
  localFail := func() error { return nil } 
  
  // Call it - usually returns error
  localFail() 
} 
`)
	_ = os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module localvar\ngo 1.22\n"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "main.go"), src, 0644)

	pkgs, _ := loader.LoadPackages([]string{"."}, tmpDir)

	// Filter by local variable name "localFail".
	// Since it's in package "main", the symbol resolution should be "main.localFail".
	flt := filter.New(nil, []string{"localvar.localFail"})

	points, err := Detect(pkgs, flt, false)
	if err != nil {
		t.Fatalf("Detect error: %v", err)
	}

	if len(points) != 0 {
		t.Errorf("Expected local variable call to be filtered, got %d points", len(points))
		// Debug info
		if len(points) > 0 {
			info := points[0].Pkg.TypesInfo
			call := points[0].Call
			fn := getCalledFunction(info, call)
			if fn != nil && fn.Pkg() != nil {
				t.Logf("Detected Symbol: %s.%s", fn.Pkg().Path(), fn.Name())
			}
		}
	}
}

// TestDetect_Directives verifies that the "// auto-err:ignore" comment excludes calls from detection.
func TestDetect_Directives(t *testing.T) {
	tmpDir := t.TempDir()
	src := []byte(`package main
func fail() error { return nil } 
func main() { 
  // Case 1: Expression statement with ignore
  fail() // auto-err:ignore

  // Case 2: Assignment with ignore
  _ = fail() // auto-err:ignore

  // Case 3: Defer with ignore (block comment placement usually binds to stmt) 
  // auto-err:ignore
  defer fail() 

  // Case 4: Unhandled, should be detected
  fail() 
} 
`)
	_ = os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module directives\ngo 1.22\n"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "main.go"), src, 0644)

	pkgs, _ := loader.LoadPackages([]string{"."}, tmpDir)

	points, err := Detect(pkgs, nil, false)
	if err != nil {
		t.Fatalf("Detect error: %v", err)
	}

	// Expect exactly 1 point (Case 4)
	if len(points) != 1 {
		t.Errorf("Expected 1 detection (Case 4), got %d", len(points))
		for _, p := range points {
			t.Logf("Detected: Line %d %s", p.Pkg.Fset.Position(p.Pos).Line, p.Call.Fun)
		}
	}
}

// Verify ordering of points (mostly for stable testing)
type byPos []InjectionPoint

func (a byPos) Len() int           { return len(a) }
func (a byPos) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a byPos) Less(i, j int) bool { return a[i].Pos < a[j].Pos }

func sortPoints(p []InjectionPoint) {
	sort.Sort(byPos(p))
}
