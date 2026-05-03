package parse_test

import (
	"testing"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/postgres/parse"
	"github.com/kaeawc/pgmem-go/types"
)

// TestParse_CreateTable_ColumnConstraints checks that the parser
// preserves NOT NULL / UNIQUE / PRIMARY KEY in the IR. PRIMARY KEY must
// desugar to NotNull && Unique — the catalog wouldn't otherwise know
// to enforce both.
func TestParse_CreateTable_ColumnConstraints(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []ir.ColumnDef
	}{
		{
			name: "primary key desugars to not null + unique",
			sql:  `CREATE TABLE t (id int PRIMARY KEY, name text)`,
			want: []ir.ColumnDef{
				{Name: "id", Type: types.Int4, NotNull: true, Unique: true},
				{Name: "name", Type: types.Text},
			},
		},
		{
			name: "explicit unique alone",
			sql:  `CREATE TABLE t (slug text UNIQUE, body text)`,
			want: []ir.ColumnDef{
				{Name: "slug", Type: types.Text, Unique: true},
				{Name: "body", Type: types.Text},
			},
		},
		{
			name: "not null and unique combined in either order",
			sql:  `CREATE TABLE t (a int NOT NULL UNIQUE, b int UNIQUE NOT NULL)`,
			want: []ir.ColumnDef{
				{Name: "a", Type: types.Int4, NotNull: true, Unique: true},
				{Name: "b", Type: types.Int4, NotNull: true, Unique: true},
			},
		},
		{
			name: "no constraints stays default",
			sql:  `CREATE TABLE t (n int)`,
			want: []ir.ColumnDef{
				{Name: "n", Type: types.Int4},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := parse.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			ct, ok := node.(*ir.CreateTable)
			if !ok {
				t.Fatalf("plan: got %T, want *ir.CreateTable", node)
			}
			if len(ct.Columns) != len(tc.want) {
				t.Fatalf("columns: got %d, want %d", len(ct.Columns), len(tc.want))
			}
			for i, got := range ct.Columns {
				if got != tc.want[i] {
					t.Errorf("col %d: got %+v, want %+v", i, got, tc.want[i])
				}
			}
		})
	}
}
