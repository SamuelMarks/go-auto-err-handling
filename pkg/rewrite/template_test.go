package rewrite

import (
	"bytes"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/astgen"
	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
)

func TestRenderTemplateDST(t *testing.T) {
	// Setup zero exprs
	intType := types.Typ[types.Int]
	strType := types.Typ[types.String]
	zeroInt, _ := astgen.ZeroExprDST(intType, astgen.ZeroCtx{})
	zeroStr, _ := astgen.ZeroExprDST(strType, astgen.ZeroCtx{})

	tests := []struct {
		name         string
		tmpl         string
		zeroExprs    []dst.Expr
		errName      string
		funcName     string
		expectedCode string
		wantImports  []string
		wantErr      bool
	}{
		{
			name:         "Default",
			tmpl:         "",
			zeroExprs:    []dst.Expr{zeroInt},
			errName:      "err",
			funcName:     "foo",
			expectedCode: "0, err",
		},
		{
			name:         "CustomWrap",
			tmpl:         "{return-zero}, fmt.Errorf(\"call %s: %w\", \"{func_name}\", err)",
			zeroExprs:    []dst.Expr{zeroStr},
			errName:      "err1",
			funcName:     "MyFunc",
			expectedCode: `"", fmt.Errorf("call %s: %w", "MyFunc", err1)`,
			wantImports:  []string{"fmt"},
		},
		{
			name:         "NoZeros",
			tmpl:         "{return-zero}, err",
			zeroExprs:    nil, // void function case
			errName:      "err",
			funcName:     "foo",
			expectedCode: "err", // leading comma stripped
		},
		{
			name:         "NoZerosCustom",
			tmpl:         "{return-zero}, errors.Wrap(err, \"{func_name}\")",
			zeroExprs:    nil,
			errName:      "e",
			funcName:     "Bar",
			expectedCode: `errors.Wrap(e, "Bar")`,
			wantImports:  []string{"errors"},
		},
		{
			name: "ComplexZeros",
			tmpl: "{return-zero}, nil",
			// Represents (0, "", nil)
			zeroExprs:    []dst.Expr{zeroInt, zeroStr},
			errName:      "err",
			funcName:     "foo",
			expectedCode: `0, "", nil`,
		},
		{
			name:         "ErrNameReplacement",
			tmpl:         "error(err)", // "error" substring should not be replaced
			zeroExprs:    nil,
			errName:      "err2",
			expectedCode: "error(err2)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exprs, imports, err := RenderTemplateDST(tt.tmpl, tt.zeroExprs, tt.errName, tt.funcName)
			if (err != nil) != tt.wantErr {
				t.Fatalf("RenderTemplateDST() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			// Verify Imports
			if !equalStrings(imports, tt.wantImports) {
				t.Errorf("Imports mismatch. Got %v, Want %v", imports, tt.wantImports)
			}

			// Verify Code by rendering exprs back to string
			var buf bytes.Buffer
			// For testing verification, we use standard wrapping trick again to ensure Fprint works
			restorer := decorator.NewRestorer()
			for i, e := range exprs {
				if i > 0 {
					buf.WriteString(", ")
				}
				// Wrap again
				file := &dst.File{
					Name: dst.NewIdent("p"),
					Decls: []dst.Decl{
						&dst.GenDecl{
							Tok: token.VAR,
							Specs: []dst.Spec{
								&dst.ValueSpec{
									Names:  []*dst.Ident{dst.NewIdent("_")},
									Values: []dst.Expr{e},
								},
							},
						},
					},
				}
				var tmpBuf bytes.Buffer
				if err := restorer.Fprint(&tmpBuf, file); err != nil {
					t.Fatalf("Fprint failed: %v", err)
				}
				s := tmpBuf.String()
				// Trim to expression
				s = strings.TrimSpace(s)
				s = strings.TrimPrefix(s, "package p")
				s = strings.TrimSpace(s)
				s = strings.TrimPrefix(s, "var _ =")
				s = strings.TrimSpace(s)
				buf.WriteString(s)
			}

			if got := buf.String(); got != tt.expectedCode {
				t.Errorf("Rendered Code mismatch.\nGot:  %s\nWant: %s", got, tt.expectedCode)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]bool)
	for _, s := range a {
		m[s] = true
	}
	for _, s := range b {
		if !m[s] {
			return false
		}
	}
	return true
}
