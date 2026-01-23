package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// TestReporter_Workflow verifies the full lifecycle of the reporter:
// initialization, accumulation, and JSON generation.
func TestReporter_Workflow(t *testing.T) {
	r := New()

	// Simulate activity
	r.AddFile("main.go")
	r.AddFile("pkg/utils.go")
	r.AddFile("main.go") // Duplicate, should be ignored

	r.IncHandled()
	r.IncHandled()
	r.IncHandled()

	r.IncSkipped()
	r.IncSkipped()

	// Verify internal state matches expectation via GetData
	data := r.GetData()
	if data.ErrorsHandled != 3 {
		t.Errorf("Expected 3 handled errors, got %d", data.ErrorsHandled)
	}
	if data.Skipped != 2 {
		t.Errorf("Expected 2 skipped, got %d", data.Skipped)
	}
	if len(data.FilesModified) != 2 {
		t.Errorf("Expected 2 unique files, got %d", len(data.FilesModified))
	}

	// Verify JSON Output
	var buf bytes.Buffer
	if err := r.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON failed: %v", err)
	}

	expectedParts := []string{
		`"files_modified": [`,
		`"main.go"`,
		`"pkg/utils.go"`,
		`"errors_handled": 3`,
		`"skipped": 2`,
	}

	out := buf.String()
	for _, part := range expectedParts {
		if !strings.Contains(out, part) {
			t.Errorf("JSON output missing part %q. Got:\n%s", part, out)
		}
	}

	// Verify valid JSON parsing
	var decoded Data
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("Failed to decode generated JSON: %v", err)
	}
	if len(decoded.FilesModified) != 2 {
		t.Error("Decoded JSON has bad file list length")
	}
}

// TestReporter_Concurrency checks strict thread safety for the reporter.
func TestReporter_Concurrency(t *testing.T) {
	r := New()
	var wg sync.WaitGroup

	// Spawn multiple goroutines accessing the reporter
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			if id%2 == 0 {
				r.IncHandled()
			} else {
				r.IncSkipped()
			}
			r.AddFile("concurrent.go")
		}(i)
	}

	wg.Wait()

	data := r.GetData()
	if data.ErrorsHandled != 50 {
		t.Errorf("Expected 50 handled, got %d", data.ErrorsHandled)
	}
	if data.Skipped != 50 {
		t.Errorf("Expected 50 skipped, got %d", data.Skipped)
	}
	if len(data.FilesModified) != 1 {
		t.Errorf("Expected 1 unique file, got %d", len(data.FilesModified))
	}
}

// TestReporter_Sorting verifies that output files are sorted alphabetically.
func TestReporter_Sorting(t *testing.T) {
	r := New()
	r.AddFile("b.go")
	r.AddFile("a.go")
	r.AddFile("c.go")

	data := r.GetData()
	if data.FilesModified[0] != "a.go" || data.FilesModified[1] != "b.go" || data.FilesModified[2] != "c.go" {
		t.Errorf("Files not sorted: %v", data.FilesModified)
	}
}

// TestReporter_Empty verifies behavior when no data is added.
func TestReporter_Empty(t *testing.T) {
	r := New()
	var buf bytes.Buffer
	if err := r.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}

	// Should produce valid JSON with empty values
	var decoded Data
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ErrorsHandled != 0 {
		t.Error("Expected 0 handled")
	}
	if len(decoded.FilesModified) != 0 {
		t.Error("Expected empty files list")
	}
}
