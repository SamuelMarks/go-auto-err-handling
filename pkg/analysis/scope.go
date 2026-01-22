package analysis

import (
	"fmt"
	"go/token"
	"go/types"
)

// GenerateUniqueName generates a variable name that does not collide with existing names
// in the provided scope or its parent scopes.
//
// It starts with the base name. If that name resolves to an object in the scope (or parents),
// it appends a counter (e.g., "err" -> "err1") and retries until a unique name is found.
//
// scope: The local scope where the variable handles are checked.
// base: The preferred name (e.g., "err").
//
// Returns "base" if it's not present, otherwise "base1", "base2", etc.
func GenerateUniqueName(scope *types.Scope, base string) string {
	if scope == nil {
		return base
	}
	name := base
	count := 1
	for {
		// LookupParent checks the current scope and all its parents.
		// We pass token.NoPos as valid position usually implies we want to see if it's visible at that pos,
		// but checking for *any* existence in the block is safer for collision avoidance.
		_, obj := scope.LookupParent(name, token.NoPos)
		if obj == nil {
			return name
		}
		name = fmt.Sprintf("%s%d", base, count)
		count++
	}
}
