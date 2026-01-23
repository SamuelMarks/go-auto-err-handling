package astgen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
)

// ZeroCtx holds configuration and context for generating zero values.
type ZeroCtx struct {
	// Qualifier formats package names in type strings.
	// If nil, package names are fully qualified by default or handled via overrides.
	Qualifier types.Qualifier

	// Overrides maps a fully qualified type string (e.g., "*k8s.io/api/admission/v1.AdmissionResponse")
	// to a Go expression string (e.g., "&k8s.io/api/admission/v1.AdmissionResponse{Allowed: false}").
	// Using overrides allows generating "Soft Failures" where a zero-value is technically valid
	// but logically incorrect for the application flow (e.g., returning nil pointer vs error struct).
	Overrides map[string]string
}

// ZeroExpr generates an ast.Expr representing the zero value for the given data type.
// It handles basic literals (0, "", false, nil), composite types (structs, arrays),
// and generic type parameters (*new(T)).
//
// It checks the provided Context for overrides first. If an override exists for the type string,
// it parses and returns that expression. Otherwise, it generates the standard Go zero value.
//
// t: The type for which to generate the zero value.
// ctx: Configuration context containing qualifiers and overrides.
//
// Returns the AST expression for the zero value, or an error if the type is unsupported.
func ZeroExpr(t types.Type, ctx ZeroCtx) (ast.Expr, error) {
	if t == nil {
		return nil, fmt.Errorf("type is nil")
	}

	// 1. Check Overrides
	// We need the fully qualified name to match against the configuration map.
	// We use the default type string behavior (Package.Name) or the custom qualifier if provided.
	typeKey := types.TypeString(t, nil) // Raw fully qualified name for robust key matching
	if override, ok := ctx.Overrides[typeKey]; ok {
		expr, err := parser.ParseExpr(override)
		if err != nil {
			return nil, fmt.Errorf("failed to parse override for %s: %w", typeKey, err)
		}
		ClearPositions(expr)
		return expr, nil
	}

	// 2. Standard Generation
	// Check for Type Param (Generics) explicitly before Underlying().
	// A TypeParam's underlying type is its constraint interface, but we want the parameter itself.
	if tp, ok := t.(*types.TypeParam); ok {
		return genericZero(tp, ctx.Qualifier)
	}

	switch u := t.Underlying().(type) {
	case *types.Basic:
		return basicZero(u)
	case *types.Pointer, *types.Slice, *types.Map, *types.Chan, *types.Signature, *types.Interface:
		return &ast.Ident{Name: "nil"}, nil
	case *types.Struct, *types.Array:
		return compositeZero(t, ctx.Qualifier)
	case *types.Tuple:
		return nil, fmt.Errorf("tuple types are not supported for single value generation")
	default:
		// Attempt fallback to nil if it looks like something nullable, otherwise error
		return nil, fmt.Errorf("unsupported type: %v", t)
	}
}

// basicZero generates input agnostic zero values for basic types (int, string, bool, etc).
//
// b: The basic type to inspect.
func basicZero(b *types.Basic) (ast.Expr, error) {
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

// compositeZero generates a composite literal (e.g., MyType{}) for structs and arrays.
// It uses the type string representation to construct the type identifier.
//
// t: The full type (named or literal).
// q: The qualifier for package names.
func compositeZero(t types.Type, q types.Qualifier) (ast.Expr, error) {
	typeStr := types.TypeString(t, q)

	typeExpr, err := parser.ParseExpr(typeStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse type string '%s': %w", typeStr, err)
	}

	ClearPositions(typeExpr)

	return &ast.CompositeLit{
		Type: typeExpr,
		Elts: nil, // empty elements for zero value
	}, nil
}

// genericZero generates a zero value for a Generic Type Parameter T.
// Since we don't know the concrete type at analysis time, we rely on the Go 1.18+ idiom:
//
//	*new(T)
//
// This allocates a zero-valued T and dereferences it, resulting in the zero value of T itself,
// valid for use in return statements.
//
// t: The type parameter.
// q: Qualifier for package names (though TypeParams are usually local names).
func genericZero(t *types.TypeParam, q types.Qualifier) (ast.Expr, error) {
	typeStr := types.TypeString(t, q)

	// Type Params are usually simple identifiers like "T" or "K".
	// However, types.TypeString handles formatting correctly.
	typeExpr, err := parser.ParseExpr(typeStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse generic type string '%s': %w", typeStr, err)
	}
	ClearPositions(typeExpr)

	// Construct *new(T)
	return &ast.StarExpr{
		X: &ast.CallExpr{
			Fun:  &ast.Ident{Name: "new"},
			Args: []ast.Expr{typeExpr},
		},
	}, nil
}

// ClearPositions recursively sets position information to token.NoPos for the AST node.
// This allows go/printer to reformat the node freely.
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
