package filter

// DefaultSymbolGlobs defines a list of function signatures that are commonly ignored
// in Go programs and thus excluded by default to reduce noise.
//
// This list includes printing functions and memory buffer writes which, while returning
// errors, are rarely handled in application logic unless strict reliability determines 100% check coverage.
var DefaultSymbolGlobs = []string{
	// fmt package: Printing usually goes to stdout/stderr; failures are often ignored.
	"fmt.Print*",
	"fmt.Fprint*",
	"fmt.Sprint*",
	"fmt.Scan*",

	// log package: Similar to fmt.
	"log.Print*",
	"log.Output",

	// strings and bytes buffers: Writes to memory buffers (that don't do I/O) rarely fail (only OOM/panic).
	"strings.Builder.Write*",
	"bytes.Buffer.Write*",
	"bytes.Buffer.Read*",

	// io utilities: WriteString is often used for simple outputs.
	"io.WriteString",
}

// GetDefaults returns the default symbol globs.
func GetDefaults() []string {
	// Return a copy to prevent mutation of the global slice
	dst := make([]string, len(DefaultSymbolGlobs))
	copy(dst, DefaultSymbolGlobs)
	return dst
}
