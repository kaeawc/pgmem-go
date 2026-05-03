package parse_test

import (
	"testing"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/postgres/parse"
)

func TestParse_Update(t *testing.T) {
	cases := []struct {
		name        string
		sql         string
		wantAssigns []string // column names of each assignment
		wantWhere   bool
		wantReturn  []string
	}{
		{
			name:        "single assignment, no where",
			sql:         `UPDATE t SET label = 'x'`,
			wantAssigns: []string{"label"},
		},
		{
			name:        "multiple assignments + where",
			sql:         `UPDATE t SET label = 'x', qty = 5 WHERE id = 1`,
			wantAssigns: []string{"label", "qty"},
			wantWhere:   true,
		},
		{
			name:        "with returning",
			sql:         `UPDATE t SET label = 'x' WHERE id = 1 RETURNING id, label`,
			wantAssigns: []string{"label"},
			wantWhere:   true,
			wantReturn:  []string{"id", "label"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := parse.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			u, ok := node.(*ir.Update)
			if !ok {
				t.Fatalf("plan: got %T, want *ir.Update", node)
			}
			if len(u.Assignments) != len(tc.wantAssigns) {
				t.Fatalf("assignments: got %d, want %d", len(u.Assignments), len(tc.wantAssigns))
			}
			for i, want := range tc.wantAssigns {
				if u.Assignments[i].Column != want {
					t.Errorf("assignment %d col: got %q, want %q", i, u.Assignments[i].Column, want)
				}
			}
			if (u.Where != nil) != tc.wantWhere {
				t.Errorf("Where presence: got %v, want %v", u.Where != nil, tc.wantWhere)
			}
			if len(u.ReturningNames) != len(tc.wantReturn) {
				t.Fatalf("ReturningNames: got %v, want %v", u.ReturningNames, tc.wantReturn)
			}
		})
	}
}
