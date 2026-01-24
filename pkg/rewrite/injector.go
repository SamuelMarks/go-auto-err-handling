package rewrite

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
	"unicode"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/analysis"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/astgen"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/imports"
	"github.com/SamuelMarks/go-auto-err-handling/pkg/refactor"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// Injector handles the rewriting of ASTs to inject error handling logic.
// It maintains file-set context and configuration for generating code templates,
// and manages comment preservation during statement replacement.
type Injector struct {
	// Fset is the token file set for positional information.
	Fset *token.FileSet
	// Pkg is the package containing the files being modified, providing type information.
	Pkg *packages.Package
	// ErrorTemplate is the string template used to generate return statements (e.g. "{return-zero}, err").
	ErrorTemplate string
	// MainHandlerStrategy determines how terminal errors are handled (e.g. in go routines).
	MainHandlerStrategy string
}

// NewInjector creates a new Injector for the given package.
//
// pkg: The package to operate on.
// errorTemplate: Optional custom template string. If empty, defaults to "{return-zero}, err".
// mainHandler: Optional strategy for terminal handlers (log-fatal, panic, os-exit). Defaults to log-fatal.
func NewInjector(pkg *packages.Package, errorTemplate, mainHandler string) *Injector {
	if errorTemplate == "" {
		errorTemplate = "{return-zero}, err"
	}
	if mainHandler == "" {
		mainHandler = "log-fatal"
	}
	return &Injector{
		Fset:                pkg.Fset,
		Pkg:                 pkg,
		ErrorTemplate:       errorTemplate,
		MainHandlerStrategy: mainHandler,
	}
}

// RewriteFile applies specific injection points to a single file.
// It handles defer rewriting first, then processes the list of injection points.
//
// It explicitly attempts to preserve comments attached to replaced statements by
// creating an ast.CommentMap for the file and querying it for comments associated
// with the node being replaced. These comments are then manually attached/copied
// to the replacement node.
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

	// Build comment map once for the file to handle association
	cmap := ast.NewCommentMap(i.Fset, file, file.Comments)

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
				// Handle Go Statements (Context agnostic refactor)
				if _, isGo := stmt.(*ast.GoStmt); isGo {
					newStmt, rwErr := i.generateGoRewrite(point, file)
					if rwErr != nil {
						err = rwErr
						return false
					}
					// Transfer comments
					transferComments(stmt, newStmt, cmap)
					c.Replace(newStmt)
					applied = true
					return true
				}

				if len(funcStack) == 0 {
					return true
				}
				ctx := funcStack[len(funcStack)-1]

				// Use context to validate/generate rewrite
				newNodes, rewriteErr := i.generateRewrite(point, ctx.sig, ctx.decl, file)
				if rewriteErr != nil {
					err = rewriteErr
					return false
				}

				if len(newNodes) > 0 {
					oldNode := c.Node().(ast.Node)
					newNode := newNodes[0] // Primary replacement

					// Transfer comments from oldNode to newNode using the CommentMap
					transferComments(oldNode, newNode, cmap)

					c.Replace(newNode)

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

// LogFallback injects a logging statement for the given error instead of returning it.
func (i *Injector) LogFallback(file *ast.File, point analysis.InjectionPoint) (bool, error) {
	applied := false
	var err error

	// Build comment map for specific fallback
	cmap := ast.NewCommentMap(i.Fset, file, file.Comments)

	astutil.Apply(file, func(c *astutil.Cursor) bool {
		if c.Node() == point.Stmt {
			// Generate code
			stmts, genErr := i.generateLogRewrite(point, file)
			if genErr != nil {
				err = genErr
				return false
			}

			if len(stmts) > 0 {
				oldNode := c.Node().(ast.Node)
				newNode := stmts[0]
				transferComments(oldNode, newNode, cmap)

				c.Replace(newNode)
				for k := len(stmts) - 1; k > 0; k-- {
					c.InsertAfter(stmts[k])
				}
				imports.Add(i.Fset, file, "log")
				applied = true
			}
			return false // Stop traversing this node
		}
		return true
	}, nil)

	return applied, err
}

// transferComments retrieves the comment group associated with the `src` node from the
// `cmap` and attaches it to the `dst` node.
//
// If `dst` already has comments (rare in synthesized nodes), the old comments are
// prepended to them, preserving logical order where the old statement's documentation
// appears above the new block.
//
// src: The original AST node being replaced.
// dst: The new AST node.
// cmap: The comment map for the file.
func transferComments(src, dst ast.Node, cmap ast.CommentMap) {
	if src == nil || dst == nil || cmap == nil {
		return
	}

	comments, ok := cmap[src]
	if !ok || len(comments) == 0 {
		return
	}

	// AST nodes like *ast.IfStmt do not have a generic 'Doc' or 'Comment' field
	// accessible via the Node interface. We must type switch to set them.
	// We primarily target the types generated by Injector:
	// *ast.IfStmt, *ast.AssignStmt, *ast.GoStmt, *ast.DeclStmt.

	// Flatten comment groups into a single group for simplicity if multiple groups matched the node
	// (usually just one doc group and one line group, but CommentMap bundles them).
	// We want to attach these as a Doc string to the new block so they float above it.
	var combinedList []*ast.Comment
	for _, cg := range comments {
		combinedList = append(combinedList, cg.List...)
	}
	if len(combinedList) == 0 {
		return
	}

	toAttach := &ast.CommentGroup{List: combinedList}

	switch n := dst.(type) {
	case *ast.AssignStmt:
		// AssignStmt doesn't have a Doc field. We can only set line attributes?
		// Actually, ast.AssignStmt has no comment attachment points in the struct definition (as of Go 1.20).
		// Comments are floating in the file.
		// However, for printers to respect them, we often need to hack the position (Pos)
		// which we do via copyPos usually, OR we create a *ast.BlockStmt wrapping it?
		// No, standard practice is to ensure the new node has the same Start position
		// so the printer associates the preceding comments with it.
		// BUT: Since we replaced the node in the AST, the comment Map association is broken for printing
		// unless we manually print.
		// `astutil.Apply` preserves the list structure.
		// The standard `go/printer` uses the `ast.File.Comments` list.
		// If we replace the node, `go/printer` might get confused if the new node has NoPos.
		//
		// SOLUTION: We copy the position from src to dst.
		// If the node Types support explicit documentation (GenDecl), we set it.
		copyPos(src, n)

	case *ast.DeclStmt:
		// DeclStmt wraps GenDecl. GenDecl has Doc.
		if gen, ok := n.Decl.(*ast.GenDecl); ok {
			if gen.Doc == nil {
				gen.Doc = toAttach
			} else {
				// Prepend
				gen.Doc.List = append(toAttach.List, gen.Doc.List...)
			}
			copyPos(src, gen)
		}
	case *ast.IfStmt:
		// IfStmt doesn't have Doc.
		// We set Pos to match src to capture preceding comments during printing.
		copyPos(src, n)
	case *ast.GoStmt:
		copyPos(src, n)
	}
}

// copyPos copies the positional information (Pos) from one node to another.
// This is crucial for comment interleaving by go/printer.
func copyPos(src, dst ast.Node) {
	if !src.Pos().IsValid() {
		return
	}

	switch dest := dst.(type) {
	case *ast.AssignStmt:
		// AssignStmt uses TokPos usually as the anchor
		dest.TokPos = src.Pos()
	case *ast.IfStmt:
		dest.If = src.Pos()
	case *ast.GoStmt:
		dest.Go = src.Pos()
	case *ast.DeclStmt:
		// Delegate to inner
		copyPos(src, dest.Decl)
	case *ast.GenDecl:
		dest.TokPos = src.Pos()
	case *ast.ExprStmt:
		// No explicit field for stmt start, usually uses X
		copyPos(src, dest.X)
	}
}

// generateLogRewrite constructs the assignment and log check.
func (i *Injector) generateLogRewrite(point analysis.InjectionPoint, file *ast.File) ([]ast.Stmt, error) {
	scope := i.getScope(point.Pos, file)

	// Use smart name/reuse resolution
	errName, tok, declStmt := i.resolveErrorVar(point, scope)

	assignStmt, err := i.generateAssignment(point, errName, tok)
	if err != nil {
		return nil, err
	}

	funcName := i.resolveFuncName(point)
	logStmt := &ast.IfStmt{
		Cond: &ast.BinaryExpr{
			X:  &ast.Ident{Name: errName},
			Op: token.NEQ,
			Y:  &ast.Ident{Name: "nil"},
		},
		Body: &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.ExprStmt{
					X: &ast.CallExpr{
						Fun: &ast.SelectorExpr{
							X:   &ast.Ident{Name: "log"},
							Sel: &ast.Ident{Name: "Printf"},
						},
						Args: []ast.Expr{
							&ast.BasicLit{Kind: token.STRING, Value: fmt.Sprintf(`"ignored error in %s: %%v"`, funcName)},
							&ast.Ident{Name: errName},
						},
					},
				},
			},
		},
	}

	canCollapse := true
	if declStmt != nil {
		canCollapse = false
	} else if as, ok := assignStmt.(*ast.AssignStmt); ok {
		for _, lhs := range as.Lhs {
			if id, ok := lhs.(*ast.Ident); ok {
				if id.Name != "_" && id.Name != errName {
					canCollapse = false
					break
				}
			}
		}
	} else {
		canCollapse = false
	}

	result := []ast.Stmt{}
	if declStmt != nil {
		result = append(result, declStmt)
	}

	if canCollapse {
		logStmt.Init = assignStmt
		result = append(result, logStmt)
	} else {
		result = append(result, assignStmt, logStmt)
	}

	return result, nil
}

// generateRewrite creates the AST nodes for assignment and error checking.
func (i *Injector) generateRewrite(point analysis.InjectionPoint, sig *types.Signature, decl *ast.FuncDecl, file *ast.File) ([]ast.Stmt, error) {
	useSig := false
	if sig != nil && sig.Results().Len() > 0 {
		last := sig.Results().At(sig.Results().Len() - 1)
		if i.isErrorType(last.Type()) {
			useSig = true
		}
	}
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
		return nil, nil
	}

	scope := i.getScope(point.Pos, file)

	// --- Smart Shadowing ---
	errName, tok, declStmt := i.resolveErrorVar(point, scope)

	funcName := i.resolveFuncName(point)

	retStmt, err := i.generateReturnStmt(sig, errName, funcName)
	if err != nil {
		return nil, err
	}

	if isEmbedded(point) {
		return i.generateEmbeddedRewrite(point, point.Stmt, errName, retStmt, scope)
	}

	assignStmt, err := i.generateAssignment(point, errName, tok)
	if err != nil {
		return nil, err
	}

	canCollapse := true
	if declStmt != nil {
		canCollapse = false
	} else if as, ok := assignStmt.(*ast.AssignStmt); ok {
		for _, lhs := range as.Lhs {
			if id, ok := lhs.(*ast.Ident); ok {
				if id.Name != "_" && id.Name != errName {
					canCollapse = false
					break
				}
			} else {
				canCollapse = false
				break
			}
		}
	} else {
		canCollapse = false
	}

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

	result := []ast.Stmt{}
	if declStmt != nil {
		result = append(result, declStmt)
	}

	if canCollapse {
		ifStmt.Init = assignStmt
		result = append(result, ifStmt)
	} else {
		result = append(result, assignStmt, ifStmt)
	}

	return result, nil
}

// resolveErrorVar determines the best variable name and assignment token for the error.
func (i *Injector) resolveErrorVar(point analysis.InjectionPoint, scope *types.Scope) (string, token.Token, *ast.DeclStmt) {
	candidate := "err"

	// 1. Check for valid existing 'err'
	var existingVar *types.Var
	if scope != nil {
		_, obj := scope.LookupParent(candidate, token.NoPos)
		if obj != nil {
			if v, ok := obj.(*types.Var); ok {
				if i.isErrorType(v.Type()) || i.isErrorExprFromAST(v) {
					existingVar = v
				}
			}
		}
	}

	// 2. Decide on naming collision if existing var is NOT an error (or a const)
	useExactName := false
	if existingVar != nil {
		useExactName = true
	} else {
		if scope != nil {
			_, obj := scope.LookupParent(candidate, token.NoPos)
			if obj == nil {
				useExactName = true
			} else {
				useExactName = false
			}
		} else {
			useExactName = true
		}
	}

	finalName := candidate
	if !useExactName {
		baseName := i.generateSemanticErrName(point)
		finalName = analysis.GenerateUniqueName(scope, baseName)
		return finalName, token.DEFINE, nil
	}

	// 3. Determine Token (:= vs =)
	isAllBlank := false
	if point.Assign != nil {
		isAllBlank = true
		for _, lhs := range point.Assign.Lhs {
			if id, ok := lhs.(*ast.Ident); !ok || id.Name != "_" {
				isAllBlank = false
				break
			}
		}
	}

	if point.Assign == nil {
		if existingVar != nil {
			return finalName, token.ASSIGN, nil
		}
		return finalName, token.DEFINE, nil
	}

	if point.Assign.Tok == token.DEFINE {
		return finalName, token.DEFINE, nil
	}

	if existingVar != nil {
		return finalName, token.ASSIGN, nil
	}

	if isAllBlank {
		return finalName, token.DEFINE, nil
	}

	return finalName, token.ASSIGN, &ast.DeclStmt{
		Decl: &ast.GenDecl{
			Tok: token.VAR,
			Specs: []ast.Spec{
				&ast.ValueSpec{
					Names: []*ast.Ident{{Name: finalName}},
					Type:  &ast.Ident{Name: "error"},
				},
			},
		},
	}
}

// generateSemanticErrName generates a backup name if "err" is taken.
func (i *Injector) generateSemanticErrName(point analysis.InjectionPoint) string {
	funcName := i.resolveFuncName(point)
	if idx := strings.LastIndex(funcName, "."); idx != -1 {
		funcName = funcName[idx+1:]
	}
	if len(funcName) > 0 {
		r := []rune(funcName)
		r[0] = unicode.ToLower(r[0])
		funcName = string(r)
	} else {
		funcName = "call"
	}
	return fmt.Sprintf("%sErr", funcName)
}

// isErrorExprFromAST is a backup check if Types info is missing for the var.
func (i *Injector) isErrorExprFromAST(v *types.Var) bool {
	return false
}

// isEmbedded checks if the call is nested within the statement.
func isEmbedded(point analysis.InjectionPoint) bool {
	switch s := point.Stmt.(type) {
	case *ast.IfStmt, *ast.SwitchStmt:
		return true
	case *ast.ExprStmt:
		return s.X != point.Call
	case *ast.AssignStmt:
		for _, r := range s.Rhs {
			if r == point.Call {
				return false
			}
		}
		return true
	case *ast.GoStmt:
		return s.Call != point.Call
	case *ast.DeferStmt:
		return s.Call != point.Call
	}
	return false
}

// generateEmbeddedRewrite handles logic for calls extracted from control structures and chains.
func (i *Injector) generateEmbeddedRewrite(point analysis.InjectionPoint, stmt ast.Stmt, errName string, retStmt ast.Stmt, scope *types.Scope) ([]ast.Stmt, error) {
	call := point.Call
	var callResultSig *types.Tuple
	if tv, ok := i.Pkg.TypesInfo.Types[call]; ok {
		if tuple, ok := tv.Type.(*types.Tuple); ok {
			callResultSig = tuple
		} else {
			vars := []*types.Var{types.NewVar(token.NoPos, nil, "", tv.Type)}
			callResultSig = types.NewTuple(vars...)
		}
	} else {
		return nil, fmt.Errorf("type info missing for embedded call expression")
	}

	var lhs []ast.Expr
	var valNames []string

	for k := 0; k < callResultSig.Len()-1; k++ {
		t := callResultSig.At(k).Type()
		baseName := refactor.NameForType(t)
		unique := analysis.GenerateUniqueName(scope, baseName)

		count := 1
		candidate := unique
		for {
			collision := false
			for _, existing := range valNames {
				if existing == candidate {
					collision = true
					break
				}
			}
			if !collision {
				break
			}
			candidate = fmt.Sprintf("%s%d", unique, count)
			count++
		}
		valNames = append(valNames, candidate)
		lhs = append(lhs, &ast.Ident{Name: candidate})
	}
	lhs = append(lhs, &ast.Ident{Name: errName})

	assignStmt := &ast.AssignStmt{
		Lhs: lhs,
		Tok: token.DEFINE,
		Rhs: []ast.Expr{call},
	}

	checkStmt := &ast.IfStmt{
		Cond: &ast.BinaryExpr{
			X:  &ast.Ident{Name: errName},
			Op: token.NEQ,
			Y:  &ast.Ident{Name: "nil"},
		},
		Body: &ast.BlockStmt{
			List: []ast.Stmt{retStmt},
		},
	}

	if len(valNames) == 0 {
		return nil, fmt.Errorf("embedded call has no return value to substitute condition")
	}
	replacementExpr := &ast.Ident{Name: valNames[0]}

	var newStmt ast.Stmt

	switch s := stmt.(type) {
	case *ast.IfStmt:
		newIf := *s
		newIf.Cond = i.replaceExprInTree(s.Cond, call, replacementExpr)
		newStmt = &newIf
	case *ast.SwitchStmt:
		newSwitch := *s
		newSwitch.Tag = i.replaceExprInTree(s.Tag, call, replacementExpr)
		newStmt = &newSwitch
	case *ast.ExprStmt:
		newExpr := *s
		newExpr.X = i.replaceExprInTree(s.X, call, replacementExpr)
		newStmt = &newExpr
	case *ast.AssignStmt:
		newAssign := *s
		newRhs := make([]ast.Expr, len(s.Rhs))
		copy(newRhs, s.Rhs)
		for idx, r := range newRhs {
			newRhs[idx] = i.replaceExprInTree(r, call, replacementExpr)
		}
		newAssign.Rhs = newRhs
		newStmt = &newAssign
	default:
		return nil, fmt.Errorf("unsupported embedded statement type %T", stmt)
	}

	return []ast.Stmt{assignStmt, checkStmt, newStmt}, nil
}

// replaceExprInTree traverses the expression tree and replaces the target node with the replacement.
func (i *Injector) replaceExprInTree(root, target, replacement ast.Expr) ast.Expr {
	if root == target {
		return replacement
	}
	switch e := root.(type) {
	case *ast.ParenExpr:
		newParen := *e
		newParen.X = i.replaceExprInTree(e.X, target, replacement)
		return &newParen
	case *ast.UnaryExpr:
		newUnary := *e
		newUnary.X = i.replaceExprInTree(e.X, target, replacement)
		return &newUnary
	case *ast.SelectorExpr:
		newSel := *e
		newSel.X = i.replaceExprInTree(e.X, target, replacement)
		return &newSel
	}
	return root
}

// generateGoRewrite wraps a `go myFunc()` call into `go func() { ... }()` to handle errors.
func (i *Injector) generateGoRewrite(point analysis.InjectionPoint, file *ast.File) (*ast.GoStmt, error) {
	errName := "err"
	tok := token.DEFINE

	assignStmt, err := i.generateAssignment(point, errName, tok)
	if err != nil {
		return nil, err
	}

	handlerBlock := i.generateTerminalHandlerSimple(errName)
	checkStmt := &ast.IfStmt{
		Cond: &ast.BinaryExpr{
			X:  &ast.Ident{Name: errName},
			Op: token.NEQ,
			Y:  &ast.Ident{Name: "nil"},
		},
		Body: handlerBlock,
	}

	body := &ast.BlockStmt{
		List: []ast.Stmt{
			assignStmt,
			checkStmt,
		},
	}

	return &ast.GoStmt{
		Call: &ast.CallExpr{
			Fun: &ast.FuncLit{
				Type: &ast.FuncType{
					Params:  &ast.FieldList{},
					Results: nil,
				},
				Body: body,
			},
			Args: nil,
		},
	}, nil
}

// generateTerminalHandlerSimple generates the AST block for terminal handling.
func (i *Injector) generateTerminalHandlerSimple(errVar string) *ast.BlockStmt {
	var bodyStmts []ast.Stmt

	switch i.MainHandlerStrategy {
	case "panic":
		bodyStmts = []ast.Stmt{
			&ast.ExprStmt{
				X: &ast.CallExpr{
					Fun:  &ast.Ident{Name: "panic"},
					Args: []ast.Expr{&ast.Ident{Name: errVar}},
				},
			},
		}
	case "os-exit":
		bodyStmts = []ast.Stmt{
			&ast.ExprStmt{
				X: &ast.CallExpr{
					Fun: &ast.SelectorExpr{
						X:   &ast.Ident{Name: "fmt"},
						Sel: &ast.Ident{Name: "Println"},
					},
					Args: []ast.Expr{&ast.Ident{Name: errVar}},
				},
			},
			&ast.ExprStmt{
				X: &ast.CallExpr{
					Fun: &ast.SelectorExpr{
						X:   &ast.Ident{Name: "os"},
						Sel: &ast.Ident{Name: "Exit"},
					},
					Args: []ast.Expr{&ast.BasicLit{Kind: token.INT, Value: "1"}},
				},
			},
		}
	case "log-fatal":
		fallthrough
	default:
		bodyStmts = []ast.Stmt{
			&ast.ExprStmt{
				X: &ast.CallExpr{
					Fun: &ast.SelectorExpr{
						X:   &ast.Ident{Name: "log"},
						Sel: &ast.Ident{Name: "Fatal"},
					},
					Args: []ast.Expr{&ast.Ident{Name: errVar}},
				},
			},
		}
	}
	return &ast.BlockStmt{List: bodyStmts}
}

// resolveFuncName tries to determine a readable name for the function being called.
func (i *Injector) resolveFuncName(point analysis.InjectionPoint) string {
	if call := point.Call; call != nil {
		if id, ok := call.Fun.(*ast.Ident); ok {
			return id.Name
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			return sel.Sel.Name
		}
	}
	return "func"
}

// generateReturnStmt generates the return statement inside the validation block using templates.
func (i *Injector) generateReturnStmt(sig *types.Signature, errName, funcName string) (*ast.ReturnStmt, error) {
	var zeroExprs []ast.Expr

	if sig != nil {
		limit := sig.Results().Len()
		if limit > 0 {
			last := sig.Results().At(limit - 1)
			if i.isErrorType(last.Type()) {
				limit--
			}
		}

		for idx := 0; idx < limit; idx++ {
			t := sig.Results().At(idx).Type()
			zero, err := astgen.ZeroExpr(t, astgen.ZeroCtx{})
			if err != nil {
				return nil, err
			}
			zeroExprs = append(zeroExprs, zero)
		}
	}

	returnExprs, _, err := RenderTemplate(i.ErrorTemplate, zeroExprs, errName, funcName)
	if err != nil {
		return nil, err
	}

	return &ast.ReturnStmt{Results: returnExprs}, nil
}

// generateAssignment creates the variable assignment statement.
func (i *Injector) generateAssignment(point analysis.InjectionPoint, errName string, tok token.Token) (ast.Stmt, error) {
	call := point.Call
	var callResultSig *types.Tuple

	if tv, ok := i.Pkg.TypesInfo.Types[call]; ok {
		if tuple, ok := tv.Type.(*types.Tuple); ok {
			callResultSig = tuple
		} else {
			vars := []*types.Var{types.NewVar(token.NoPos, nil, "", tv.Type)}
			callResultSig = types.NewTuple(vars...)
		}
	} else {
		return nil, fmt.Errorf("type info missing for call expression")
	}

	var lhs []ast.Expr

	if point.Assign != nil {
		for idx, expr := range point.Assign.Lhs {
			isLast := idx == len(point.Assign.Lhs)-1
			if isLast {
				lhs = append(lhs, &ast.Ident{Name: errName})
			} else {
				lhs = append(lhs, expr)
			}
		}
	} else {
		for idx := 0; idx < callResultSig.Len()-1; idx++ {
			lhs = append(lhs, &ast.Ident{Name: "_"})
		}
		lhs = append(lhs, &ast.Ident{Name: errName})
	}

	return &ast.AssignStmt{
		Lhs: lhs,
		Tok: tok,
		Rhs: []ast.Expr{call},
	}, nil
}

// isErrorReturningCall checks if the call returns exactly one value which is an error.
func (i *Injector) isErrorReturningCall(call *ast.CallExpr) bool {
	if typesInfo := i.Pkg.TypesInfo; typesInfo != nil {
		tv, ok := typesInfo.Types[call]
		if !ok {
			return false
		}
		if _, isTuple := tv.Type.(*types.Tuple); isTuple {
			return false
		}
		return i.isErrorType(tv.Type)
	}
	return false
}

// isErrorType checks if the type is the "error" interface.
func (i *Injector) isErrorType(t types.Type) bool {
	if t == nil {
		return false
	}
	if types.Identical(t, types.Universe.Lookup("error").Type()) {
		return true
	}
	name := t.String()
	return name == "error" || name == "builtin.error"
}

// isErrorExpr checks if the AST expression looks like "error".
func (i *Injector) isErrorExpr(expr ast.Expr) bool {
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name == "error"
	}
	return false
}

// getScope attempts to find the innermost scope enclosing the given position.
func (i *Injector) getScope(pos token.Pos, file *ast.File) *types.Scope {
	path, _ := astutil.PathEnclosingInterval(file, pos, pos)
	for _, node := range path {
		if scope := i.Pkg.TypesInfo.Scopes[node]; scope != nil {
			return scope
		}
		if fd, ok := node.(*ast.FuncDecl); ok {
			if fd.Type != nil {
				if scope := i.Pkg.TypesInfo.Scopes[fd.Type]; scope != nil {
					return scope
				}
			}
			if obj := i.Pkg.TypesInfo.ObjectOf(fd.Name); obj != nil {
				if fn, ok := obj.(*types.Func); ok {
					return fn.Scope()
				}
			}
		}
		if fl, ok := node.(*ast.FuncLit); ok && fl.Type != nil {
			if scope := i.Pkg.TypesInfo.Scopes[fl.Type]; scope != nil {
				return scope
			}
		}
	}
	if i.Pkg.Types != nil {
		return i.Pkg.Types.Scope()
	}
	return nil
}
