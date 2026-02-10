package rewrite

import (
	"errors"
	"go/ast"
	"go/printer"
	"go/token"
	"go/types"
	"io"
	"strings"
	"testing"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/astgen"
	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
)

func renderASTExprs(t *testing.T, exprs []ast.Expr) string {
	if len(exprs) == 0 {
		return ""
	}
	var out strings.Builder
	fset := token.NewFileSet()
	for i, e := range exprs {
		if i > 0 {
			out.WriteString(", ")
		}
		if err := printer.Fprint(&out, fset, e); err != nil {
			t.Fatalf("printer failed: %v", err)
		}
	}
	return out.String()
}

func TestRenderTemplate_LegacySuccess(t *testing.T) {
	zeroInt, err := astgen.ZeroExpr(types.Typ[types.Int], astgen.ZeroCtx{})
	if err != nil {
		t.Fatalf("zero int: %v", err)
	}
	if _, _, err := RenderTemplate("", []ast.Expr{zeroInt}, "err", "fn"); err != nil {
		t.Fatalf("RenderTemplate default failed: %v", err)
	}
	exprs, imports, err := RenderTemplate(`{return-zero}, fmt.Errorf("%s", err)`, []ast.Expr{zeroInt}, "err", "fn")
	if err != nil {
		t.Fatalf("RenderTemplate failed: %v", err)
	}
	if len(exprs) == 0 {
		t.Fatal("expected expressions")
	}
	if got := renderASTExprs(t, exprs); !strings.Contains(got, "fmt.Errorf") {
		t.Fatalf("expected fmt.Errorf in output, got %q", got)
	}
	if len(imports) != 1 || imports[0] != "fmt" {
		t.Fatalf("expected fmt import, got %v", imports)
	}
}

func TestRenderTemplate_LegacyErrors(t *testing.T) {
	zeroInt, err := astgen.ZeroExpr(types.Typ[types.Int], astgen.ZeroCtx{})
	if err != nil {
		t.Fatalf("zero int: %v", err)
	}

	// Parse error
	if _, _, err := RenderTemplate("{return-zero}, )", []ast.Expr{zeroInt}, "err", "fn"); err == nil {
		t.Fatal("expected parse error")
	}

	// No return results -> treated as error
	if _, _, err := RenderTemplate("{return-zero}", nil, "err", "fn"); err == nil {
		t.Fatal("expected no-return error")
	}

	// Force printer error via hook
	orig := printerFprint
	printerFprint = func(_ io.Writer, _ *token.FileSet, _ any) error {
		return errors.New("boom")
	}
	defer func() { printerFprint = orig }()

	if _, _, err := RenderTemplate("{return-zero}", []ast.Expr{zeroInt}, "err", "fn"); err == nil {
		t.Fatal("expected printer error")
	}
}

func TestRenderTemplateDST_Errors(t *testing.T) {
	zeroInt, err := astgen.ZeroExprDST(types.Typ[types.Int], astgen.ZeroCtx{})
	if err != nil {
		t.Fatalf("zero int: %v", err)
	}

	// Parse error
	if _, _, err := RenderTemplateDST("{return-zero}, )", []dst.Expr{zeroInt}, "err", "fn"); err == nil {
		t.Fatal("expected parse error")
	}

	// No return results
	if _, _, err := RenderTemplateDST("{return-zero}", nil, "err", "fn"); err == nil {
		t.Fatal("expected no-return error")
	}

	// Force restorer error via hook
	orig := restorerFprint
	restorerFprint = func(_ *decorator.Restorer, _ io.Writer, _ *dst.File) error {
		return errors.New("boom")
	}
	defer func() { restorerFprint = orig }()

	if _, _, err := RenderTemplateDST("{return-zero}", []dst.Expr{zeroInt}, "err", "fn"); err == nil {
		t.Fatal("expected restorer error")
	}
}
