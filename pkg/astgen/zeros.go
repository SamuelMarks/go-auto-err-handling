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
// It supports complex types involving imported packages by utilizing the provided qualifier
// function to correctly render package aliases (e.g., "myalias.MyStruct{}").
//
// t: The type for which to generate the zero value.
// q: An optional qualifier function to format package names in type strings.
//
// Returns the AST expression for the zero value, or an error if the type is unsupported
// (like tuples).
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
		// Named types that don't fall into the above (like named basic types w/ methods that aren't *types.Basic underlying?)
		// Actually types.Named usually resolves to underlying for structure check, but we need to handle
		// the value generation. If it's a named struct, it hits the Struct case above.
		// If it's something else we missed, report error.
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
// Only the outer type needs strict qualification. Go's composite literal syntax allows
// omitting element types in array/slice/map literals usually, but for the zero value
// of a named type `var x pkg.T`, the zero value is `pkg.T{}`.
//
// t: The full type (named or literal).
// q: The qualifier for package names.
func compositeZero(t types.Type, q types.Qualifier) (ast.Expr, error) {
	// Generate string representation of the type, respecting imports via q
	typeStr := types.TypeString(t, q)

	// Parse the type string back into an AST expression to use as the Type of the CompositeLit.
	// We use ParseExpr from go/parser which is robust for handling expressions like "pkg.Type"
	// or "[]pkg.Type".
	typeExpr, err := parser.ParseExpr(typeStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse type string '%s': %w", typeStr, err)
	}

	// Parser uses a throw-away FileSet, so these positions are invalid in the target file.
	// We must clear them to allow go/printer to format correctly and avoid weird newlines
	// in generated structs (e.g. struct{ \n }).
	ClearPositions(typeExpr)

	return &ast.CompositeLit{
		Type: typeExpr,
		Elts: nil, // empty elements for zero value
	}, nil
}

// ClearPositions recursively sets position information to token.NoPos for the AST node.
// This allows go/printer to reformat the node freely when inserted into a new file.
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
			// SelectorExpr has no explicit pos for the dot
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
