package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

// PatchSignature manually updates the types.Info maps to reflect a change in a function's signature
// (specifically adding an error return). Please ensure the AST is modified before calling this.
//
// actions:
// 1. Retrieves the existing function object.
// 2. Constructs a new types.Signature with the appended 'error' return value.
// 3. Creates a new types.Func object pointing to this signature.
// 4. Updates info.Defs to point to the new object.
// 5. Updates info.Types map for the function type node.
// 6. Updates info.Uses to point all existing references to the new object.
func PatchSignature(info *types.Info, decl *ast.FuncDecl, pkg *types.Package) error {
	if info == nil || decl == nil {
		return fmt.Errorf("nil info or decl")
	}

	// 1. Get existing object
	obj := lookupObject(info, decl.Name)
	if obj == nil {
		return fmt.Errorf("object not found for function %s", decl.Name.Name)
	}

	fnObj, ok := obj.(*types.Func)
	if !ok {
		return fmt.Errorf("%s is not a function", decl.Name.Name)
	}

	oldSig, ok := fnObj.Type().(*types.Signature)
	if !ok {
		return fmt.Errorf("object type is not signature")
	}

	// 2. Construct New Signature
	newSig := ExtendSignatureWithError(oldSig, pkg)

	// 3. Create New Func Object
	newFnObj := types.NewFunc(fnObj.Pos(), fnObj.Pkg(), fnObj.Name(), newSig)

	// 4. Update Defs
	info.Defs[decl.Name] = newFnObj

	// 5. Update Types map for the Function type declaration
	if decl.Type != nil {
		info.Types[decl.Type] = types.TypeAndValue{
			Type:  newSig,
			Value: nil,
		}
	}

	// 6. Update Uses
	updateUses(info, fnObj, newFnObj)

	return nil
}

// PatchVarType manually updates the types.Info maps to reflect a change in a variable's type.
// It constructs a new types.Var object with the updated signature and replaces references.
// Returns the new variable object.
func PatchVarType(info *types.Info, ident *ast.Ident, newSig *types.Signature) (*types.Var, error) {
	if info == nil || ident == nil {
		return nil, fmt.Errorf("nil inputs")
	}

	obj := lookupObject(info, ident)
	if obj == nil {
		return nil, fmt.Errorf("object not found for var %s", ident.Name)
	}

	varObj, ok := obj.(*types.Var)
	if !ok {
		return nil, fmt.Errorf("%s is not a variable", ident.Name)
	}

	// Create new Var object with new type
	newVarObj := types.NewVar(varObj.Pos(), varObj.Pkg(), varObj.Name(), newSig)

	// Update Defs (if ident is definition)
	if _, isDef := info.Defs[ident]; isDef {
		info.Defs[ident] = newVarObj
	} else {
		// Linear scan to update definition if we are patching from a usage (unlikely but safe)
		for k, v := range info.Defs {
			if v == varObj {
				info.Defs[k] = newVarObj
			}
		}
	}

	// Update Uses
	updateUses(info, varObj, newVarObj)

	return newVarObj, nil
}

// ExtendSignatureWithError creates a new signature based on oldSig with an added 'error' return value.
func ExtendSignatureWithError(oldSig *types.Signature, pkg *types.Package) *types.Signature {
	params := oldSig.Params()
	oldResults := oldSig.Results()

	var newVars []*types.Var

	// Copy existing results
	if oldResults != nil {
		for i := 0; i < oldResults.Len(); i++ {
			newVars = append(newVars, oldResults.At(i))
		}
	}

	// Create Error Var
	errType := types.Universe.Lookup("error").Type()
	errVar := types.NewVar(token.NoPos, pkg, "", errType)
	newVars = append(newVars, errVar)

	newResults := types.NewTuple(newVars...)

	return types.NewSignature(oldSig.Recv(), params, newResults, oldSig.Variadic())
}

func updateUses(info *types.Info, oldObj, newObj types.Object) {
	for id, usedObj := range info.Uses {
		if usedObj == oldObj {
			info.Uses[id] = newObj
		}
	}
}

func lookupObject(info *types.Info, ident *ast.Ident) types.Object {
	if info == nil || ident == nil {
		return nil
	}
	if obj := info.ObjectOf(ident); obj != nil {
		return obj
	}
	// Fallback: allow cloned idents by matching on position/name.
	for id, def := range info.Defs {
		if def != nil && id.Pos() == ident.Pos() && id.Name == ident.Name {
			return def
		}
	}
	for id, use := range info.Uses {
		if use != nil && id.Pos() == ident.Pos() && id.Name == ident.Name {
			return use
		}
	}
	return nil
}
