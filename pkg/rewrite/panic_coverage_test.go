package rewrite

import (
	"testing"

	"github.com/dave/dst"
)

// TestIsTerminating_Detailed provides exhaustive coverage for the isTerminating control flow analysis logic.
func TestIsTerminating_Detailed(t *testing.T) {
	inj := &Injector{}

	tests := []struct {
		name string
		stmt dst.Stmt
		want bool
	}{
		{"Return", &dst.ReturnStmt{}, true},
		{"PanicExpr", &dst.ExprStmt{X: &dst.CallExpr{Fun: dst.NewIdent("panic")}}, true},
		{"NormalExpr", &dst.ExprStmt{X: dst.NewIdent("print")}, false},
		{"BlockTerm", &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{}}}, true},
		{"BlockNonTerm", &dst.BlockStmt{List: []dst.Stmt{&dst.ExprStmt{}}}, false},
		{"BlockEmpty", &dst.BlockStmt{List: []dst.Stmt{}}, false},
		{"IfBothTerm", &dst.IfStmt{Body: &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{}}}, Else: &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{}}}}, true},
		{"IfNoElse", &dst.IfStmt{Body: &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{}}}}, false},
		{"IfElseNonTerm", &dst.IfStmt{Body: &dst.BlockStmt{List: []dst.Stmt{&dst.ReturnStmt{}}}, Else: &dst.BlockStmt{}}, false},
		{"ForInfinite", &dst.ForStmt{Cond: nil}, true},
		{"ForCond", &dst.ForStmt{Cond: dst.NewIdent("true")}, false},
		{"SwitchNoBody", &dst.SwitchStmt{Body: &dst.BlockStmt{List: []dst.Stmt{}}}, false},
		{"SwitchNoDefault", &dst.SwitchStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.CaseClause{List: []dst.Expr{dst.NewIdent("1")}, Body: []dst.Stmt{&dst.ReturnStmt{}}},
		}}}, false},
		{"SwitchDefaultTerm", &dst.SwitchStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.CaseClause{List: nil, Body: []dst.Stmt{&dst.ReturnStmt{}}},
		}}}, true},
		{"SwitchNonCaseNode", &dst.SwitchStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.EmptyStmt{},
		}}}, false},
		{"SwitchCaseNonTerm", &dst.SwitchStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.CaseClause{List: nil, Body: []dst.Stmt{&dst.ExprStmt{X: dst.NewIdent("x")}}},
		}}}, false},
		{"SwitchCaseEmpty", &dst.SwitchStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.CaseClause{List: nil, Body: nil},
		}}}, false},
		{"SelectNoDefault", &dst.SelectStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.CommClause{Comm: &dst.SendStmt{}, Body: []dst.Stmt{&dst.ReturnStmt{}}},
		}}}, false},
		{"SelectDefaultTerm", &dst.SelectStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.CommClause{Comm: nil, Body: []dst.Stmt{&dst.ReturnStmt{}}},
		}}}, true},
		{"SelectNonCommNode", &dst.SelectStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.EmptyStmt{},
		}}}, false},
		{"SelectCaseNonTerm", &dst.SelectStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.CommClause{Comm: nil, Body: []dst.Stmt{&dst.ExprStmt{X: dst.NewIdent("x")}}},
		}}}, false},
		{"SelectCaseEmpty", &dst.SelectStmt{Body: &dst.BlockStmt{List: []dst.Stmt{
			&dst.CommClause{Comm: nil, Body: []dst.Stmt{}}, // implies Break
		}}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inj.isTerminating(tt.stmt); got != tt.want {
				t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
