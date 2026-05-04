package exec_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kaeawc/pgmem-go/catalog"
	"github.com/kaeawc/pgmem-go/exec"
	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/storage"
	"github.com/kaeawc/pgmem-go/types"
)

// TestSubstring_NegativeLength_RejectsAtExec exercises the SQLSTATE
// 22011 path. We can't get there through the SQL parser until it
// supports unary minus, so we drive the IR directly.
func TestSubstring_NegativeLength_RejectsAtExec(t *testing.T) {
	plan := &ir.Project{
		Input: &ir.Values{Rows: [][]ir.Expr{{}}},
		Exprs: []ir.Expr{&ir.FuncCall{
			Name: "substring",
			Args: []ir.Expr{
				&ir.Literal{Value: "abc", T: types.Text},
				&ir.Literal{Value: int32(1), T: types.Int4},
				&ir.Literal{Value: int32(-1), T: types.Int4},
			},
		}},
		OutputNames: []string{"x"},
	}

	sch := catalog.NewSchema()
	eng := storage.NewEngine()
	tx, _ := eng.Begin(context.Background())
	defer func() { _ = tx.Rollback() }()

	op, err := exec.Build(plan, &exec.Env{Schema: sch, Engine: eng, Txn: tx})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer op.Close()
	_, err = op.Next(context.Background())
	var sqlErr *exec.SQLError
	if !errors.As(err, &sqlErr) || sqlErr.Code != "22011" {
		t.Errorf("got %v, want SQLSTATE 22011", err)
	}
}
