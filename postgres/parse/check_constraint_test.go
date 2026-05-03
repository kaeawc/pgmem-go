package parse_test

import (
	"testing"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/postgres/parse"
	"github.com/kaeawc/pgmem-go/types"
)

// TestParse_CreateTable_CheckConstraint asserts that `CHECK (expr)`
// attached to a column lands on ColumnDef.Check as the parsed
// expression. We don't dig into the expression shape; the executor
// tests cover that. Here we care that the parser recognized CHECK and
// connected the bytes after it to the column.
func TestParse_CreateTable_CheckConstraint(t *testing.T) {
	cases := []struct {
		name      string
		sql       string
		wantCheck bool
	}{
		{
			name:      "single check",
			sql:       `CREATE TABLE t (qty int CHECK (qty > 0))`,
			wantCheck: true,
		},
		{
			name:      "check after not null",
			sql:       `CREATE TABLE t (qty int NOT NULL CHECK (qty > 0))`,
			wantCheck: true,
		},
		{
			name:      "check before not null",
			sql:       `CREATE TABLE t (qty int CHECK (qty > 0) NOT NULL)`,
			wantCheck: true,
		},
		{
			name:      "no check stays nil",
			sql:       `CREATE TABLE t (qty int NOT NULL)`,
			wantCheck: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := parse.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			ct := node.(*ir.CreateTable)
			col := ct.Columns[0]
			if (col.Check != nil) != tc.wantCheck {
				t.Errorf("Check presence: got %v, want %v", col.Check != nil, tc.wantCheck)
			}
			if tc.wantCheck && col.Type != types.Int4 {
				t.Errorf("Type: got %v, want int4", col.Type)
			}
		})
	}
}
