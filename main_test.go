package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRun verifies CLI parsing logic and defaults.
func TestRun(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		expected  string
		expectErr bool
	}{
		{
			name:     "DefaultAllEnabled",
			args:     []string{},
			expected: "Active Levels: Preexisting=true, ReturnTypeChanges=true, ThirdParty=true",
		},
		{
			name: "DisableThirdParty",
			// Explicitly set bool to false to ensure parsing works regardless of negation syntax support
			args:     []string{"--third-party=false"},
			expected: "ThirdParty=false",
		},
		{
			name: "DisableAll",
			args: []string{
				"--local-preexisting-err=false",
				"--return-type-changes=false",
				"--third-party=false",
			},
			expected: "Preexisting=false, ReturnTypeChanges=false, ThirdParty=false",
		},
		{
			name:     "WithPath",
			args:     []string{"--dry-run", "./pkg/..."},
			expected: "Starting analysis on paths: [./pkg/...]",
		},
		{
			name:     "CheckFlag",
			args:     []string{"--check", "."},
			expected: "Mode: CI Check (Verification)",
		},
		{
			name:     "VerifySameAsCheck",
			args:     []string{"--verify", "."},
			expected: "Mode: CI Check (Verification)",
		},
		{
			name:      "UnknownFlag",
			args:      []string{"--foo-bar"},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			// Capture runner logger
			err := run(tt.args, &buf)

			if tt.expectErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				// We care about the log output for flag verification.
				t.Logf("Runner execution error: %v", err)
			}

			output := buf.String()
			if !strings.Contains(output, tt.expected) {
				t.Errorf("output missing %q. Got:\n%s", tt.expected, output)
			}
		})
	}
}
