package astgen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
)

// ZeroExpr generates an ast.Expr representing the zero value for the given data type.
// It handles basic literals (0, "", false, nil) and composite types (structs, arrays)
// by creating suitable AST nodes.
//
// t: The type for which to generate the zero value.
// q: An optional qualifier function to format package names in type strings.
func ZeroExpr(t types.Type, q types.Qualifier) (ast.Expr, error) {
	// check the underlying type to determine the category of the zero value
	switch u := t.Underlying().(type) {
	case *types.Basic:
		return basicZero(u)
	case *types.Pointer, *types.Slice, *types.Map, *types.Chan, *types.Signature, *types.Interface:
		return &ast.Ident{Name: "nil"}, nil
	case *types.Struct, *types.Array:
		return compositeZero(t, q)
	case *types.Tuple:
		return nil, fmt.Errorf("tuple types are not supported for single value generation")
	default:
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
	default: // defaults to nil for untyped nil or unknown basics
		return &ast.Ident{Name: "nil"}, nil
	}
}

// compositeZero generates a composite literal (e.g., MyType{}) for structs and arrays.
// It uses the type string representation to construct the type identifier.
//
// t: The full type (named or literal).
// q: The qualifier for package names.
func compositeZero(t types.Type, q types.Qualifier) (ast.Expr, error) {
	// Generate string representation of the type, respecting imports via q
	typeStr := types.TypeString(t, q)

	// Parse the type string back into an AST expression to use as the Type of the CompositeLit.
	// This avoids manually reconstructing complex ASTs for named types or imported types.
	typeExpr, err := parser.ParseExpr(typeStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse type string '%s': %w", typeStr, err)
	}

	return &ast.CompositeLit{
		Type: typeExpr,
		Elts: nil, // empty elements for zero value
	}, nil
}
