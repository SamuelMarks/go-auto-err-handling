package rewrite

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"regexp"
	"strings"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/astgen"
)

// RenderTemplate transforms a template string into a list of AST expressions suitable for a ReturnStmt.
//
// tmpl: The template string (e.g., "{return-zero}, fmt.Errorf(\"wrapping %s\", \"{func_name}\", err)").
// zeroExprs: The AST expressions for the zero values (converted to string and injected).
// errName: The actual name of the error variable in the scope (avoids shadowing conflicts).
// funcName: The name of the function being called, for context.
//
// Returns:
// - slice of ast.Expr for the return statement results.
// - slice of strings representing imports found in the template (e.g., "fmt").
// - error if parsing fails.
func RenderTemplate(tmpl string, zeroExprs []ast.Expr, errName string, funcName string) ([]ast.Expr, []string, error) {
	if tmpl == "" {
		tmpl = "{return-zero}, err"
	}

	// 1. Render zero values to string
	var zerosParts []string
	fset := token.NewFileSet()
	for _, z := range zeroExprs {
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, fset, z); err != nil {
			return nil, nil, fmt.Errorf("failed to render zero expr: %w", err)
		}
		zerosParts = append(zerosParts, buf.String())
	}
	zerosStr := strings.Join(zerosParts, ", ")

	// 2. Perform text replacements
	processed := tmpl

	// Handle {return-zero}
	if len(zerosParts) == 0 {
		// If no zero strings, remove "{return-zero}," or ", {return-zero}" to avoid syntax errors like ", err" or "err,".
		// Regex to remove trailing comma
		reTrailing := regexp.MustCompile(`\{return-zero\}\s*,\s*`)
		processed = reTrailing.ReplaceAllString(processed, "")

		// Regex to remove leading comma
		reLeading := regexp.MustCompile(`,\s*\{return-zero\}`)
		processed = reLeading.ReplaceAllString(processed, "")

		// Replace bare '{return-zero}' with empty
		processed = strings.ReplaceAll(processed, "{return-zero}", "")
	} else {
		processed = strings.ReplaceAll(processed, "{return-zero}", zerosStr)
	}

	// Handle {func_name}
	processed = strings.ReplaceAll(processed, "{func_name}", funcName)

	// Handle err variable name.
	// We use a regex word boundary to avoid replacing "error" -> "err1or"
	// Regex: \berr\b matches "err" but not "error", "terr", etc.
	reErr := regexp.MustCompile(`\berr\b`)
	processed = reErr.ReplaceAllString(processed, errName)

	// 3. Parse the resulting string into AST
	// We wrap it in a dummy function to parse as valid Go source.
	dummySrc := fmt.Sprintf("package p; func _() { return %s }", processed)
	file, err := parser.ParseFile(fset, "", dummySrc, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse generated template '%s': %w", processed, err)
	}

	// 4. Extract ReturnStmt
	var returnResults []ast.Expr
	var importsFound []string

	ast.Inspect(file, func(n ast.Node) bool {
		if ret, ok := n.(*ast.ReturnStmt); ok {
			returnResults = ret.Results
			return false // Found it, stop digging children of return
		}
		return true
	})

	if returnResults == nil {
		// Might happen if template was empty?
		return nil, nil, fmt.Errorf("no return statement found in processed template")
	}

	// 5. Clear Token Positions
	// The parser generated tokens with positions relative to the temporary Fset.
	// If we return these positions, the main printer will be confused and may generate bad indentation.
	// We use the shared logic from astgen.
	for _, expr := range returnResults {
		astgen.ClearPositions(expr)
	}

	// 6. Scan for potential imports in the expressions
	// We look for SelectorExpr where X is an Ident (e.g. fmt.Errorf -> X=fmt).
	for _, expr := range returnResults {
		ast.Inspect(expr, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok {
					// Heuristic: If it looks like a package name (lowercase), treat as import candidate.
					// This is simple but covers stdlib/common cases (fmt, log, errors).
					// Determining actual existence requires generic knowledge or assumption.
					// We'll trust the user's template implies availability.
					importsFound = append(importsFound, id.Name)
				}
			}
			return true
		})
	}

	return returnResults, uniqueStrings(importsFound), nil
}

func uniqueStrings(s []string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, v := range s {
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}
