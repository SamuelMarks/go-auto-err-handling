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
	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
)

// RenderTemplate transforms a template into a list of AST expressions (Legacy).
func RenderTemplate(tmpl string, zeroExprs []ast.Expr, errName string, funcName string) ([]ast.Expr, []string, error) {
	// Reusing logic via temporary source string is easiest but we have existing logic.
	// For compilation checks of existing dependent code, we restore original logic.
	if tmpl == "" {
		tmpl = "{return-zero}, err"
	}

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
	processed := applyTemplateReplacement(tmpl, zerosStr, funcName, errName)

	dummySrc := fmt.Sprintf("package p; func _() { return %s }", processed)
	file, err := parser.ParseFile(fset, "", dummySrc, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse generated template '%s': %w", processed, err)
	}

	var returnResults []ast.Expr
	var importsFound []string
	ast.Inspect(file, func(n ast.Node) bool {
		if ret, ok := n.(*ast.ReturnStmt); ok {
			returnResults = ret.Results
			return false
		}
		return true
	})
	if returnResults == nil {
		return nil, nil, fmt.Errorf("no return statement found")
	}
	for _, expr := range returnResults {
		astgen.ClearPositions(expr)
		// Scan imports
		ast.Inspect(expr, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok {
					importsFound = append(importsFound, id.Name)
				}
			}
			return true
		})
	}
	return returnResults, uniqueStrings(importsFound), nil
}

// RenderTemplateDST transforms a template into a list of DST expressions (New).
func RenderTemplateDST(tmpl string, zeroExprs []dst.Expr, errName string, funcName string) ([]dst.Expr, []string, error) {
	if tmpl == "" {
		tmpl = "{return-zero}, err"
	}

	var zerosParts []string
	restorer := decorator.NewRestorer()
	for _, z := range zeroExprs {
		var buf bytes.Buffer
		// Wrap z in a dummy file to satisfy Restorer.Fprint strict check
		file := &dst.File{
			Name: dst.NewIdent("p"),
			Decls: []dst.Decl{
				&dst.GenDecl{
					Tok: token.VAR,
					Specs: []dst.Spec{
						&dst.ValueSpec{
							Names:  []*dst.Ident{dst.NewIdent("_")},
							Values: []dst.Expr{z},
						},
					},
				},
			},
		}
		if err := restorer.Fprint(&buf, file); err != nil {
			return nil, nil, fmt.Errorf("failed to render zero expr: %w", err)
		}
		// Extract cleaned string from "package p\n\nvar _ = expr"
		s := buf.String()
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "package p")
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "var _ =")
		s = strings.TrimSpace(s)
		zerosParts = append(zerosParts, s)
	}
	zerosStr := strings.Join(zerosParts, ", ")
	processed := applyTemplateReplacement(tmpl, zerosStr, funcName, errName)

	dummySrc := fmt.Sprintf("package p; func _() { return %s }", processed)
	// decorator.Parse accepts just source string
	file, err := decorator.Parse(dummySrc)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse generated template '%s': %w", processed, err)
	}

	var returnResults []dst.Expr
	var importsFound []string
	dst.Inspect(file, func(n dst.Node) bool {
		if ret, ok := n.(*dst.ReturnStmt); ok {
			returnResults = ret.Results
			return false
		}
		return true
	})
	if returnResults == nil {
		return nil, nil, fmt.Errorf("no return statement found")
	}

	for _, expr := range returnResults {
		astgen.ClearDecorations(expr)
		dst.Inspect(expr, func(n dst.Node) bool {
			if sel, ok := n.(*dst.SelectorExpr); ok {
				if id, ok := sel.X.(*dst.Ident); ok {
					importsFound = append(importsFound, id.Name)
				}
			}
			return true
		})
	}
	return returnResults, uniqueStrings(importsFound), nil
}

func applyTemplateReplacement(tmpl, zerosStr, funcName, errName string) string {
	processed := tmpl
	if zerosStr == "" {
		reTrailing := regexp.MustCompile(`\{return-zero\}\s*,\s*`)
		processed = reTrailing.ReplaceAllString(processed, "")
		reLeading := regexp.MustCompile(`,\s*\{return-zero\}`)
		processed = reLeading.ReplaceAllString(processed, "")
		processed = strings.ReplaceAll(processed, "{return-zero}", "")
	} else {
		processed = strings.ReplaceAll(processed, "{return-zero}", zerosStr)
	}
	processed = strings.ReplaceAll(processed, "{func_name}", funcName)
	reErr := regexp.MustCompile(`\berr\b`)
	processed = reErr.ReplaceAllString(processed, errName)
	return processed
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
