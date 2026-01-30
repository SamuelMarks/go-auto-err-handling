package astgen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"

	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
)

// ZeroCtx holds configuration and context for generating zero values.
type ZeroCtx struct {
	// Qualifier formats package names in type strings.
	Qualifier types.Qualifier

	// Overrides maps a fully qualified type string to a Go expression string.
	Overrides map[string]string

	// MakeMapsAndChans, if true, causes maps and channels to be initialized via 'make(...)'.
	MakeMapsAndChans bool
}

// ZeroExpr generates an ast.Expr representing the zero value (Legacy AST version).
func ZeroExpr(t types.Type, ctx ZeroCtx) (ast.Expr, error) {
	if t == nil {
		return nil, fmt.Errorf("type is nil")
	}

	typeKey := types.TypeString(t, nil)
	if override, ok := ctx.Overrides[typeKey]; ok {
		expr, err := parser.ParseExpr(override)
		if err != nil {
			return nil, fmt.Errorf("failed to parse override for %s: %w", typeKey, err)
		}
		ClearPositions(expr)
		return expr, nil
	}

	if tp, ok := t.(*types.TypeParam); ok {
		return genericZeroAST(tp, ctx.Qualifier)
	}

	switch u := t.Underlying().(type) {
	case *types.Basic:
		return basicZeroAST(u)
	case *types.Pointer, *types.Slice, *types.Signature, *types.Interface:
		return &ast.Ident{Name: "nil"}, nil
	case *types.Map, *types.Chan:
		if ctx.MakeMapsAndChans {
			return makeInitializedAST(t, ctx.Qualifier)
		}
		return &ast.Ident{Name: "nil"}, nil
	case *types.Struct, *types.Array:
		return compositeZeroAST(t, ctx.Qualifier)
	case *types.Tuple:
		return nil, fmt.Errorf("tuple types are not supported for single value generation")
	default:
		return nil, fmt.Errorf("unsupported type: %v", t)
	}
}

// ZeroExprDST generates a dst.Expr representing the zero value (New DST version).
func ZeroExprDST(t types.Type, ctx ZeroCtx) (dst.Expr, error) {
	if t == nil {
		return nil, fmt.Errorf("type is nil")
	}

	typeKey := types.TypeString(t, nil)
	if override, ok := ctx.Overrides[typeKey]; ok {
		return parseDstExpr(override)
	}

	if tp, ok := t.(*types.TypeParam); ok {
		return genericZeroDST(tp, ctx.Qualifier)
	}

	switch u := t.Underlying().(type) {
	case *types.Basic:
		return basicZeroDST(u)
	case *types.Pointer, *types.Slice, *types.Signature, *types.Interface:
		return &dst.Ident{Name: "nil"}, nil
	case *types.Map, *types.Chan:
		if ctx.MakeMapsAndChans {
			return makeInitializedDST(t, ctx.Qualifier)
		}
		return &dst.Ident{Name: "nil"}, nil
	case *types.Struct, *types.Array:
		return compositeZeroDST(t, ctx.Qualifier)
	case *types.Tuple:
		return nil, fmt.Errorf("tuple types are not supported for single value generation")
	default:
		return nil, fmt.Errorf("unsupported type: %v", t)
	}
}

// --- AST Implementations ---

func basicZeroAST(b *types.Basic) (ast.Expr, error) {
	info := b.Info()
	switch {
	case info&types.IsBoolean != 0:
		return &ast.Ident{Name: "false"}, nil
	case info&types.IsNumeric != 0:
		return &ast.BasicLit{Kind: token.INT, Value: "0"}, nil
	case info&types.IsString != 0:
		return &ast.BasicLit{Kind: token.STRING, Value: `""`}, nil
	case b.Kind() == types.UnsafePointer:
		return &ast.Ident{Name: "nil"}, nil
	default:
		return &ast.Ident{Name: "nil"}, nil
	}
}

func compositeZeroAST(t types.Type, q types.Qualifier) (ast.Expr, error) {
	typeStr := types.TypeString(t, q)
	typeExpr, err := parser.ParseExpr(typeStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse type string '%s': %w", typeStr, err)
	}
	ClearPositions(typeExpr)
	return &ast.CompositeLit{Type: typeExpr, Elts: nil}, nil
}

func genericZeroAST(t *types.TypeParam, q types.Qualifier) (ast.Expr, error) {
	typeStr := types.TypeString(t, q)
	typeExpr, err := parser.ParseExpr(typeStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse generic type string '%s': %w", typeStr, err)
	}
	ClearPositions(typeExpr)
	return &ast.StarExpr{X: &ast.CallExpr{Fun: &ast.Ident{Name: "new"}, Args: []ast.Expr{typeExpr}}}, nil
}

func makeInitializedAST(t types.Type, q types.Qualifier) (ast.Expr, error) {
	typeStr := types.TypeString(t, q)
	typeExpr, err := parser.ParseExpr(typeStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse type string '%s': %w", typeStr, err)
	}
	ClearPositions(typeExpr)
	return &ast.CallExpr{Fun: &ast.Ident{Name: "make"}, Args: []ast.Expr{typeExpr}}, nil
}

// --- DST Implementations ---

func basicZeroDST(b *types.Basic) (dst.Expr, error) {
	info := b.Info()
	switch {
	case info&types.IsBoolean != 0:
		return &dst.Ident{Name: "false"}, nil
	case info&types.IsNumeric != 0:
		return &dst.BasicLit{Kind: token.INT, Value: "0"}, nil
	case info&types.IsString != 0:
		return &dst.BasicLit{Kind: token.STRING, Value: `""`}, nil
	case b.Kind() == types.UnsafePointer:
		return &dst.Ident{Name: "nil"}, nil
	default:
		return &dst.Ident{Name: "nil"}, nil
	}
}

func compositeZeroDST(t types.Type, q types.Qualifier) (dst.Expr, error) {
	typeStr := types.TypeString(t, q)
	typeExpr, err := parseDstType(typeStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse type string '%s': %w", typeStr, err)
	}
	return &dst.CompositeLit{Type: typeExpr, Elts: nil}, nil
}

func genericZeroDST(t *types.TypeParam, q types.Qualifier) (dst.Expr, error) {
	typeStr := types.TypeString(t, q)
	typeExpr, err := parseDstType(typeStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse generic type string '%s': %w", typeStr, err)
	}
	return &dst.StarExpr{X: &dst.CallExpr{Fun: &dst.Ident{Name: "new"}, Args: []dst.Expr{typeExpr}}}, nil
}

func makeInitializedDST(t types.Type, q types.Qualifier) (dst.Expr, error) {
	typeStr := types.TypeString(t, q)
	typeExpr, err := parseDstType(typeStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse type string '%s': %w", typeStr, err)
	}
	return &dst.CallExpr{Fun: &dst.Ident{Name: "make"}, Args: []dst.Expr{typeExpr}}, nil
}

// --- Helpers ---

// ClearPositions recursively sets position information to token.NoPos for the AST node.
func ClearPositions(node ast.Node) {
	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			x.NamePos = token.NoPos
		case *ast.BasicLit:
			x.ValuePos = token.NoPos
		case *ast.BinaryExpr:
			x.OpPos = token.NoPos
		case *ast.CallExpr:
			x.Lparen = token.NoPos
			x.Rparen = token.NoPos
		case *ast.CompositeLit:
			x.Lbrace = token.NoPos
			x.Rbrace = token.NoPos
		case *ast.Ellipsis:
			x.Ellipsis = token.NoPos
		case *ast.FuncLit:
			x.Type.Func = token.NoPos
		case *ast.FuncType:
			x.Func = token.NoPos
		case *ast.IndexExpr:
			x.Lbrack = token.NoPos
			x.Rbrack = token.NoPos
		case *ast.InterfaceType:
			x.Interface = token.NoPos
		case *ast.KeyValueExpr:
			x.Colon = token.NoPos
		case *ast.MapType:
			x.Map = token.NoPos
		case *ast.ParenExpr:
			x.Lparen = token.NoPos
			x.Rparen = token.NoPos
		case *ast.SelectorExpr:
		case *ast.SliceExpr:
			x.Lbrack = token.NoPos
			x.Rbrack = token.NoPos
		case *ast.StarExpr:
			x.Star = token.NoPos
		case *ast.StructType:
			x.Struct = token.NoPos
		case *ast.TypeAssertExpr:
			x.Lparen = token.NoPos
			x.Rparen = token.NoPos
		case *ast.UnaryExpr:
			x.OpPos = token.NoPos
		case *ast.FieldList:
			x.Opening = token.NoPos
			x.Closing = token.NoPos
		case *ast.ArrayType:
			x.Lbrack = token.NoPos
		case *ast.ChanType:
			x.Begin = token.NoPos
			x.Arrow = token.NoPos
		}
		return true
	})
}

// ClearDecorations recursively clears formatting decorations (spacing, comments) from a DST node.
func ClearDecorations(node dst.Node) {
	dst.Inspect(node, func(n dst.Node) bool {
		if n == nil {
			return false
		}
		n.Decorations().Start.Clear()
		n.Decorations().End.Clear()
		n.Decorations().Before = dst.None
		n.Decorations().After = dst.None
		return true
	})
}

// parseDstExpr extracts a DST expression from a string.
func parseDstExpr(exprStr string) (dst.Expr, error) {
	src := "package p; var _ = " + exprStr
	f, err := decorator.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("failed to parse override expr: %w", err)
	}

	if len(f.Decls) > 0 {
		if gd, ok := f.Decls[0].(*dst.GenDecl); ok && len(gd.Specs) > 0 {
			if vs, ok := gd.Specs[0].(*dst.ValueSpec); ok && len(vs.Values) > 0 {
				expr := vs.Values[0]
				ClearDecorations(expr)
				return expr, nil
			}
		}
	}
	return nil, fmt.Errorf("failed to extract expression from parsed AST")
}

// parseDstType extracts a DST type from a string.
func parseDstType(typeStr string) (dst.Expr, error) {
	src := "package p; type _ " + typeStr
	f, err := decorator.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("failed to parse type string: %w", err)
	}

	if len(f.Decls) > 0 {
		if gd, ok := f.Decls[0].(*dst.GenDecl); ok && len(gd.Specs) > 0 {
			if ts, ok := gd.Specs[0].(*dst.TypeSpec); ok {
				expr := ts.Type
				ClearDecorations(expr)
				return expr, nil
			}
		}
	}
	return nil, fmt.Errorf("failed to extract type from parsed AST")
}
