package runner

import (
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// FuncContext represents the function (Declaration or Literal) enclosing a specific point in code.
// It provides access to the semantic signature and the syntactic AST node required for refactoring.
type FuncContext struct {
	// Sig is the type signature of the function, resolved from type information.
	Sig *types.Signature
	// Decl is set if the enclosing function is a named declaration (e.g. func Foo() {}).
	Decl *ast.FuncDecl
	// Lit is set if the enclosing function is a literal (e.g. func() {}).
	Lit *ast.FuncLit
	// Node is the AST node corresponding to the function (either Decl or Lit).
	Node ast.Node
}

// IsLiteral reports whether the function context is an anonymous function literal.
func (fc *FuncContext) IsLiteral() bool {
	return fc.Lit != nil
}

// FindEnclosingFunc resolves the nearest function wrapping the provided position in the file.
// It traverses the AST upwards from the position to find either a FuncDecl or a FuncLit.
// It requires specific type information to be present in the package to resolve signatures.
//
// pkg: The package containing the file, populated with syntax and type info.
// file: The AST file associated with the position.
// pos: The token position to start the search from.
//
// Returns nil if no function context encloses the position or if type information is missing.
func FindEnclosingFunc(pkg *packages.Package, file *ast.File, pos token.Pos) *FuncContext {
	path, _ := astutil.PathEnclosingInterval(file, pos, pos)

	for _, node := range path {
		switch fn := node.(type) {
		case *ast.FuncDecl:
			// Found a named function declaration.
			// Look up the definition object to retrieve the signature.
			if obj := pkg.TypesInfo.ObjectOf(fn.Name); obj != nil {
				if sig, ok := obj.Type().(*types.Signature); ok {
					return &FuncContext{
						Sig:  sig,
						Decl: fn,
						Node: fn,
					}
				}
			}
			// If we found a decl but no type info, we can't safely proceed with
			// semantics-aware refactoring, so we treat it as not found.
			return nil

		case *ast.FuncLit:
			// Found an anonymous function literal.
			// Look up the type of the expression itself.
			if tv, ok := pkg.TypesInfo.Types[fn]; ok {
				if sig, ok := tv.Type.(*types.Signature); ok {
					return &FuncContext{
						Sig:  sig,
						Lit:  fn,
						Node: fn,
					}
				}
			}
			// Similar to declarations, require valid type info.
			// If incorrect, we continue searching up? No, the *immediate* parent
			// is the only valid context for a return statement.
			return nil
		}
	}

	return nil
}
