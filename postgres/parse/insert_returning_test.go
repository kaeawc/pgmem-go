package parse_test

import (
	"testing"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/postgres/parse"
)

// TestParse_Insert_Returning checks that the parser captures the
// RETURNING list as Insert.Returning + Insert.ReturningNames in the
// same shape the SELECT-list parser produces.
func TestParse_Insert_Returning(t *testing.T) {
	cases := []struct {
		name      string
		sql       string
		wantNames []string
	}{
		{
			name:      "no returning",
			sql:       `INSERT INTO t (id) VALUES (1)`,
			wantNames: nil,
		},
		{
			name:      "single column",
			sql:       `INSERT INTO t (id, name) VALUES (1, 'a') RETURNING id`,
			wantNames: []string{"id"},
		},
		{
			name:      "multi column",
			sql:       `INSERT INTO t (id, name) VALUES (1, 'a') RETURNING id, name`,
			wantNames: []string{"id", "name"},
		},
		{
			name:      "with explicit alias",
			sql:       `INSERT INTO t (id) VALUES (1) RETURNING id AS new_id`,
			wantNames: []string{"new_id"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := parse.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			ins := node.(*ir.Insert)
			if len(ins.ReturningNames) != len(tc.wantNames) {
				t.Fatalf("ReturningNames len: got %d (%v), want %d (%v)",
					len(ins.ReturningNames), ins.ReturningNames, len(tc.wantNames), tc.wantNames)
			}
			for i, want := range tc.wantNames {
				if ins.ReturningNames[i] != want {
					t.Errorf("name %d: got %q, want %q", i, ins.ReturningNames[i], want)
				}
			}
			if (ins.Returning == nil) != (len(tc.wantNames) == 0) {
				t.Errorf("Returning expr presence mismatch: got %v, want %v",
					ins.Returning != nil, len(tc.wantNames) > 0)
			}
		})
	}
}
