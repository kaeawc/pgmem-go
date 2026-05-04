package parse_test

import (
	"testing"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/postgres/parse"
)

func TestParse_DropTable(t *testing.T) {
	cases := []struct {
		name       string
		sql        string
		wantName   string
		wantIfExst bool
	}{
		{name: "plain", sql: `DROP TABLE foo`, wantName: "foo"},
		{name: "if exists", sql: `DROP TABLE IF EXISTS foo`, wantName: "foo", wantIfExst: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := parse.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			d, ok := node.(*ir.DropTable)
			if !ok {
				t.Fatalf("plan: got %T, want *ir.DropTable", node)
			}
			if d.Name != tc.wantName {
				t.Errorf("Name: got %q, want %q", d.Name, tc.wantName)
			}
			if d.IfExists != tc.wantIfExst {
				t.Errorf("IfExists: got %v, want %v", d.IfExists, tc.wantIfExst)
			}
		})
	}
}
