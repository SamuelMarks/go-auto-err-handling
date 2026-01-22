package filter

import (
	"strings"
	"testing"
)

// TestGetDefaults checks that the default exclusion list contains expected members
// and is returned as a safe copy.
func TestGetDefaults(t *testing.T) {
	d := GetDefaults()

	if len(d) == 0 {
		t.Fatal("GetDefaults returned empty list")
	}

	foundFmt := false
	for _, s := range d {
		if strings.HasPrefix(s, "fmt.Print") {
			foundFmt = true
		}
	}

	if !foundFmt {
		t.Error("Default globs missing 'fmt.Print*'")
	}

	// Ensure it's a copy
	d[0] = "mutated"
	d2 := GetDefaults()
	if d2[0] == "mutated" {
		t.Error("GetDefaults returned reference to mutable global, expected copy")
	}
}
