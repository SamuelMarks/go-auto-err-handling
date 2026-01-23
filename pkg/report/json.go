package report

import (
	"encoding/json"
	"io"
	"sort"
	"sync"
)

// Data represents the structure of the JSON report output.
// It maps directly to the required JSON schema for CI integration.
type Data struct {
	// FilesModified lists the unique paths of files that were altered during execution.
	FilesModified []string `json:"files_modified"`
	// ErrorsHandled is the count of error handling checks injected.
	ErrorsHandled int `json:"errors_handled"`
	// Skipped is the count of injection points that were filtered out or ignored.
	Skipped int `json:"skipped"`
}

// Reporter collects statistics during the refactoring process and generates structured output.
// It is safe for concurrent use.
type Reporter struct {
	mu      sync.Mutex
	data    Data
	fileSet map[string]struct{}
}

// New creates a new instance of Reporter with initialized maps.
func New() *Reporter {
	return &Reporter{
		fileSet: make(map[string]struct{}),
		data: Data{
			FilesModified: []string{},
		},
	}
}

// AddFile records a file path as modified.
// It creates a unique set of files, ignoring duplicates.
//
// path: The file path to record.
func (r *Reporter) AddFile(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.fileSet[path]; !exists {
		r.fileSet[path] = struct{}{}
		r.data.FilesModified = append(r.data.FilesModified, path)
	}
}

// IncHandled increments the counter for successfully processed error injection points.
func (r *Reporter) IncHandled() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data.ErrorsHandled++
}

// IncSkipped increments the counter for injection points that were identified but skipped per configuration.
func (r *Reporter) IncSkipped() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data.Skipped++
}

// WriteJSON serializes the collected statistics to the provided writer in indented JSON format.
// Validates that the file list is sorted before writing to ensure deterministic output.
//
// w: The writer to output the JSON to.
func (r *Reporter) WriteJSON(w io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Ensure deterministic output
	sort.Strings(r.data.FilesModified)

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r.data)
}

// GetData returns a copy of the internal data structure.
// This is primarily useful for testing or programmatic access aside from writing JSON.
func (r *Reporter) GetData() Data {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Return copy to prevent external mutation race conditions
	// Slice copy needed
	files := make([]string, len(r.data.FilesModified))
	copy(files, r.data.FilesModified)
	sort.Strings(files)

	return Data{
		FilesModified: files,
		ErrorsHandled: r.data.ErrorsHandled,
		Skipped:       r.data.Skipped,
	}
}
