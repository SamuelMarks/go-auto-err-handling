package rewrite

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"

	"github.com/SamuelMarks/go-auto-err-handling/pkg/rewrite/testnode"
	"github.com/dave/dst"
)

type fakeSliceNode struct {
	Items []int
	Decs  dst.NodeDecs
}

func (f *fakeSliceNode) Decorations() *dst.NodeDecs { return &f.Decs }
func (f *fakeSliceNode) Pos() token.Pos             { return token.NoPos }
func (f *fakeSliceNode) End() token.Pos             { return token.NoPos }

type fakeIfaceNode struct {
	Value interface{}
	Decs  dst.NodeDecs
}

func (f *fakeIfaceNode) Decorations() *dst.NodeDecs { return &f.Decs }
func (f *fakeIfaceNode) Pos() token.Pos             { return token.NoPos }
func (f *fakeIfaceNode) End() token.Pos             { return token.NoPos }

type unexportedFieldParent struct {
	pos   token.Pos
	child ast.Node
}

func (p *unexportedFieldParent) Pos() token.Pos { return p.pos }
func (p *unexportedFieldParent) End() token.Pos { return p.pos }

func TestDetermineStep_NotFound(t *testing.T) {
	parent := &ast.IfStmt{}
	child := &ast.ReturnStmt{}
	if _, err := determineStep(parent, child); err == nil {
		t.Fatal("expected error when child not found")
	}
}

func TestApplyStep_Errors(t *testing.T) {
	cases := []struct {
		name string
		node dst.Node
		step traversalStep
	}{
		{
			name: "missing-field",
			node: &dst.IfStmt{},
			step: traversalStep{FieldName: "DoesNotExist", Index: -1},
		},
		{
			name: "index-on-non-slice",
			node: &dst.IfStmt{},
			step: traversalStep{FieldName: "Cond", Index: 0},
		},
		{
			name: "slice-index-oob",
			node: &dst.BlockStmt{},
			step: traversalStep{FieldName: "List", Index: 2},
		},
		{
			name: "slice-element-not-node",
			node: &fakeSliceNode{Items: []int{1}},
			step: traversalStep{FieldName: "Items", Index: 0},
		},
		{
			name: "field-nil",
			node: &dst.IfStmt{Cond: nil},
			step: traversalStep{FieldName: "Cond", Index: -1},
		},
		{
			name: "field-not-node",
			node: &fakeIfaceNode{Value: 123},
			step: traversalStep{FieldName: "Value", Index: -1},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := applyStep(tc.node, tc.step); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestDecorateFile(t *testing.T) {
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "main.go", "package main\nfunc main() {}\n", 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	dstFile, err := DecorateFile(fset, astFile)
	if err != nil {
		t.Fatalf("DecorateFile failed: %v", err)
	}
	if dstFile == nil || dstFile.Name == nil {
		t.Fatal("expected decorated file with name")
	}
}

func TestFindDstNode_PathErrors(t *testing.T) {
	fset, astFile, dstFile := setupMapperTest(t, "package main\nfunc main(){ foo() }\n")
	target := findAstNode(astFile, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "foo" {
				return true
			}
		}
		return false
	})
	if target == nil {
		t.Fatal("expected target call")
	}

	origPath := pathEnclosingIntervalFunc
	origDetermine := determineStepFunc
	t.Cleanup(func() {
		pathEnclosingIntervalFunc = origPath
		determineStepFunc = origDetermine
	})

	// len(path) == 0
	pathEnclosingIntervalFunc = func(_ *ast.File, _ token.Pos, _ token.Pos) ([]ast.Node, bool) {
		return nil, false
	}
	if _, err := FindDstNode(fset, dstFile, astFile, target); err == nil {
		t.Fatal("expected error for empty path")
	}

	// path does not terminate at file root
	pathEnclosingIntervalFunc = func(_ *ast.File, _ token.Pos, _ token.Pos) ([]ast.Node, bool) {
		return []ast.Node{target, &ast.File{}}, false
	}
	if _, err := FindDstNode(fset, dstFile, astFile, target); err == nil {
		t.Fatal("expected root mismatch error")
	}

	// determineStep error
	pathEnclosingIntervalFunc = origPath
	determineStepFunc = func(parent, child ast.Node) (traversalStep, error) {
		return traversalStep{}, fmt.Errorf("boom")
	}
	if _, err := FindDstNode(fset, dstFile, astFile, target); err == nil {
		t.Fatal("expected determineStep error")
	}
}

func TestDetermineStep_UnexportedField(t *testing.T) {
	p := &unexportedFieldParent{child: ast.NewIdent("x")}
	if _, err := determineStep(p, p.child); err == nil {
		t.Fatal("expected error for unexported field")
	}
}

func TestDetermineStep_CanInterfaceFalse(t *testing.T) {
	p := testnode.NewParent()
	child := p.Child()
	if child == nil {
		t.Fatal("expected child node")
	}
	if _, err := determineStep(p, child); err == nil {
		t.Fatal("expected error when element cannot interface")
	}
}

func TestDetermineStep_CanInterfaceHook(t *testing.T) {
	orig := canInterfaceFunc
	t.Cleanup(func() { canInterfaceFunc = orig })
	canInterfaceFunc = func(_ reflect.Value) bool { return false }

	parent := &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{}}}
	child := parent.List[0]
	if _, err := determineStep(parent, child); err == nil {
		t.Fatal("expected error when CanInterface is forced false")
	}
}
