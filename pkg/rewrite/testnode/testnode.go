package testnode

import (
	"go/ast"
	"go/token"
)

// Parent is a custom ast.Node with an exported slice field of unexported elements.
type Parent struct {
	Slice []hidden
}

// hidden is an unexported ast.Node used to trigger reflection edge cases.
type hidden struct {
	pos token.Pos
}

// Pos implements ast.Node.
func (h *hidden) Pos() token.Pos { return h.pos }

// End implements ast.Node.
func (h *hidden) End() token.Pos { return h.pos }

// Pos implements ast.Node.
func (p *Parent) Pos() token.Pos { return 0 }

// End implements ast.Node.
func (p *Parent) End() token.Pos { return 0 }

// NewParent returns a Parent containing a single hidden child.
func NewParent() *Parent {
	return &Parent{Slice: []hidden{{}}}
}

// Child exposes the hidden node as an ast.Node.
func (p *Parent) Child() ast.Node {
	if len(p.Slice) == 0 {
		return nil
	}
	return &p.Slice[0]
}
