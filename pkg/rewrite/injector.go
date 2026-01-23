package rewrite

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/analysis"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/astgen"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// Injector handles the rewriting of ASTs to inject error handling logic.
// It maintains file-set context and configuration for generating code templates.
type Injector struct {
	// Fset is the token file set for positional information.
	Fset *token.FileSet
	// Pkg is the package containing the files being modified, providing type information.
	Pkg *packages.Package
	// ErrorTemplate is the string template used to generate return statements (e.g. "{return-zero}, err").
	ErrorTemplate string
}

// NewInjector creates a new Injector for the given package.
//
// pkg: The package to operate on.
// errorTemplate: Optional custom template string. If empty, defaults to "{return-zero}, err".
func NewInjector(pkg *packages.Package, errorTemplate string) *Injector {
	if errorTemplate == "" {
		errorTemplate = "{return-zero}, err"
	}
	return &Injector{
		Fset:          pkg.Fset,
		Pkg:           pkg,
		ErrorTemplate: errorTemplate,
	}
}

// RewriteFile applies specific injection points to a single file.
// It handles defer rewriting first, then processes the list of injection points.
//
// It attempts to preserve comments attached to replaced statements by copying position info
// to the new assignment node, leveraging standard AST behavior where comments are
// position-relative.
//
// file: The AST file to modify.
// points: The list of identified injection points to process.
//
// Returns true if changes were applied, and an error if processing failed.
func (i *Injector) RewriteFile(file *ast.File, points []analysis.InjectionPoint) (bool, error) {
	// 1. Rewrite defers first (catches ignored errors in defer statements)
	defersApplied, deferErr := i.RewriteDefers(file)
	if deferErr != nil {
		return false, deferErr
	}

	if len(points) == 0 {
		return defersApplied, nil
	}

	pointMap := make(map[ast.Stmt]analysis.InjectionPoint)
	for _, p := range points {
		if p.Stmt != nil {
			pointMap[p.Stmt] = p
		}
	}

	applied := false
	var err error

	// Stack to track current enclosing function signature AND AST node
	// (AST node needed to verify returns if TypesInfo is stale due to in-flight modifications)
	type funcContext struct {
		sig  *types.Signature
		decl *ast.FuncDecl // Nil if FuncLit
	}
	var funcStack []funcContext

	astutil.Apply(file, func(c *astutil.Cursor) bool {
		if err != nil {
			return false
		}

		node := c.Node()

		// Track enclosing function coverage
		switch fn := node.(type) {
		case *ast.FuncDecl:
			var sig *types.Signature
			if obj := i.Pkg.TypesInfo.ObjectOf(fn.Name); obj != nil {
				sig, _ = obj.Type().(*types.Signature)
			}
			funcStack = append(funcStack, funcContext{sig: sig, decl: fn})
		case *ast.FuncLit:
			var sig *types.Signature
			if tv, ok := i.Pkg.TypesInfo.Types[fn]; ok {
				sig, _ = tv.Type.(*types.Signature)
			}
			funcStack = append(funcStack, funcContext{sig: sig, decl: nil})
		}

		if stmt, isStmt := node.(ast.Stmt); isStmt {
			if point, exists := pointMap[stmt]; exists {
				if len(funcStack) == 0 {
					return true
				}
				ctx := funcStack[len(funcStack)-1]

				// Use context to validate/generate rewrite
				newNodes, rewriteErr := i.generateRewrite(point, ctx.sig, ctx.decl, file)
				if rewriteErr != nil {
					err = rewriteErr
					return false // Stop traversing to report error
				}

				if len(newNodes) > 0 {
					// Preserve comments:
					// The old statement might have specific line comments or doc comments.
					// We copy them to the primary replacement node.

					oldNode, isOldNode := c.Node().(ast.Node)
					newNode := newNodes[0]

					if isOldNode && newNode != nil {
						copyComments(oldNode, newNode)
					}

					c.Replace(newNodes[0])

					// Insert any subsequent nodes (e.g. if we didn't collapse into one)
					// iterate in reverse so they are inserted in correct order after the replacement
					for k := len(newNodes) - 1; k > 0; k-- {
						c.InsertAfter(newNodes[k])
					}
					applied = true
				}
			}
		}

		return true
	}, func(c *astutil.Cursor) bool {
		node := c.Node()
		switch node.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			if len(funcStack) > 0 {
				funcStack = funcStack[:len(funcStack)-1]
			}
		}
		return true
	})

	return applied || defersApplied, err
}

// copyComments copies the position info from src to dst.
// Since AST comments are position-based (in printer), assigning the source's Pos()
// to the destination's primary token position is the standard workaround to
// associate "floating" comments with the new replacement node.
//
// src: The original AST node.
// dst: The new AST node.
func copyComments(src, dst ast.Node) {
	if !src.Pos().IsValid() {
		return
	}

	// Depending on the statement type, the "anchoring" position field differs.
	// We handle the most common generated nodes.
	switch destNode := dst.(type) {
	case *ast.AssignStmt:
		// Assignment statements align comments to the definition/assignment token.
		destNode.TokPos = src.Pos()
	case *ast.ExprStmt:
		// Expressions don't have a direct position field, they rely on the expression inside.
		// But we rarely generate ExprStmt as a replacement (usually AssignStmt or IfStmt).
	case *ast.DeclStmt:
		// Ensure declaration position matches.
		if gen, ok := destNode.Decl.(*ast.GenDecl); ok {
			gen.TokPos = src.Pos()
		}
	case *ast.IfStmt:
		// If statements align comments to the 'if' keyword.
		destNode.If = src.Pos()
	}
}

// generateRewrite creates the AST nodes for assignment and error checking.
// It resolves the appropriate variable name for the error (avoiding shadowing) relative to the injection point.
//
// It attempts to generate the idiomatic `if err := call(); err != nil { ... }` syntax
// if the assignment does not introduce other variables needed in the outer scope.
//
// point: The injection point details.
// sig: The type signature of the enclosing function (from pre-analysis).
// decl: The AST declaration of the enclosing function (for current state).
// file: The file being modified.
//
// Returns a slice of statements to replace the original, or an error.
func (i *Injector) generateRewrite(point analysis.InjectionPoint, sig *types.Signature, decl *ast.FuncDecl, file *ast.File) ([]ast.Stmt, error) {
	// Check if returns error.
	// 1. Check TypesInfo (sig) - this matches state at load time.
	useSig := false
	if sig != nil && sig.Results().Len() > 0 {
		last := sig.Results().At(sig.Results().Len() - 1)
		if i.isErrorType(last.Type()) {
			useSig = true
		}
	}

	// 2. If TypesInfo didn't confirm error, check AST (decl) in case of Stale TypesInfo.
	// This happens if we appended "error" to signature in previous pass without reload.
	// The AST will show the new 'error' return type even if TypesInfo doesn't yet.
	if !useSig && decl != nil && decl.Type.Results != nil {
		list := decl.Type.Results.List
		if len(list) > 0 {
			lastField := list[len(list)-1]
			if i.isErrorExpr(lastField.Type) {
				useSig = true
			}
		}
	}

	if !useSig {
		return nil, nil // Skip - enclosing function does not return error
	}

	// Calculate unique variable name
	scope := i.getScope(point.Pos, file)
	errName := analysis.GenerateUniqueName(scope, "err")

	// Call Name Resolution
	funcName := i.resolveFuncName(point)

	// Generate Return Statement using the template
	retStmt, err := i.generateReturnStmt(sig, errName, funcName)
	if err != nil {
		return nil, err
	}

	// Generate Assignment: err := call()
	assignStmt, err := i.generateAssignment(point, errName)
	if err != nil {
		return nil, err
	}

	// Determine if we can collapse assignment into if init.
	// We only collapse if the assignment defines/assigns ONLY the error variable (or blanks _).
	// If it defines other variables (e.g. val, err := ...), those variables would be
	// scoped to the if block, breaking outer usage.
	canCollapse := true
	if as, ok := assignStmt.(*ast.AssignStmt); ok {
		for _, lhs := range as.Lhs {
			if id, ok := lhs.(*ast.Ident); ok {
				// If we find a variable that is NOT "_" and NOT our error var,
				// it's a value capture that must survive the if block.
				if id.Name != "_" && id.Name != errName {
					canCollapse = false
					break
				}
			} else {
				// Complex LHS (like *p), unlikely to be generated by generateAssignment currently,
				// but safest to assume side-effects/scoping complexity.
				canCollapse = false
				break
			}
		}
	} else {
		canCollapse = false
	}

	// Generate Check Statement: if err != nil { ... }
	ifStmt := &ast.IfStmt{
		Cond: &ast.BinaryExpr{
			X:  &ast.Ident{Name: errName},
			Op: token.NEQ,
			Y:  &ast.Ident{Name: "nil"},
		},
		Body: &ast.BlockStmt{
			List: []ast.Stmt{retStmt},
		},
	}

	if canCollapse {
		ifStmt.Init = assignStmt
		return []ast.Stmt{ifStmt}, nil
	}

	return []ast.Stmt{assignStmt, ifStmt}, nil
}

// resolveFuncName tries to determine a readable name for the function being called.
//
// point: The injection point.
//
// Returns the function name string (e.g. "Println" or "Write").
func (i *Injector) resolveFuncName(point analysis.InjectionPoint) string {
	if call := point.Call; call != nil {
		if id, ok := call.Fun.(*ast.Ident); ok {
			return id.Name
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			return sel.Sel.Name
		}
	}
	return "func" // Fallback
}

// generateReturnStmt generates the return statement inside the validation block using templates.
// It relies on post-processing for adding necessary imports.
//
// sig: The signature of the enclosing function.
// errName: The variable name of the error being returned.
// funcName: The name of the function that failed (for template context).
func (i *Injector) generateReturnStmt(sig *types.Signature, errName, funcName string) (*ast.ReturnStmt, error) {
	var zeroExprs []ast.Expr

	// If sig is nil (e.g. stale/missing), we assume 0 returns preceding error.
	if sig != nil {
		// Iterate all returns.
		// NOTE: If sig includes the 'error' (TypesInfo fresh), we skip last.
		// If sig does NOT include 'error' (TypesInfo stale), we iterate all (0 to len).
		limit := sig.Results().Len()
		if limit > 0 {
			last := sig.Results().At(limit - 1)
			if i.isErrorType(last.Type()) {
				limit-- // Skip the error, we will render it via template
			}
		}

		for idx := 0; idx < limit; idx++ {
			t := sig.Results().At(idx).Type()
			zero, err := astgen.ZeroExpr(t, nil)
			if err != nil {
				return nil, err
			}
			zeroExprs = append(zeroExprs, zero)
		}
	}

	// Render using template. We ignore the returned imports list as post-processing handles it.
	returnExprs, _, err := RenderTemplate(i.ErrorTemplate, zeroExprs, errName, funcName)
	if err != nil {
		return nil, err
	}

	return &ast.ReturnStmt{Results: returnExprs}, nil
}

// generateAssignment creates the variable assignment statement (err := ...).
// It constructs the LHS to match the return values of the call, typically assigning ignoring (_)
// everything except the error.
//
// point: The injection point.
// errName: The resolved name for the error variable.
func (i *Injector) generateAssignment(point analysis.InjectionPoint, errName string) (ast.Stmt, error) {
	call := point.Call
	var callResultSig *types.Tuple

	if tv, ok := i.Pkg.TypesInfo.Types[call]; ok {
		if tuple, ok := tv.Type.(*types.Tuple); ok {
			callResultSig = tuple
		} else {
			// Single return value simulation
			vars := []*types.Var{types.NewVar(token.NoPos, nil, "", tv.Type)}
			callResultSig = types.NewTuple(vars...)
		}
	} else {
		return nil, fmt.Errorf("type info missing for call expression")
	}

	var lhs []ast.Expr

	if point.Assign != nil {
		// Reuse existing LHS expressions, patching the error position
		for idx, expr := range point.Assign.Lhs {
			isLast := idx == len(point.Assign.Lhs)-1
			if isLast {
				lhs = append(lhs, &ast.Ident{Name: errName})
			} else {
				lhs = append(lhs, expr)
			}
		}
	} else {
		// Create new LHS: _, _, err :=
		for idx := 0; idx < callResultSig.Len()-1; idx++ {
			lhs = append(lhs, &ast.Ident{Name: "_"})
		}
		lhs = append(lhs, &ast.Ident{Name: errName})
	}

	tok := token.DEFINE

	return &ast.AssignStmt{
		Lhs: lhs,
		Tok: tok,
		Rhs: []ast.Expr{call},
	}, nil
}

// isErrorReturningCall checks if the call returns exactly one value which is an error.
//
// call: The call expression.
func (i *Injector) isErrorReturningCall(call *ast.CallExpr) bool {
	if typesInfo := i.Pkg.TypesInfo; typesInfo != nil {
		tv, ok := typesInfo.Types[call]
		if !ok {
			return false
		}

		// Must not be a tuple (multiple returns)
		if _, isTuple := tv.Type.(*types.Tuple); isTuple {
			return false
		}

		// Check if it is error
		return i.isErrorType(tv.Type)
	}
	return false
}

// isErrorType checks if the type is the "error" interface.
// It performs a robust check by comparing against the universe "error" type directly
// to avoid issues with custom types named "error" or string matching ambiguity.
//
// t: The type to check.
func (i *Injector) isErrorType(t types.Type) bool {
	if t == nil {
		return false
	}
	// Strict Type Check: Compare against the distinct 'error' object in the Universe scope.
	// This handles standard 'error' correctly even if aliased.
	if types.Identical(t, types.Universe.Lookup("error").Type()) {
		return true
	}

	// Fallback: Check string representation for standard "error" or "builtin.error".
	// This helps in cases where the type info might be incomplete (e.g. some test mocks)
	// or where Universe hasn't been unified.
	name := t.String()
	return name == "error" || name == "builtin.error"
}

// isErrorExpr checks if the AST expression looks like "error".
// This is an AST-level fallback when Type info is not available.
//
// expr: The AST expression.
func (i *Injector) isErrorExpr(expr ast.Expr) bool {
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name == "error"
	}
	return false
}

// getScope attempts to find the innermost scope enclosing the given position.
// It traverses the AST path upwards until a node with an associated scope is found.
//
// pos: The position in the file.
// file: The start file.
func (i *Injector) getScope(pos token.Pos, file *ast.File) *types.Scope {
	path, _ := astutil.PathEnclosingInterval(file, pos, pos)
	for _, node := range path {
		if scope := i.Pkg.TypesInfo.Scopes[node]; scope != nil {
			return scope
		}
		// Fallback: PathEnclosingInterval includes FuncDecl, but scopes are often keyed on FuncType.
		if fd, ok := node.(*ast.FuncDecl); ok {
			if fd.Type != nil {
				if scope := i.Pkg.TypesInfo.Scopes[fd.Type]; scope != nil {
					return scope
				}
			}
			// Robustness fallback: Look up function Object
			if obj := i.Pkg.TypesInfo.ObjectOf(fd.Name); obj != nil {
				if fn, ok := obj.(*types.Func); ok {
					return fn.Scope()
				}
			}
		}
		// Also handle function literals
		if fl, ok := node.(*ast.FuncLit); ok && fl.Type != nil {
			if scope := i.Pkg.TypesInfo.Scopes[fl.Type]; scope != nil {
				return scope
			}
		}
	}
	// Fallback to package scope if available.
	if i.Pkg.Types != nil {
		return i.Pkg.Types.Scope()
	}
	return nil
}
