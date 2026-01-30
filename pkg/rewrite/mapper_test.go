package rewrite

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
)

// setupMapperTest parses the source into both AST and DST representations.
// It returns the FileSet, AST File, and DST File.
func setupMapperTest(t *testing.T, src string) (*token.FileSet, *ast.File, *dst.File) {
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "main.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parser failed: %v", err)
	}

	// Use daves/dst decorator to produce the DST
	dstFile, err := decorator.NewDecorator(fset).DecorateFile(astFile)
	if err != nil {
		t.Fatalf("decorator failed: %v", err)
	}

	return fset, astFile, dstFile
}

// findAstNode locates a node in the AST matching a predicate for testing purposes.
func findAstNode(root ast.Node, predicate func(ast.Node) bool) ast.Node {
	var found ast.Node
	ast.Inspect(root, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if n != nil && predicate(n) {
			found = n
			return false
		}
		return true
	})
	return found
}

func TestFindDstNode_SimpleCall(t *testing.T) {
	src := `package main
func main() {
	foo()
}
`
	fset, astFile, dstFile := setupMapperTest(t, src)

	// Target: "foo()" CallExpr
	target := findAstNode(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok {
				return id.Name == "foo"
			}
		}
		return false
	})
	if target == nil {
		t.Fatal("Target AST node not found")
	}

	// Execute Map
	res, err := FindDstNode(fset, dstFile, astFile, target)
	if err != nil {
		t.Fatalf("FindDstNode failed: %v", err)
	}

	// Verify Result Type
	dstCall, ok := res.Node.(*dst.CallExpr)
	if !ok {
		// Use Fatalf to avoid panic on subsequent checks
		t.Fatalf("Expected *dst.CallExpr, got %T", res.Node)
	}
	// Verify Result Content
	if ident, ok := dstCall.Fun.(*dst.Ident); !ok || ident.Name != "foo" {
		t.Errorf("Mapped node content mismatch. Expected foo, got %v", dstCall.Fun)
	}
	// Verify Parent (Should be ExprStmt for a bare call)
	if _, ok := res.Parent.(*dst.ExprStmt); !ok {
		t.Errorf("Expected parent to be ExprStmt, got %T", res.Parent)
	}
}

func TestFindDstNode_DeepNesting(t *testing.T) {
	src := `package main
func main() {
	if true {
		go func() {
			target(1)
		}()
	}
}
`
	fset, astFile, dstFile := setupMapperTest(t, src)

	// Target: "target(1)"
	target := findAstNode(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok {
				return id.Name == "target"
			}
		}
		return false
	})

	res, err := FindDstNode(fset, dstFile, astFile, target)
	if err != nil {
		t.Fatalf("FindDstNode failed: %v", err)
	}

	if _, ok := res.Node.(*dst.CallExpr); !ok {
		t.Errorf("Expected *dst.CallExpr, got %T", res.Node)
	}
}

func TestFindDstNode_ListIndices(t *testing.T) {
	src := `package main
func main() {
	a := 1
	b := 2
	targetStmt()
}
`
	fset, astFile, dstFile := setupMapperTest(t, src)

	// Target: "targetStmt()" which is the 3rd statement (index 2)
	target := findAstNode(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok {
				return id.Name == "targetStmt"
			}
		}
		return false
	})

	// Note: We need the Stmt wrapping the call to test list index mapping of BlockStmt
	// astutil enclosure will handle retrieving the Call, and the mapper will look for the Call.
	// The path will be [Call, ExprStmt, BlockStmt].
	// Step 1: BlockStmt -> ExprStmt (List[2]).
	// Step 2: ExprStmt -> CallExpr (X).

	res, err := FindDstNode(fset, dstFile, astFile, target)
	if err != nil {
		t.Fatalf("FindDstNode failed: %v", err)
	}

	// Verify we got the call
	if _, ok := res.Node.(*dst.CallExpr); !ok {
		t.Errorf("Expected *dst.CallExpr, got %T", res.Node)
	}
}

func TestFindDstNode_ConditionField(t *testing.T) {
	src := `package main
func main() {
	if check() {
	}
}
`
	fset, astFile, dstFile := setupMapperTest(t, src)

	target := findAstNode(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok {
				return id.Name == "check"
			}
		}
		return false
	})

	res, err := FindDstNode(fset, dstFile, astFile, target)
	if err != nil {
		t.Fatalf("FindDstNode failed: %v", err)
	}

	// Check if parent is IfStmt
	if ifStmt, ok := res.Parent.(*dst.IfStmt); ok {
		if ifStmt.Cond != res.Node {
			t.Error("Mapped node is not the Condition field of the parent IfStmt")
		}
	} else {
		t.Errorf("Expected parent IfStmt, got %T", res.Parent)
	}
}

func TestFindDstNode_Error_MismatchStructure(t *testing.T) {
	// This test ensures we catch cases where AST and DST diverge (e.g. if one was modified)
	src := `package main
func main() {
	a()
}
`
	fset, astFile, dstFile := setupMapperTest(t, src)

	target := findAstNode(astFile, func(n ast.Node) bool {
		if _, ok := n.(*ast.CallExpr); ok {
			return true
		}
		return false
	})

	// MUTATE DST to break isomorphism
	// Remove the statement from the DST block
	funcDecl := dstFile.Decls[0].(*dst.FuncDecl)
	funcDecl.Body.List = nil

	// Now try to map. The logic will determine step from AST (List[0]),
	// but fail to apply it to DST (List is empty).
	_, err := FindDstNode(fset, dstFile, astFile, target)
	if err == nil {
		t.Error("Expected error due to structural mismatch, got nil")
	}

	// We expect "DST slice index out of bounds" or similar
	if err != nil && fmt.Sprintf("%v", err) == "" {
		t.Error("Empty error string")
	}
}

func TestFindDstNode_NilTarget(t *testing.T) {
	_, _, dstFile := setupMapperTest(t, "package p")
	_, err := FindDstNode(nil, dstFile, nil, nil)
	if err == nil {
		t.Error("Expected error for nil target")
	}
}
