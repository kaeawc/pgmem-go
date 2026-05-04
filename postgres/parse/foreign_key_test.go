package parse_test

import (
	"testing"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/postgres/parse"
)

// TestParse_References_AttachesColumnRefSpec confirms the parser
// captures the (table, column) the FK targets.
func TestParse_References_AttachesColumnRefSpec(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{name: "simple", sql: `CREATE TABLE child (parent_id int REFERENCES parent(id))`},
		{name: "with not null", sql: `CREATE TABLE child (parent_id int NOT NULL REFERENCES parent(id))`},
		{name: "with primary key elsewhere", sql: `CREATE TABLE child (id int PRIMARY KEY, parent_id int REFERENCES parent(id))`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := parse.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			ct := node.(*ir.CreateTable)
			// parent_id is the last column in each case.
			col := ct.Columns[len(ct.Columns)-1]
			if col.References == nil {
				t.Fatal("References: got nil, want non-nil")
			}
			if col.References.Table != "parent" || col.References.Column != "id" {
				t.Errorf("References: got %v, want {parent id}", col.References)
			}
		})
	}
}
