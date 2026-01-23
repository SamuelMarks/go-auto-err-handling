package refactor

import (
	"go/ast"
	"go/types"
	"regexp"
	"unicode"
)

// defaultTypeMap defines mappings for well-known types to their idiomatic variable names.
// Keys are fully qualified type strings (sans pointer *) or basic type names.
var defaultTypeMap = map[string]string{
	"context.Context":         "ctx",
	"error":                   "err",
	"net/http.ResponseWriter": "w",
	"net/http.Request":        "r",
	"database/sql.Tx":         "tx",
	"database/sql.DB":         "db",
	"io.Reader":               "r",
	"io.Writer":               "w",
	"io.Closer":               "c",
	"testing.T":               "t",
	"testing.B":               "b",
	"sync.WaitGroup":          "wg",
	"sync.Mutex":              "mu",
	"sync.RWMutex":            "mu",
	"time.Time":               "t",
	"time.Duration":           "d",
	// Basics
	"bool":    "b",
	"string":  "s",
	"int":     "i",
	"int8":    "i",
	"int16":   "i",
	"int32":   "i",
	"int64":   "i",
	"uint":    "u",
	"uint8":   "b", // byte
	"uint16":  "u",
	"uint32":  "u",
	"uint64":  "u",
	"byte":    "b",
	"rune":    "r",
	"float32": "f",
	"float64": "f",
}

// NameForType generates a heuristic variable name for a given Go type.
// It handles standard library idioms (Context -> ctx), pointer stripping,
// and basic camelCase conversion for named types.
//
// t: The type to analyze.
//
// Returns a short, idiomatic variable name (e.g., "ctx", "s", "user").
func NameForType(t types.Type) string {
	if t == nil {
		return "v"
	}

	// 1. Unwrap pointers, slices, and arrays to get the core element.
	// We want *User -> user, []User -> users (plural handling later?), []*User -> user
	core := unwrapType(t)

	// 2. Check for exact matches in our dictionary
	typeString := stripPackagePath(t.String()) // Fallback for basic types like "string"
	if name, ok := defaultTypeMap[typeString]; ok {
		return name
	}

	// Check fully qualified named types
	if named, ok := core.(*types.Named); ok {
		fullName := named.Obj().Pkg().Path() + "." + named.Obj().Name()
		if name, ok := defaultTypeMap[fullName]; ok {
			return name
		}
		fullNameNoPath := named.Obj().Name()
		if name, ok := defaultTypeMap[fullNameNoPath]; ok {
			return name
		}

		// Fallback: Use the type name converted to lowerCamelCase
		return toVariableName(named.Obj().Name())
	}

	// 3. Handle Basic Types if not caught in map
	if basic, ok := core.(*types.Basic); ok {
		// This should usually catch int, string etc via string match above,
		// but ensures safety for untyped or odd basics.
		if basic.Info()&types.IsInteger != 0 {
			return "i"
		}
		if basic.Info()&types.IsBoolean != 0 {
			return "b"
		}
		if basic.Info()&types.IsString != 0 {
			return "s"
		}
	}

	// 4. Handle Interfaces (similar to named)
	if _, ok := core.(*types.Interface); ok {
		// Usually interfaces are Named types, so caught above.
		// If anonymous interface, return v
		return "v"
	}

	return "v"
}

// NameForExpr generates a name based purely on the AST expression.
// This is a fallback when TypeInfo is missing (e.g. in some unit tests).
//
// expr: The AST expression (e.g. &ast.Ident{Name: "int"})
func NameForExpr(expr ast.Expr) string {
	if expr == nil {
		return "v"
	}

	// Unwrap stars/arrays in AST
	core := unwrapExpr(expr)

	// Resolve the identifier or selector string
	var typeName string
	switch t := core.(type) {
	case *ast.Ident:
		typeName = t.Name
	case *ast.SelectorExpr:
		// pkg.Type -> use Type
		typeName = t.Sel.Name
		// We can also try to reconstruct "pkg.Type" for map lookup
		// if X is an ident.
		if x, ok := t.X.(*ast.Ident); ok {
			full := x.Name + "." + t.Sel.Name
			if val, ok := defaultTypeMap[full]; ok {
				return val
			}
		}
	default:
		return "v"
	}

	if val, ok := defaultTypeMap[typeName]; ok {
		return val
	}

	return toVariableName(typeName)
}

func unwrapType(t types.Type) types.Type {
	switch u := t.(type) {
	case *types.Pointer:
		return unwrapType(u.Elem())
	case *types.Slice:
		return unwrapType(u.Elem())
	case *types.Array:
		return unwrapType(u.Elem())
	}
	return t
}

func unwrapExpr(e ast.Expr) ast.Expr {
	switch u := e.(type) {
	case *ast.StarExpr:
		return unwrapExpr(u.X)
	case *ast.ArrayType:
		return unwrapExpr(u.Elt)
	}
	return e
}

// stripPackagePath handles cleanup for basic type string representation if needed
func stripPackagePath(s string) string {
	// For "string" it returns "string". for "pkg.Type" it returns "pkg.Type".
	return s
}

// toVariableName converts "MyStruct" -> "myStruct", "ID" -> "id", "APIClient" -> "apiClient"
func toVariableName(s string) string {
	if s == "" {
		return "v"
	}
	runes := []rune(s)

	// Iterate through characters
	for i, r := range runes {
		if unicode.IsUpper(r) {
			shouldLower := false
			// 1. Always lower the first character
			if i == 0 {
				shouldLower = true
			} else if i+1 < len(runes) {
				// 2. If the next character is also Upper, this is part of an acronym chain.
				// We lower this one unless the next one is lower (meaning this is the last char of acronym before a word).
				// E.g. "APIClient". A->a, P->p, I->i. C (next l)->C.
				if unicode.IsUpper(runes[i+1]) {
					shouldLower = true
				}
			} else {
				// 3. End of string. If we are here, we are in an upper-case streak.
				// For "APIID" -> "apiid".
				shouldLower = true
			}

			if shouldLower {
				runes[i] = unicode.ToLower(r)
			}
		} else {
			// Hit a lowercase, stop processing prefix to preserve inner words (e.g. MyHTTPServer -> myHTTPServer)
			break
		}
	}

	res := string(runes)
	if res == "break" || res == "default" || res == "func" || res == "interface" || res == "select" ||
		res == "case" || res == "defer" || res == "go" || res == "map" || res == "struct" ||
		res == "chan" || res == "else" || res == "goto" || res == "package" || res == "switch" ||
		res == "const" || res == "fallthrough" || res == "if" || res == "range" || res == "type" ||
		res == "continue" || res == "for" || res == "import" || res == "return" || res == "var" {
		return string(runes[0]) // Fallback to first letter for keywords
	}

	// Clean invalid chars (rare in type names)
	re := regexp.MustCompile(`[^a-zA-Z0-9_]`)
	res = re.ReplaceAllString(res, "")

	if res == "" {
		return "v"
	}
	return res
}
