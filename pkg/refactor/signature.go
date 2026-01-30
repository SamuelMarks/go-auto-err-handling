package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/astgen"
	"github.com/dave/dst"
	"golang.org/x/tools/go/ast/astutil"
)

// AddErrorToSignature modifies a function declaration signature to include an error return type.
// It performs smart anonymization of return values to prefer idiomatic Go signatures.
//
// fset: The FileSet for position handling.
// decl: The function declaration to modify.
//
// Returns true if the signature was modified.
func AddErrorToSignature(fset *token.FileSet, decl *ast.FuncDecl) (bool, error) {
	if decl == nil {
		return false, fmt.Errorf("function declaration is nil")
	}

	// Initialize Results if nil (e.g., void function)
	if decl.Type.Results == nil {
		decl.Type.Results = &ast.FieldList{}
	}

	// Clear positions to allow reformatting
	decl.Type.Results.Opening = token.NoPos
	decl.Type.Results.Closing = token.NoPos

	// 1. Analyze Context
	hasNamedReturns := false
	for _, field := range decl.Type.Results.List {
		if len(field.Names) > 0 {
			hasNamedReturns = true
			break
		}
	}

	hasNakedReturns := false
	if hasNamedReturns {
		hasNakedReturns = scanForNakedReturns(decl.Body)
	}

	// 2. Strategy Decision
	preserveNames := hasNakedReturns
	wasVoid := len(decl.Type.Results.List) == 0

	// 3. Apply Anonymization (if applicable)
	var injectedStmts []ast.Stmt

	if hasNamedReturns && !preserveNames {
		var newResultList []*ast.Field

		for _, field := range decl.Type.Results.List {
			typeExpr := field.Type

			// Handle multi-name fields like "func() (a, b int)"
			for _, name := range field.Names {
				// Inject var if used
				if isNameUsed(decl.Body, name.Name) {
					declStmt := &ast.DeclStmt{
						Decl: &ast.GenDecl{
							Tok: token.VAR,
							Specs: []ast.Spec{
								&ast.ValueSpec{
									Names: []*ast.Ident{{Name: name.Name}},
									Type:  typeExpr,
								},
							},
						},
					}
					injectedStmts = append(injectedStmts, declStmt)
				}
				// Append anonymous field for THIS specific variable types.
				newResultList = append(newResultList, &ast.Field{
					Type: typeExpr, // Sharing pointer is fine for AST logic
				})
			}
		}

		decl.Type.Results.List = newResultList
		hasNamedReturns = false
	}

	// 4. Prepend Injected Variables (if any)
	if len(injectedStmts) > 0 {
		decl.Body.List = append(injectedStmts, decl.Body.List...)
	}

	// 5. Append 'error' field
	errorType := &ast.Ident{Name: "error"}
	var newField *ast.Field

	if hasNamedReturns {
		// Calculate safe name avoiding collisions
		usedNames := make(map[string]bool)
		if decl.Type.Params != nil {
			for _, f := range decl.Type.Params.List {
				for _, n := range f.Names {
					usedNames[n.Name] = true
				}
			}
		}
		for _, f := range decl.Type.Results.List {
			for _, n := range f.Names {
				usedNames[n.Name] = true
			}
		}

		baseName := "err"
		name := baseName
		count := 1
		for usedNames[name] {
			name = fmt.Sprintf("%s%d", baseName, count)
			count++
		}

		newField = &ast.Field{
			Names: []*ast.Ident{{Name: name}},
			Type:  errorType,
		}
	} else {
		newField = &ast.Field{
			Type: errorType,
		}
	}

	decl.Type.Results.List = append(decl.Type.Results.List, newField)

	// 6. Update Return Statements
	astutil.Apply(decl.Body, func(c *astutil.Cursor) bool {
		node := c.Node()

		// Skip nested function literals or declarations
		if _, isFuncLit := node.(*ast.FuncLit); isFuncLit {
			return false
		}
		if _, isFuncDecl := node.(*ast.FuncDecl); isFuncDecl {
			return false
		}

		// Handle Return Statements
		if ret, isRet := node.(*ast.ReturnStmt); isRet {
			isNaked := hasNamedReturns && len(ret.Results) == 0

			if !isNaked && (len(ret.Results) > 0 || wasVoid) {
				// We append explicit nil. Semantic zero values for other types would be better,
				// but without type info here, we rely on the fact we only appended error.
				ret.Results = append(ret.Results, &ast.Ident{Name: "nil"})
			}
			return false
		}
		return true
	}, nil)

	// 7. Handle Void Fallthrough
	if wasVoid {
		needsAppend := true
		if len(decl.Body.List) > 0 {
			if _, ok := decl.Body.List[len(decl.Body.List)-1].(*ast.ReturnStmt); ok {
				needsAppend = false
			}
		}
		if needsAppend {
			decl.Body.List = append(decl.Body.List, &ast.ReturnStmt{
				Results: []ast.Expr{&ast.Ident{Name: "nil"}},
			})
		}
	}

	return true, nil
}

// AddErrorToSignatureDST modifies a DST function declaration signature to include an error return type.
// It leverages dst.FieldList for automatic formatting.
//
// decl: The DST function declaration.
//
// Returns true if changed.
func AddErrorToSignatureDST(decl *dst.FuncDecl) (bool, error) {
	if decl == nil {
		return false, fmt.Errorf("function declaration is nil")
	}

	if decl.Type.Results == nil {
		decl.Type.Results = &dst.FieldList{}
	}

	// 1. Analyze Context
	hasNamedReturns := false
	for _, field := range decl.Type.Results.List {
		if len(field.Names) > 0 {
			hasNamedReturns = true
			break
		}
	}

	hasNakedReturns := false
	if hasNamedReturns {
		hasNakedReturns = scanForNakedReturnsDST(decl.Body)
	}

	preserveNames := hasNakedReturns
	wasVoid := len(decl.Type.Results.List) == 0

	// 2. Anonymization Strategy
	var injectedStmts []dst.Stmt

	if hasNamedReturns && !preserveNames {
		var newResultList []*dst.Field

		for _, field := range decl.Type.Results.List {
			typeExpr := field.Type

			for _, name := range field.Names {
				// Check usage to decide whether to declare a var or just drop the name
				if isNameUsedDST(decl.Body, name.Name) {
					declStmt := &dst.DeclStmt{
						Decl: &dst.GenDecl{
							Tok: token.VAR,
							Specs: []dst.Spec{
								&dst.ValueSpec{
									Names: []*dst.Ident{dst.NewIdent(name.Name)},
									Type:  dst.Clone(typeExpr).(dst.Expr),
								},
							},
						},
					}
					injectedStmts = append(injectedStmts, declStmt)
				}
				// Append anonymous field result
				newResultList = append(newResultList, &dst.Field{
					Type: dst.Clone(typeExpr).(dst.Expr),
				})
			}
		}
		decl.Type.Results.List = newResultList
		hasNamedReturns = false
	}

	if len(injectedStmts) > 0 {
		decl.Body.List = append(injectedStmts, decl.Body.List...)
	}

	// 3. Append error field
	errorType := dst.NewIdent("error")
	var newField *dst.Field

	if hasNamedReturns {
		usedNames := make(map[string]bool)
		for _, f := range decl.Type.Params.List {
			for _, n := range f.Names {
				usedNames[n.Name] = true
			}
		}
		for _, f := range decl.Type.Results.List {
			for _, n := range f.Names {
				usedNames[n.Name] = true
			}
		}

		baseName := "err"
		name := baseName
		count := 1
		for usedNames[name] {
			name = fmt.Sprintf("%s%d", baseName, count)
			count++
		}

		newField = &dst.Field{
			Names: []*dst.Ident{dst.NewIdent(name)},
			Type:  errorType,
		}
	} else {
		newField = &dst.Field{
			Type: errorType,
		}
	}

	decl.Type.Results.List = append(decl.Type.Results.List, newField)

	// 4. Update Returns
	dst.Inspect(decl.Body, func(n dst.Node) bool {
		// Stop at closures
		if _, isFuncLit := n.(*dst.FuncLit); isFuncLit {
			return false
		}

		if ret, ok := n.(*dst.ReturnStmt); ok {
			isNaked := hasNamedReturns && len(ret.Results) == 0
			// If not naked, append zero value for error (nil)
			if !isNaked && (len(ret.Results) > 0 || wasVoid) {
				// We use "nil" because 'error' interface zero value is always nil.
				// We do not need robust astgen logic here unless we are adding non-interface types.
				ret.Results = append(ret.Results, dst.NewIdent("nil"))
			}
			return false
		}
		return true
	})

	// 5. Void Fallthrough handling
	// If function was void, it might rely on implicit return at end of block.
	// We must add explicit return nil.
	if wasVoid {
		needsAppend := true
		if len(decl.Body.List) > 0 {
			if _, ok := decl.Body.List[len(decl.Body.List)-1].(*dst.ReturnStmt); ok {
				needsAppend = false
			}
		}
		if needsAppend {
			// Using astgen for cleanliness, though explicit ident is fine for error
			val, _ := astgen.ZeroExprDST(types.Universe.Lookup("error").Type(), astgen.ZeroCtx{})
			decl.Body.List = append(decl.Body.List, &dst.ReturnStmt{
				Results: []dst.Expr{val},
			})
		}
	}

	return true, nil
}

// EnsureNamedReturns checks AST function declarations for unnamed return values and names them.
// This is critical for rewrites that introduce defer closures which capture return values.
//
// fset: FileSet for position information.
// decl: The candidate function declaration.
// info: Type info package (optional, allows smarter naming).
func EnsureNamedReturns(fset *token.FileSet, decl *ast.FuncDecl, info *types.Info) (bool, error) {
	if decl == nil {
		return false, fmt.Errorf("function declaration is nil")
	}
	if decl.Type.Results == nil || len(decl.Type.Results.List) == 0 {
		return false, nil
	}

	results := decl.Type.Results.List
	if len(results[0].Names) > 0 {
		return false, nil
	}

	changeMade := false
	nameCounts := make(map[string]int)
	total := len(results)

	for i, field := range results {
		if len(field.Names) == 0 {
			var baseName string
			if info != nil {
				if t, ok := info.Types[field.Type]; ok {
					baseName = NameForType(t.Type)
				}
			}
			if baseName == "" || baseName == "v" {
				baseName = NameForExpr(field.Type)
			}

			isLast := i == total-1
			if isLast && isErrorExpr(field.Type) {
				baseName = "err"
			}

			name := baseName
			if count, seen := nameCounts[baseName]; seen {
				name = fmt.Sprintf("%s%d", baseName, count)
				nameCounts[baseName] = count + 1
			} else {
				nameCounts[baseName] = 1
			}

			field.Names = []*ast.Ident{{Name: name}}
			changeMade = true
		}
	}
	return changeMade, nil
}

// EnsureNamedReturnsDST checks DST function declarations for unnamed return values and names them.
// It uses heuristics for naming since TypeInfo is often not associated with DST nodes.
func EnsureNamedReturnsDST(decl *dst.FuncDecl) (bool, error) {
	if decl == nil {
		return false, fmt.Errorf("function declaration is nil")
	}
	if decl.Type.Results == nil || len(decl.Type.Results.List) == 0 {
		return false, nil
	}

	results := decl.Type.Results.List
	if len(results[0].Names) > 0 {
		return false, nil
	}

	changeMade := false
	nameCounts := make(map[string]int)
	total := len(results)

	for i, field := range results {
		if len(field.Names) == 0 {
			baseName := nameForDstExpr(field.Type)

			isLast := i == total-1
			if isLast && isErrorDstExpr(field.Type) {
				baseName = "err"
			}

			name := baseName
			if count, seen := nameCounts[baseName]; seen {
				name = fmt.Sprintf("%s%d", baseName, count)
				nameCounts[baseName] = count + 1
			} else {
				nameCounts[baseName] = 1
			}

			field.Names = []*dst.Ident{dst.NewIdent(name)}
			changeMade = true
		}
	}
	return changeMade, nil
}

// Helpers

func scanForNakedReturns(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if _, ok := n.(*ast.FuncDecl); ok {
			return false
		}
		if ret, ok := n.(*ast.ReturnStmt); ok {
			if len(ret.Results) == 0 {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func scanForNakedReturnsDST(body *dst.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	dst.Inspect(body, func(n dst.Node) bool {
		if found {
			return false
		}
		if _, ok := n.(*dst.FuncLit); ok {
			return false
		}
		if ret, ok := n.(*dst.ReturnStmt); ok {
			if len(ret.Results) == 0 {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func isNameUsed(body *ast.BlockStmt, name string) bool {
	if body == nil {
		return false
	}
	used := false
	ast.Inspect(body, func(n ast.Node) bool {
		if used {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if _, ok := n.(*ast.FuncDecl); ok {
			return false
		}
		if id, ok := n.(*ast.Ident); ok {
			if id.Name == name {
				used = true
				return false
			}
		}
		return true
	})
	return used
}

func isNameUsedDST(body *dst.BlockStmt, name string) bool {
	if body == nil {
		return false
	}
	used := false
	dst.Inspect(body, func(n dst.Node) bool {
		if used {
			return false
		}
		if _, ok := n.(*dst.FuncLit); ok {
			return false
		}
		if id, ok := n.(*dst.Ident); ok {
			if id.Name == name {
				used = true
				return false
			}
		}
		return true
	})
	return used
}

func isErrorExpr(expr ast.Expr) bool {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name == "error"
	}
	return false
}

func isErrorDstExpr(expr dst.Expr) bool {
	if ident, ok := expr.(*dst.Ident); ok {
		return ident.Name == "error"
	}
	return false
}

// nameForDstExpr mimics NameForExpr logic but for DST nodes.
// It provides heuristics for naming variables based on types (e.g. *http.Client -> client).
func nameForDstExpr(expr dst.Expr) string {
	if expr == nil {
		return "v"
	}

	core := unwrapDstExpr(expr)

	var typeName string
	switch t := core.(type) {
	case *dst.Ident:
		typeName = t.Name
	case *dst.SelectorExpr:
		typeName = t.Sel.Name
		// Heuristics for pkg.Type using keys from naming.go's defaultMap
		if x, ok := t.X.(*dst.Ident); ok {
			full := x.Name + "." + t.Sel.Name
			if val, ok := defaultTypeMap[full]; ok {
				return val
			}
		}
	default:
		return "v"
	}

	// defaultTypeMap is defined in naming.go and is package-private to refactor
	if val, ok := defaultTypeMap[typeName]; ok {
		return val
	}

	// Reuse toVariableName from naming.go
	return toVariableName(typeName)
}

func unwrapDstExpr(e dst.Expr) dst.Expr {
	switch u := e.(type) {
	case *dst.StarExpr:
		return unwrapDstExpr(u.X)
	case *dst.ArrayType:
		return unwrapDstExpr(u.Elt)
	}
	return e
}
