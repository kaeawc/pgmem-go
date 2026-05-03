package parse_test

import (
	"testing"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/postgres/parse"
	"github.com/kaeawc/pgmem-go/types"
)

// TestParse_Serial_DesugarsToIntPlusAuto confirms SERIAL/BIGSERIAL
// flatten into the underlying int type with NotNull and Auto set.
func TestParse_Serial_DesugarsToIntPlusAuto(t *testing.T) {
	cases := []struct {
		name     string
		sql      string
		wantType types.Type
	}{
		{name: "serial", sql: `CREATE TABLE t (id serial)`, wantType: types.Int4},
		{name: "bigserial", sql: `CREATE TABLE t (id bigserial)`, wantType: types.Int8},
		{name: "serial primary key", sql: `CREATE TABLE t (id serial PRIMARY KEY)`, wantType: types.Int4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := parse.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			ct := node.(*ir.CreateTable)
			col := ct.Columns[0]
			if col.Type != tc.wantType {
				t.Errorf("Type: got %v, want %v", col.Type, tc.wantType)
			}
			if !col.Auto {
				t.Errorf("Auto: got false, want true")
			}
			if !col.NotNull {
				t.Errorf("NotNull: got false, want true (SERIAL implies NOT NULL)")
			}
		})
	}
}
