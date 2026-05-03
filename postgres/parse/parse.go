// Package parse turns a SQL string into an ir.Node tree.
//
// M2 ships a hand-rolled recursive-descent parser covering exactly the
// subset listed in MILESTONES.md (CREATE TABLE, INSERT, SELECT with
// WHERE/ORDER BY/LIMIT, $N parameters). M7 swaps in pg_query_go behind
// the cgo_pgquery build tag for fidelity-critical use cases; the IR
// boundary stays the same.
package parse

import (
	"fmt"
	"strings"

	"github.com/kaeawc/pgmem-go/ir"
)

// Parse turns a single SQL statement into its IR representation.
func Parse(sql string) (ir.Node, error) {
	trimmed := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(sql), ";"))
	toks, err := lex(trimmed)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	p := &parser{toks: toks}
	stmt, err := p.parseStmt()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tEOF {
		return nil, fmt.Errorf("parse: trailing tokens at %d (%q)", p.peek().pos, p.peek().val)
	}
	return stmt, nil
}
