package parse_test

import (
	"strings"
	"testing"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/postgres/parse"
)

func TestParse_JoinTypes(t *testing.T) {
	cases := []struct {
		name     string
		sql      string
		wantType ir.JoinType
		wantCond bool
	}{
		{name: "left", sql: `SELECT a.id FROM a LEFT JOIN b ON a.id = b.a_id`, wantType: ir.JoinLeft, wantCond: true},
		{name: "left outer", sql: `SELECT a.id FROM a LEFT OUTER JOIN b ON a.id = b.a_id`, wantType: ir.JoinLeft, wantCond: true},
		{name: "cross", sql: `SELECT a.id FROM a CROSS JOIN b`, wantType: ir.JoinCross, wantCond: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := parse.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			j := node.(*ir.Project).Input.(*ir.Join)
			if j.Type != tc.wantType {
				t.Errorf("Type: got %v, want %v", j.Type, tc.wantType)
			}
			if (j.Cond != nil) != tc.wantCond {
				t.Errorf("Cond presence: got %v, want %v", j.Cond != nil, tc.wantCond)
			}
		})
	}
}

// TestParse_CrossJoin_RejectsOn confirms we error rather than silently
// accept a confused `CROSS JOIN ... ON ...`.
func TestParse_CrossJoin_RejectsOn(t *testing.T) {
	_, err := parse.Parse(`SELECT a.id FROM a CROSS JOIN b ON a.id = b.a_id`)
	if err == nil {
		t.Fatal("Parse: want error, got nil")
	}
	if !strings.Contains(err.Error(), "CROSS JOIN") {
		t.Errorf("error %q should mention CROSS JOIN", err)
	}
}
