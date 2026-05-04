package parse_test

import (
	"testing"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/postgres/parse"
)

func TestParse_OnDeleteAction(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want ir.OnDeleteAction
	}{
		{name: "default restrict", sql: `CREATE TABLE c (p int REFERENCES p(id))`, want: ir.OnDeleteRestrict},
		{name: "explicit cascade", sql: `CREATE TABLE c (p int REFERENCES p(id) ON DELETE CASCADE)`, want: ir.OnDeleteCascade},
		{name: "explicit set null", sql: `CREATE TABLE c (p int REFERENCES p(id) ON DELETE SET NULL)`, want: ir.OnDeleteSetNull},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := parse.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			ct := node.(*ir.CreateTable)
			ref := ct.Columns[0].References
			if ref == nil {
				t.Fatal("References: got nil")
			}
			if ref.OnDelete != tc.want {
				t.Errorf("OnDelete: got %v, want %v", ref.OnDelete, tc.want)
			}
		})
	}
}
