package rewrite

import (
	"fmt"
	"go/ast"
	"go/token"
	"reflect"

	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
	"golang.org/x/tools/go/ast/astutil"
)

// DstMapResult holds the result of a mapping operation, containing the target DST node
// and its immediate parent (container).
type DstMapResult struct {
	// Node is the DST node corresponding to the input AST node.
	Node dst.Node
	// Parent is the parent of the resolved Node in the DST tree (or nil if Node is root).
	Parent dst.Node
}

// FindDstNode locates the equivalent 'dst.Node' for a specific 'ast.Node'.
//
// It uses the provided FileSet to compute the exact AST path enclosing the node,
// then traverses the 'dst.File' structure in an isomorphic manner (matching field names and list indices)
// to find the corresponding node in the Concrete Syntax Tree.
//
// This is critical for translating analysis results (go/ast) into refactoring targets (dave/dst).
//
// fset: The token.FileSet used to parse the file.
// dstFile: The root of the DST tree (previously decorated).
// astFile: The root of the AST tree (used for analysis).
// targetNode: The specific AST node to map (e.g. InjectionPoint.Call).
//
// Returns a DstMapResult containing the DST node and its parent, or an error if correspondence is lost.
func FindDstNode(fset *token.FileSet, dstFile *dst.File, astFile *ast.File, targetNode ast.Node) (DstMapResult, error) {
	if targetNode == nil {
		return DstMapResult{}, fmt.Errorf("targetNode cannot be nil")
	}

	// 1. Calculate the path in the AST.
	path, _ := astutil.PathEnclosingInterval(astFile, targetNode.Pos(), targetNode.End())
	if len(path) == 0 {
		return DstMapResult{}, fmt.Errorf("no AST node found covering position %d", targetNode.Pos())
	}

	// 2. Validate Root matches
	if path[len(path)-1] != astFile {
		return DstMapResult{}, fmt.Errorf("AST path does not terminate at provided file root")
	}

	// 3. Find the index of our targetNode in the path.
	startIndex := -1
	for i, n := range path {
		if n == targetNode {
			startIndex = i
			break
		}
	}

	if startIndex == -1 {
		return DstMapResult{}, fmt.Errorf("target node not found in enclosing path")
	}

	// 4. Traverse DST
	var currentDst dst.Node = dstFile
	var parentDst dst.Node = nil

	for i := len(path) - 2; i >= startIndex; i-- {
		astParent := path[i+1]
		astChild := path[i]

		step, err := determineStep(astParent, astChild)
		if err != nil {
			return DstMapResult{}, fmt.Errorf("failed to map step at depth %d (%T -> %T): %w", i, astParent, astChild, err)
		}

		nextDst, err := applyStep(currentDst, step)
		if err != nil {
			return DstMapResult{}, fmt.Errorf("failed to apply step to DST node %T: %w", currentDst, err)
		}

		parentDst = currentDst
		currentDst = nextDst
	}

	return DstMapResult{Node: currentDst, Parent: parentDst}, nil
}

// traversalStep describes how to move from a parent node to a child node in the tree structure.
type traversalStep struct {
	// FieldName is the struct field name in the parent (e.g., "Body", "List", "X").
	FieldName string
	// Index is the slice index if the field is a slice (e.g., BlockStmt.List[i]). -1 if direct field.
	Index int
}

// determineStep calculates the structural relationship between an AST parent and child.
func determineStep(parent, child ast.Node) (traversalStep, error) {
	val := reflect.ValueOf(parent)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	for i := 0; i < val.NumField(); i++ {
		fieldVal := val.Field(i)
		fieldType := val.Type().Field(i)
		name := fieldType.Name

		if fieldType.PkgPath != "" {
			continue
		}

		if fieldVal.Kind() == reflect.Slice {
			for idx := 0; idx < fieldVal.Len(); idx++ {
				if !fieldVal.Index(idx).CanInterface() {
					continue
				}
				elem := fieldVal.Index(idx).Interface()
				if elem == child {
					return traversalStep{FieldName: name, Index: idx}, nil
				}
			}
		}

		if fieldVal.Kind() == reflect.Ptr || fieldVal.Kind() == reflect.Interface {
			if !fieldVal.IsNil() && fieldVal.Interface() == child {
				return traversalStep{FieldName: name, Index: -1}, nil
			}
		}
	}

	return traversalStep{}, fmt.Errorf("child node %T not found in parent %T", child, parent)
}

// applyStep traverses the DST node using the step instruction derived from the AST.
func applyStep(node dst.Node, step traversalStep) (dst.Node, error) {
	val := reflect.ValueOf(node)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	fieldVal := val.FieldByName(step.FieldName)
	if !fieldVal.IsValid() {
		return nil, fmt.Errorf("DST node %T does not have field %q", node, step.FieldName)
	}

	if step.Index >= 0 {
		if fieldVal.Kind() != reflect.Slice {
			return nil, fmt.Errorf("DST field %s.%s is not a slice", val.Type().Name(), step.FieldName)
		}
		if step.Index >= fieldVal.Len() {
			return nil, fmt.Errorf("DST slice index out of bounds: %d >= %d", step.Index, fieldVal.Len())
		}
		res := fieldVal.Index(step.Index).Interface()
		if resNode, ok := res.(dst.Node); ok {
			return resNode, nil
		}
		return nil, fmt.Errorf("DST slice element at %s[%d] is not a dst.Node (got %T)", step.FieldName, step.Index, res)
	}

	if fieldVal.IsNil() {
		return nil, fmt.Errorf("DST field %s is nil", step.FieldName)
	}
	res := fieldVal.Interface()
	if resNode, ok := res.(dst.Node); ok {
		return resNode, nil
	}
	return nil, fmt.Errorf("DST field %s is not a dst.Node (got %T)", step.FieldName, res)
}

// DecorateFile converts a standard Go AST file into a DST file, preserving comments/spacing.
func DecorateFile(fset *token.FileSet, file *ast.File) (*dst.File, error) {
	dec := decorator.NewDecorator(fset)
	return dec.DecorateFile(file)
}
