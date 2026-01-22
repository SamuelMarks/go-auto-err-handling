package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRun verifies that the command line arguments are correctly parsed into the configuration
// and that the application runs without error for valid inputs.
func TestRun(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		expected  string
		expectErr bool
	}{
		{
			name:     "Default",
			args:     []string{},
			expected: "Starting analysis on paths:",
		},
		{
			name:     "LocalPreexisting",
			args:     []string{"--local-preexisting-err", "pkg/foo"},
			expected: "Mode: Preexisting=true",
		},
		{
			name:     "FullConfiguration",
			args:     []string{"--local-nonexisting-err", "--exclude-glob", "*_test.go", "--exclude-symbol-glob", "fmt.*"},
			expected: "NonExisting=true",
		},
		{
			name:      "UnknownFlag",
			args:      []string{"--unknown-flag"},
			expectErr: true,
		},
		{
			name:     "ThirdParty",
			args:     []string{"--third-party-err"},
			expected: "ThirdParty=true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := run(tt.args, &buf)

			if tt.expectErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			output := buf.String()
			if !strings.Contains(output, tt.expected) {
				t.Errorf("expected output to contain %q, got %q", tt.expected, output)
			}
		})
	}
}
