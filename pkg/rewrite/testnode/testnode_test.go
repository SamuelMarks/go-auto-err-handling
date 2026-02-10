package testnode

import (
	"go/ast"
	"go/token"
	"testing"
)

func TestParentAndChild(t *testing.T) {
	p := NewParent()
	if p == nil {
		t.Fatal("expected parent")
	}
	if p.Pos() != 0 || p.End() != 0 {
		t.Fatal("expected zero positions for parent")
	}
	child := p.Child()
	if child == nil {
		t.Fatal("expected child")
	}
	if _, ok := child.(ast.Node); !ok {
		t.Fatal("expected ast.Node child")
	}
	if child.Pos() != 0 || child.End() != 0 {
		t.Fatal("expected zero positions for child")
	}
}

func TestChildEmpty(t *testing.T) {
	p := &Parent{}
	if p.Child() != nil {
		t.Fatal("expected nil child for empty slice")
	}
}

func TestHiddenPos(t *testing.T) {
	h := &hidden{pos: token.Pos(5)}
	if h.Pos() != token.Pos(5) {
		t.Fatal("expected hidden Pos to match")
	}
	if h.End() != token.Pos(5) {
		t.Fatal("expected hidden End to match")
	}
}
