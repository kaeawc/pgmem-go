package parse_test

import (
	"testing"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/postgres/parse"
)

// TestParse_Delete covers the four shapes of DELETE we recognize:
// no-where, where, where + returning, returning alone.
func TestParse_Delete(t *testing.T) {
	cases := []struct {
		name       string
		sql        string
		wantWhere  bool
		wantReturn []string
	}{
		{name: "delete all", sql: `DELETE FROM t`, wantWhere: false},
		{name: "where only", sql: `DELETE FROM t WHERE id = 1`, wantWhere: true},
		{name: "returning only", sql: `DELETE FROM t RETURNING id, name`, wantReturn: []string{"id", "name"}},
		{name: "where + returning", sql: `DELETE FROM t WHERE id = 1 RETURNING id`, wantWhere: true, wantReturn: []string{"id"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := parse.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			d, ok := node.(*ir.Delete)
			if !ok {
				t.Fatalf("plan: got %T, want *ir.Delete", node)
			}
			if (d.Where != nil) != tc.wantWhere {
				t.Errorf("Where presence: got %v, want %v", d.Where != nil, tc.wantWhere)
			}
			if len(d.ReturningNames) != len(tc.wantReturn) {
				t.Fatalf("ReturningNames: got %v, want %v", d.ReturningNames, tc.wantReturn)
			}
			for i, want := range tc.wantReturn {
				if d.ReturningNames[i] != want {
					t.Errorf("name %d: got %q, want %q", i, d.ReturningNames[i], want)
				}
			}
		})
	}
}
