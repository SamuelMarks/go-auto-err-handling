package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

// PatchSignature manually updates the types.Info maps to reflect a change in a function's signature
// (specifically adding an error return). This allows subsequent analysis passes (like PropagateCallers)
// to see the correct return types immediately without requiring a generic packages.Load reload (which is slow).
//
// It performs the following:
// 1. Retrieves the existing function object.
// 2. Constructs a new types.Signature with the appended 'error' return value.
// 3. Creates a new types.Func object pointing to this signature.
// 4. Updates info.Defs to point to the new object.
// 5. Updates info.Types map for the function type node.
// 6. Updates info.Uses to point all existing references to the new object.
//
// info: The package type info.
// decl: The modified AST declaration (should already have the 'error' field in AST).
// pkg: The types.Package the function belongs to.
func PatchSignature(info *types.Info, decl *ast.FuncDecl, pkg *types.Package) error {
	if info == nil || decl == nil {
		return fmt.Errorf("nil info or decl")
	}

	// 1. Get existing object
	obj := info.ObjectOf(decl.Name)
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
	// We rely on oldSig for everything except the extra result.
	// This preserves parameter types and imports.

	params := oldSig.Params()
	oldResults := oldSig.Results()

	var newVars []*types.Var

	// Copy existing results
	// Check for nil to avoid iterating a nil tuple pointer if strictly void
	if oldResults != nil {
		for i := 0; i < oldResults.Len(); i++ {
			newVars = append(newVars, oldResults.At(i))
		}
	}

	// Create Error Var
	// We use Universe 'error'
	errType := types.Universe.Lookup("error").Type()
	errVar := types.NewVar(token.NoPos, pkg, "", errType)
	newVars = append(newVars, errVar)

	newResults := types.NewTuple(newVars...)

	// Create Signature
	newSig := types.NewSignature(oldSig.Recv(), params, newResults, oldSig.Variadic())

	// 3. Create New Func Object
	// We reuse position and package from old object
	newFnObj := types.NewFunc(fnObj.Pos(), fnObj.Pkg(), fnObj.Name(), newSig)

	// 4. Update Defs
	// This is the critical step. AST Ident -> New Object.
	info.Defs[decl.Name] = newFnObj

	// 5. Update Types map for the Function Type Node
	if decl.Type != nil {
		info.Types[decl.Type] = types.TypeAndValue{
			Type:  newSig,
			Value: nil,
		}
	}

	// 6. Update Uses
	// Iterate over all uses in the package. If any use pointed to the old 'fnObj',
	// point it to 'newFnObj'. This ensures PropagateCallers can follow the chain
	// without needing a full reload.
	for id, usedObj := range info.Uses {
		if usedObj == fnObj {
			info.Uses[id] = newFnObj
		}
	}

	return nil
}
