// Package inserter provides utilities for inserting error handling code into Go source files.
// It supports different levels of aggressiveness for adding 'if err != nil { return ... }' checks,
// modifying function signatures, and propagating changes up the call graph.
// The package is designed to work with AST manipulation using golang.org/x/tools and error checking via errcheck.
package inserter

import (
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"go/types"
	"os"
	"strconv"
	"strings"

	"github.com/kisielk/errcheck/errcheck"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

// ReturnsError checks if the given function signature returns an error as its last result.
// It examines the results tuple to see if the last type is the builtin error interface.
//
// sig: the function signature to check.
func ReturnsError(sig *types.Signature) bool {
	results := sig.Results()
	if results.Len() == 0 {
		return false
	}
	last := results.At(results.Len() - 1)
	return types.Identical(last.Type(), types.Universe.Lookup("error").Type())
}

// IsLocalCall determines if the unchecked error comes from a local function call within the same module.
// It finds the package containing the file, retrieves the module path, locates the CallExpr at the position,
// resolves the called function's package, and checks if its path starts with the module path.
//
// unchecked: the unchecked error to analyze.
// pkgs: the loaded packages to search within.
func IsLocalCall(unchecked errcheck.UncheckedError, pkgs []*packages.Package) (bool, error) {
	var modPath string
	var info *types.Info
	var file *ast.File
	var fset *token.FileSet

	// Find the package and file corresponding to the unchecked error
	for _, pkg := range pkgs {
		if modPath == "" && pkg.Module != nil {
			modPath = pkg.Module.Path
		}
		for _, syn := range pkg.Syntax {
			fname := pkg.Fset.File(syn.Pos()).Name()
			if fname == unchecked.Pos.Filename {
				file = syn
				info = pkg.TypesInfo
				fset = pkg.Fset
				break
			}
		}
		if file != nil {
			break
		}
	}

	if file == nil {
		return false, fmt.Errorf("file not found: %s", unchecked.Pos.Filename)
	}
	if modPath == "" {
		// If no module info is present, assume everything relative or standard lib is external.
		// However, for purposes of this tool, we need a module context.
		// We'll return error to be safe, or false.
		return false, fmt.Errorf("module path not found")
	}

	// Locate the call expression at the error position
	tokenPos := fset.File(file.Pos()).Pos(unchecked.Pos.Offset)
	path, _ := astutil.PathEnclosingInterval(file, tokenPos, tokenPos)
	
	var call *ast.CallExpr
	for _, node := range path {
		if c, ok := node.(*ast.CallExpr); ok {
			call = c
			break
		}
	}
	if call == nil {
		return false, fmt.Errorf("call expr not found at position")
	}

	// Determine the package of the called function
	var calleePkg *types.Package
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		obj := info.ObjectOf(fun)
		if obj == nil {
			return false, fmt.Errorf("object not found")
		}
		calleePkg = obj.Pkg()
	case *ast.SelectorExpr:
		sel := info.Selections[fun]
		if sel != nil {
			calleePkg = sel.Obj().Pkg()
		} else {
			// Fallback: looking up object directly (e.g. package variable function)
			obj := info.ObjectOf(fun.Sel)
			if obj != nil {
				calleePkg = obj.Pkg()
			}
		}
	}

	// If calleePkg is nil, it's likely a builtin (e.g. make, new) or code without package info.
	if calleePkg == nil {
		return false, nil
	}

	// Check if the package path implies internal module
	return strings.HasPrefix(calleePkg.Path(), modPath), nil
}

// FindEnclosingFuncAndBlock finds the enclosing FuncDecl and its BlockStmt for the given position.
// It uses ast.Inspect to traverse the AST and locate the innermost FuncDecl containing the position.
//
// pos: the token.Pos of the position to find the enclosing function for.
// file: the AST file to search within.
// fset: the file set for position calculations.
// Returns the FuncDecl, BlockStmt, and the path from file to block if found, or error.
func FindEnclosingFuncAndBlock(pos token.Pos, file *ast.File, fset *token.FileSet) (*ast.FuncDecl, *ast.BlockStmt, []ast.Node, error) {
	path, _ := astutil.PathEnclosingInterval(file, pos, pos)
	var funcDecl *ast.FuncDecl
	var block *ast.BlockStmt
	
	// Walk up the path to find block and function
	for _, node := range path {
		if b, ok := node.(*ast.BlockStmt); ok && block == nil {
			block = b
		}
		if fd, ok := node.(*ast.FuncDecl); ok {
			funcDecl = fd
			break
		}
	}

	if funcDecl == nil {
		return nil, nil, nil, fmt.Errorf("no enclosing func")
	}
	if block == nil {
		return nil, nil, nil, fmt.Errorf("no enclosing block")
	}

	// Trim path to end at the block for convenience
	// We want the path inside the block to the statement
	return funcDecl, block, path, nil
}

// isNilable returns true if the zero value for the type is nil.
// This includes pointers, slices, maps, channels, functions, and interfaces.
//
// t: the type to check.
func isNilable(t types.Type) bool {
	switch t.Underlying().(type) {
	case *types.Pointer, *types.Slice, *types.Map, *types.Chan, *types.Signature, *types.Interface:
		return true
	}
	return false
}

// typeToExpr converts a types.Type to an ast.Expr representing the type.
// It recursively builds the AST type expression, adding imports if necessary for named types from other packages.
//
// fset: the file set for AST operations.
// file: the AST file to modify (for adding imports).
// currentPkg: the current package for determining if import is needed.
// t: the type to convert.
func typeToExpr(fset *token.FileSet, file *ast.File, currentPkg *types.Package, t types.Type) ast.Expr {
	switch ut := t.(type) {
	case *types.Basic:
		return &ast.Ident{Name: ut.Name()}
	case *types.Named:
		obj := ut.Obj()
		if obj.Pkg() == nil || obj.Pkg() == currentPkg {
			return &ast.Ident{Name: obj.Name()}
		}
		impPath := obj.Pkg().Path()
		// AddNamedImport adds the import if missing and returns the alias or package name
		alias := astutil.AddNamedImport(fset, file, "", impPath)
		importName := obj.Pkg().Name()
		if alias != "" {
			importName = alias
		}
		return &ast.SelectorExpr{X: &ast.Ident{Name: importName}, Sel: &ast.Ident{Name: obj.Name()}}
	case *types.Array:
		var lenExpr ast.Expr
		if ut.Len() >= 0 {
			lenExpr = &ast.BasicLit{Kind: token.INT, Value: strconv.FormatInt(ut.Len(), 10)}
		}
		return &ast.ArrayType{Len: lenExpr, Elt: typeToExpr(fset, file, currentPkg, ut.Elem())}
	case *types.Slice:
		return &ast.ArrayType{Elt: typeToExpr(fset, file, currentPkg, ut.Elem())}
	case *types.Map:
		return &ast.MapType{Key: typeToExpr(fset, file, currentPkg, ut.Key()), Value: typeToExpr(fset, file, currentPkg, ut.Value())}
	case *types.Struct:
		// Simplification: only handling named structs usually; anonymous structs are hard to reconstruct perfectly without more context
		// Returning empty struct type for now if not named
		return &ast.StructType{Fields: &ast.FieldList{}}
	case *types.Pointer:
		return &ast.StarExpr{X: typeToExpr(fset, file, currentPkg, ut.Elem())}
	case *types.Chan:
		return &ast.ChanType{Value: typeToExpr(fset, file, currentPkg, ut.Elem())}
	case *types.Interface:
		return &ast.InterfaceType{Methods: &ast.FieldList{}}
	case *types.Signature:
		return &ast.FuncType{}
	default:
		return nil
	}
}

// ZeroExpr returns an AST expression representing the zero value for the given type.
// Supports basic types, nilable types, and composite types with empty literals.
//
// fset: the file set for AST operations.
// file: the AST file to modify.
// info: the types info.
// currentPkg: the current package.
// t: the type to generate zero for.
func ZeroExpr(fset *token.FileSet, file *ast.File, info *types.Info, currentPkg *types.Package, t types.Type) ast.Expr {
	if isNilable(t) {
		return &ast.Ident{Name: "nil"}
	}
	if bt, ok := t.Underlying().(*types.Basic); ok {
		switch bt.Kind() {
		case types.Int, types.Int8, types.Int16, types.Int32, types.Int64,
			types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64,
			types.Float32, types.Float64, types.Complex64, types.Complex128:
			return &ast.BasicLit{Kind: token.INT, Value: "0"}
		case types.String:
			return &ast.BasicLit{Kind: token.STRING, Value: `""`}
		case types.Bool:
			return &ast.Ident{Name: "false"}
		default:
			return nil
		}
	}
	// For structs, arrays, etc., return CompositeLit
	typExpr := typeToExpr(fset, file, currentPkg, t)
	if typExpr == nil {
		return nil
	}
	return &ast.CompositeLit{Type: typExpr}
}

// InsertBasicErrorCheck inserts an error check after the call statement.
// It modifies the assignment to capture the error and adds 'if err != nil { return ... }'.
//
// unchecked: the unchecked error position.
// pkgs: loaded packages.
func InsertBasicErrorCheck(unchecked errcheck.UncheckedError, pkgs []*packages.Package) (bool, error) {
	var file *ast.File
	var info *types.Info
	var fset *token.FileSet
	var currentPkg *packages.Package

	// Locate file
	for _, pkg := range pkgs {
		for _, syn := range pkg.Syntax {
			fname := pkg.Fset.File(syn.Pos()).Name()
			if fname == unchecked.Pos.Filename {
				file = syn
				info = pkg.TypesInfo
				fset = pkg.Fset
				currentPkg = pkg
				break
			}
		}
		if file != nil {
			break
		}
	}
	if file == nil {
		return false, fmt.Errorf("file not found: %s", unchecked.Pos.Filename)
	}

	pos := fset.File(file.Pos()).Pos(unchecked.Pos.Offset)
	funcDecl, block, path, err := FindEnclosingFuncAndBlock(pos, file, fset)
	if err != nil {
		return false, err
	}

	sig := info.Defs[funcDecl.Name].Type().(*types.Signature)
	if !ReturnsError(sig) {
		return false, nil // Caller must return error
	}

	// Identify the statement containing the call
	var stmt ast.Stmt
	var call *ast.CallExpr
	var stmtIndex int = -1

	// Reverse path to find statement inside block
	for i := len(path) - 1; i >= 0; i-- {
		if c, ok := path[i].(*ast.CallExpr); ok {
			call = c
		}
		if s, ok := path[i].(ast.Stmt); ok {
			// Ensure s is a direct child of block
			for idx, bs := range block.List {
				if bs == s {
					stmt = s
					stmtIndex = idx
					break
				}
			}
			if stmt != nil {
				break
			}
		}
	}

	if stmt == nil || call == nil {
		return false, fmt.Errorf("could not locate statement or call")
	}

	// Verify call returns error as last value
	callType := info.TypeOf(call)
	var callTuple *types.Tuple
	if t, ok := callType.(*types.Tuple); ok {
		callTuple = t
	} else {
		// Single return value or void?
		// Since errcheck flagged it, it must return error or (..., error)
		// But if it's single value 'error', Tuple cast might fail if underlying is Basic
		// Actually info.TypeOf returns *types.Tuple for multiple, but Type for single.
		if types.Identical(callType, types.Universe.Lookup("error").Type()) {
			callTuple = types.NewTuple(types.NewVar(token.NoPos, nil, "", callType))
		}
	}

	if callTuple == nil || callTuple.Len() == 0 {
		return false, fmt.Errorf("call does not return error")
	}
	lastRet := callTuple.At(callTuple.Len() - 1)
	if !types.Identical(lastRet.Type(), types.Universe.Lookup("error").Type()) {
		return false, fmt.Errorf("call last return is not error")
	}

	callResultsLen := callTuple.Len()
	errVarName := "err"

	// Modify statement to assign error
	// Cases:
	// 1. ExprStmt: call() -> _, err := call()  (or err := call() if len=1)
	// 2. AssignStmt (:=): a := call() -> a, err := call()
	// 3. AssignStmt (=): a = call() -> a, err = call() (assuming err declared) OR mixed? 
	//    Ideally convert to := if possible or use define.
	
	// Check for name collision
	// If "err" is already defined in this scope, we reusing it is fine, 
	// unless it's shadowing something we need? 
	// To be safe, if we are introducing :=, we use err. If existing =, we reuse.
	
	var newStmt ast.Stmt = stmt

	switch s := stmt.(type) {
	case *ast.ExprStmt:
		// Case: call()
		lhs := make([]ast.Expr, callResultsLen)
		for i := 0; i < callResultsLen-1; i++ {
			lhs[i] = &ast.Ident{Name: "_"}
		}
		lhs[callResultsLen-1] = &ast.Ident{Name: errVarName}
		newStmt = &ast.AssignStmt{
			Lhs: lhs,
			Tok: token.DEFINE,
			Rhs: []ast.Expr{call},
		}

	case *ast.AssignStmt:
		// Case: x := call() or x = call()
		// We need to extend LHS.
		if len(s.Rhs) != 1 || s.Rhs[0] != call {
			return false, fmt.Errorf("assignment structure too complex")
		}
		
		// If current LHS len < call results, append
		if len(s.Lhs) < callResultsLen {
			needed := callResultsLen - len(s.Lhs)
			for i := 0; i < needed; i++ {
				// The last one should be error
				if i == needed-1 {
					s.Lhs = append(s.Lhs, &ast.Ident{Name: errVarName})
				} else {
					s.Lhs = append(s.Lhs, &ast.Ident{Name: "_"})
				}
			}
			// If it was =, switch to := if we added a new var name
			// But if we use assignments, we might need to declare err first?
			// Simplest strategy: Convert to := (DEFINE) if not already, 
			// assuming the existing variables on LHS can be shadowed or re-assigned.
			// However, if they were already declared, := is valid if at least one new var.
			s.Tok = token.DEFINE
		} else {
			// Already has slots? Check if last is _ or err
			last := s.Lhs[len(s.Lhs)-1]
			if id, ok := last.(*ast.Ident); ok {
				if id.Name == "_" {
					id.Name = errVarName
					s.Tok = token.DEFINE // Ensure we define err
				} else {
					errVarName = id.Name // Use existing name
				}
			}
		}
		newStmt = s // Modified in place
	default:
		return false, fmt.Errorf("unsupported statement type")
	}

	// construct 'if err != nil { return ... }'
	
	// Determine return values
	sigResults := sig.Results()
	var returnStmts []ast.Stmt
	
	// Handle named returns vs explicit returns
	// If named returns exist, we can just assign defaults to them and return?
	// Or explicitly return zero values.
	// Mixing: "return" (bare) vs "return 0, nil".
	// We'll generate explicit returns for safety.
	
	returnExprs := make([]ast.Expr, sigResults.Len())
	for i := 0; i < sigResults.Len(); i++ {
		v := sigResults.At(i)
		// If it's the error (last one usually), return 'err'
		if i == sigResults.Len()-1 && types.Identical(v.Type(), types.Universe.Lookup("error").Type()) {
			returnExprs[i] = &ast.Ident{Name: errVarName}
		} else {
			zero := ZeroExpr(fset, file, info, currentPkg.Types, v.Type())
			if zero == nil {
				return false, fmt.Errorf("cannot generate zero value for %s", v.Type())
			}
			returnExprs[i] = zero
		}
	}
	
	ifStmt := &ast.IfStmt{
		Cond: &ast.BinaryExpr{
			X:  &ast.Ident{Name: errVarName},
			Op: token.NEQ,
			Y:  &ast.Ident{Name: "nil"},
		},
		Body: &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.ReturnStmt{Results: returnExprs},
			},
		},
	}

	// Replace the old statement with newStmt + ifStmt
	// Use a slice insertion
	block.List[stmtIndex] = newStmt
	// Insert ifStmt after
	block.List = append(block.List[:stmtIndex+1], append([]ast.Stmt{ifStmt}, block.List[stmtIndex+1:]...)...)

	return true, nil
}

// ModifySignatureToAddError modifies the function signature to add an error return type.
//
// funcDecl: the function declaration to modify.
// fset: the file set.
// file: the AST file.
func ModifySignatureToAddError(funcDecl *ast.FuncDecl, fset *token.FileSet, file *ast.File) error {
	if funcDecl.Type.Results == nil {
		funcDecl.Type.Results = &ast.FieldList{}
	}
	
	// Check if returns are named
	isNamed := false
	if len(funcDecl.Type.Results.List) > 0 {
		if len(funcDecl.Type.Results.List[0].Names) > 0 {
			isNamed = true
		}
	}

	newField := &ast.Field{
		Type: &ast.Ident{Name: "error"},
	}
	if isNamed {
		newField.Names = []*ast.Ident{{Name: "err"}}
	}

	funcDecl.Type.Results.List = append(funcDecl.Type.Results.List, newField)
	return nil
}

// UpdateReturnStatements updates all return statements in the function to append nil for the added error.
// Only affects returns with explicit results.
//
// funcDecl: the function declaration to update.
func UpdateReturnStatements(funcDecl *ast.FuncDecl) error {
	ast.Inspect(funcDecl.Body, func(n ast.Node) bool {
		if r, ok := n.(*ast.ReturnStmt); ok {
			// If it's a bare return (len=0), it picks up named vars, including the new 'err' (zero initialized to nil).
			// If it has results, we must append 'nil'.
			if len(r.Results) > 0 {
				r.Results = append(r.Results, &ast.Ident{Name: "nil"})
			}
		}
		// Don't dive into nested functions (closures)
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		return true
	})
	return nil
}

// FindCallSites finds all call sites to the given function definition across the packages.
//
// funcDef: the function definition to find calls to.
// pkgs: the loaded packages.
func FindCallSites(funcDef *types.Func, pkgs []*packages.Package) ([]*ast.CallExpr, []*ast.File, []*token.FileSet) {
	var calls []*ast.CallExpr
	var files []*ast.File
	var fsets []*token.FileSet

	for _, pkg := range pkgs {
		for _, syn := range pkg.Syntax {
			ast.Inspect(syn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				
				// Resolve call
				var obj types.Object
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					obj = pkg.TypesInfo.Uses[fun]
				case *ast.SelectorExpr:
					if sel := pkg.TypesInfo.Selections[fun]; sel != nil {
						obj = sel.Obj()
					} else {
						obj = pkg.TypesInfo.Uses[fun.Sel]
					}
				}

				if obj == funcDef {
					calls = append(calls, call)
					files = append(files, syn)
					fsets = append(fsets, pkg.Fset)
				}
				return true
			})
		}
	}
	return calls, files, fsets
}

// AddErrorHandlingAndPropagate adds error handling for the unchecked error, propagating up the call graph if necessary.
//
// unchecked: the (possibly fake) unchecked error.
// pkgs: loaded packages.
// processed: set of processed functions to avoid recursion.
func AddErrorHandlingAndPropagate(unchecked errcheck.UncheckedError, pkgs []*packages.Package, processed map[*types.Func]struct{}) (bool, error) {
	var info *types.Info
	var file *ast.File
	var fset *token.FileSet
	
	for _, pkg := range pkgs {
		for _, syn := range pkg.Syntax {
			fname := pkg.Fset.File(syn.Pos()).Name()
			if fname == unchecked.Pos.Filename {
				file = syn
				info = pkg.TypesInfo
				fset = pkg.Fset
				break
			}
		}
		if file != nil {
			break
		}
	}
	if file == nil {
		return false, fmt.Errorf("file not found: %s", unchecked.Pos.Filename)
	}

	pos := fset.File(file.Pos()).Pos(unchecked.Pos.Offset)
	funcDecl, _, _, err := FindEnclosingFuncAndBlock(pos, file, fset)
	if err != nil {
		return false, err
	}

	funcObj := info.Defs[funcDecl.Name]
	funcTyped, ok := funcObj.(*types.Func)
	if !ok {
		return false, fmt.Errorf("not a func")
	}

	if _, ok := processed[funcTyped]; ok {
		return false, nil // Already processed or cycle
	}
	processed[funcTyped] = struct{}{}

	sig := funcTyped.Type().(*types.Signature)
	propagate := false

	// If signature does not return error, modify it
	if !ReturnsError(sig) {
		propagate = true
		if err := ModifySignatureToAddError(funcDecl, fset, file); err != nil {
			return false, err
		}
		if err := UpdateReturnStatements(funcDecl); err != nil {
			return false, err
		}
	}

	// Insert checks for the specific unchecked error
	// Note: unchecked error is 'inside' this function.
	inserted, err := InsertBasicErrorCheck(unchecked, pkgs)
	if err != nil {
		return false, err
	}

	// Propagate to callers if we changed the signature
	if propagate && inserted {
		calls, callFiles, callFsets := FindCallSites(funcTyped, pkgs)
		for i, call := range calls {
			// Construct a fake unchecked error at the call site
			cPos := callFsets[i].Position(call.End())
			fakeErr := errcheck.UncheckedError{
				Pos: cPos,
				// Other fields ignored by our logic
			}
			// Recursive call
			_, err := AddErrorHandlingAndPropagate(fakeErr, pkgs, processed)
			if err != nil {
				return false, err
			}
		}
	}

	return inserted, nil
}

// RewriteFile rewrites the modified AST file back to disk.
//
// filename: the path to the file to rewrite.
// file: the modified AST file.
// fset: the file set for printing.
func RewriteFile(filename string, file *ast.File, fset *token.FileSet) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	return printer.Fprint(f, fset, file)
}

// ProcessLevel1 processes unchecked errors for level 1: insert basic checks for local preexisting err functions.
//
// unchecked: list of unchecked errors.
// pkgs: loaded packages.
// Returns list of modified files or error.
func ProcessLevel1(unchecked []errcheck.UncheckedError, pkgs []*packages.Package) ([]string, error) {
	modified := make(map[string]bool)
	filesMap := make(map[string]*ast.File)
	fsetsMap := make(map[string]*token.FileSet)

	for _, u := range unchecked {
		local, err := IsLocalCall(u, pkgs)
		if err != nil {
			continue // Skip errors/unresolvable
		}
		if !local {
			continue
		}
		inserted, err := InsertBasicErrorCheck(u, pkgs)
		if err == nil && inserted {
			modified[u.Pos.Filename] = true
			
			// Cache file/fset for rewriting
			for _, pkg := range pkgs {
				for _, syn := range pkg.Syntax {
					if pkg.Fset.File(syn.Pos()).Name() == u.Pos.Filename {
						filesMap[u.Pos.Filename] = syn
						fsetsMap[u.Pos.Filename] = pkg.Fset
					}
				}
			}
		}
	}

	var modFiles []string
	for fname := range modified {
		if err := RewriteFile(fname, filesMap[fname], fsetsMap[fname]); err != nil {
			return nil, err
		}
		modFiles = append(modFiles, fname)
	}
	return modFiles, nil
}

// ProcessLevel2 processes unchecked errors for level 2: propagate error handling.
//
// unchecked: list of unchecked errors.
// pkgs: loaded packages.
// Returns list of modified files or error.
func ProcessLevel2(unchecked []errcheck.UncheckedError, pkgs []*packages.Package) ([]string, error) {
	modified := make(map[string]bool)
	processed := make(map[*types.Func]struct{})
	filesMap := make(map[string]*ast.File)
	fsetsMap := make(map[string]*token.FileSet)

	for _, u := range unchecked {
		local, err := IsLocalCall(u, pkgs)
		if err != nil {
			continue
		}
		if !local {
			continue
		}
		inserted, err := AddErrorHandlingAndPropagate(u, pkgs, processed)
		if err == nil && inserted {
			// Since propagation might touch multiple files, we need to find what changed.
			// Currently AddErrorHandlingAndPropagate modifies ASTs in memory.
			// We iterate all loaded files to see if we flagged them? 
			// Simpler: Just collect all potential files from involved packages or rely on `processed`.
			// Since `AddErrorHandlingAndPropagate` doesn't return list of modified files, 
			// and we are modifying ASTs in place, we can collect all files that need valid rewrite.
			// Optimization: `AddErrorHandlingAndPropagate` could return list. 
			// For now, we'll mark the file of the unchecked error, but this misses propagated callers.
			// Let's iterate all packages' syntax and assume if we touched them we rewrite.
			// But tracking exact changes is better. Ideally passing a 'modifiedSet' down.
			// Given interface constraint, we will do a scan or assume only `u.Pos.Filename` and callers.
			// To be correct within constraints, let's just rewrite *all* files that *might* have changed?
			// Or better: ProcessLevel2 should likely be tracking more.
			// Fix: We'll Iterate processed functions, look up their files, add to modified.
			modified[u.Pos.Filename] = true // At least this one
		}
	}
	
	// Collect files from processed functions
	for fn := range processed {
		// Find file for fn
		for _, pkg := range pkgs {
			if pkg.TypesInfo.Defs[fn.Name()] == fn { // Approximate lookup
				// Doesn't help locate file easily without iterating syntax
			}
		}
		// Better: Iterate all files, check if they contain processed functions? Slow.
		// Fast path: Just iterate all files in loaded packages. If they are in the module, rewrite them?
		// Safe approach: Rewrite all files in pkgs.
		for _, pkg := range pkgs {
			for _, syn := range pkg.Syntax {
				fname := pkg.Fset.File(syn.Pos()).Name()
				filesMap[fname] = syn
				fsetsMap[fname] = pkg.Fset
				// We don't know for sure if it changed, but rewriting identity is safe (idempotent).
				// We add to modified map to ensure we output it.
				// In a real tool, we'd check diffs or flags.
				modified[fname] = true
			}
		}
	}

	var modFiles []string
	for fname := range modified {
		// Filter to only files inside the root dir? Handled by loader/cli usually.
		if file, ok := filesMap[fname]; ok {
			if err := RewriteFile(fname, file, fsetsMap[fname]); err != nil {
				return nil, err
			}
			modFiles = append(modFiles, fname)
		}
	}
	return modFiles, nil
}

// ProcessLevel3 processes unchecked errors for level 3: third party errors.
//
// unchecked: list of unchecked errors.
// pkgs: loaded packages.
// Returns list of modified files or error.
func ProcessLevel3(unchecked []errcheck.UncheckedError, pkgs []*packages.Package) ([]string, error) {
	modified := make(map[string]bool)
	filesMap := make(map[string]*ast.File)
	fsetsMap := make(map[string]*token.FileSet)

	for _, u := range unchecked {
		local, err := IsLocalCall(u, pkgs)
		if err != nil {
			continue
		}
		if local {
			continue // Skip local, Logic is "Also handle errors that aren't from this codebase"
			// Wait, "Same as 1/2 but also". So strict Level 3 logic usually implies superset.
			// But usually CLI flags compose. The prompt says "--third-party-err".
			// If flags are exclusive in execution, this funtion handles ONLY third party.
			// If flags are additive, main calls both.
			// Prompt "Same as 1. but also". Level 3 implies "Non-Local".
			// Let's process Non-Local here.
		}
		
		inserted, err := InsertBasicErrorCheck(u, pkgs)
		if err == nil && inserted {
			modified[u.Pos.Filename] = true
			for _, pkg := range pkgs {
				for _, syn := range pkg.Syntax {
					if pkg.Fset.File(syn.Pos()).Name() == u.Pos.Filename {
						filesMap[u.Pos.Filename] = syn
						fsetsMap[u.Pos.Filename] = pkg.Fset
					}
				}
			}
		}
	}

	var modFiles []string
	for fname := range modified {
		if err := RewriteFile(fname, filesMap[fname], fsetsMap[fname]); err != nil {
			return nil, err
		}
		modFiles = append(modFiles, fname)
	}
	return modFiles, nil
}