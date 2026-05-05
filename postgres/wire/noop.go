package wire

import "strings"

// isClientNoop recognizes statements pgx (and similar clients) issue
// for connection setup that pgmem doesn't model semantically — SET in
// particular. Real BEGIN / COMMIT / ROLLBACK and the SAVEPOINT family
// are handled by classifyTx (they need to drive conn-scoped state).
func isClientNoop(sql string) bool {
	return noopTag(sql) != ""
}

// noopTag returns the CommandComplete tag for a recognized no-op, or
// "" if the statement is not in the no-op set. The set covers
// statements pgx-style clients and migration tools (goose, atlas,
// flyway) emit but pgmem-go has no semantic counterpart for.
func noopTag(sql string) string {
	upper := strings.ToUpper(strings.TrimSpace(strings.TrimRight(strings.TrimSpace(sql), ";")))
	switch {
	case strings.HasPrefix(upper, "SET "):
		return "SET"
	case strings.HasPrefix(upper, "RESET "):
		return "RESET"
	case upper == "DISCARD ALL" || strings.HasPrefix(upper, "DISCARD "):
		return "DISCARD"
	case strings.HasPrefix(upper, "COMMENT ON "):
		return "COMMENT"
	case strings.HasPrefix(upper, "CREATE EXTENSION "):
		return "CREATE EXTENSION"
	case strings.HasPrefix(upper, "DROP EXTENSION "):
		return "DROP EXTENSION"
	case strings.HasPrefix(upper, "CREATE SCHEMA "):
		return "CREATE SCHEMA"
	case strings.HasPrefix(upper, "DROP SCHEMA "):
		return "DROP SCHEMA"
	case strings.HasPrefix(upper, "ANALYZE") || strings.HasPrefix(upper, "ANALYSE"):
		return "ANALYZE"
	case strings.HasPrefix(upper, "VACUUM"):
		return "VACUUM"
	}
	return ""
}

// txCommand bundles the parsed transaction-control statement into a
// shape handleTxAction can dispatch on without re-parsing.
type txCommand struct {
	action txAction
	tag    string // CommandComplete tag the client expects
	name   string // savepoint identifier (empty for non-savepoint actions)
}

// classifyTx maps the transaction-control family to a txCommand. Returns
// (zero value, false) when the statement isn't recognized.
//
// Recognized: BEGIN / START TRANSACTION / COMMIT / END / ROLLBACK /
// ABORT / SAVEPOINT name / RELEASE [SAVEPOINT] name /
// ROLLBACK TO [SAVEPOINT] name. PG's synonyms are mirrored.
func classifyTx(sql string) (txCommand, bool) {
	trimmed := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(sql), ";"))
	upper := strings.ToUpper(trimmed)

	switch {
	case upper == "BEGIN" || strings.HasPrefix(upper, "BEGIN "):
		return txCommand{action: txBegin, tag: "BEGIN"}, true
	case upper == "START TRANSACTION" || strings.HasPrefix(upper, "START TRANSACTION "):
		return txCommand{action: txBegin, tag: "BEGIN"}, true
	case upper == "COMMIT" || strings.HasPrefix(upper, "COMMIT "):
		return txCommand{action: txCommit, tag: "COMMIT"}, true
	case upper == "END" || strings.HasPrefix(upper, "END "):
		return txCommand{action: txCommit, tag: "COMMIT"}, true
	}

	// ROLLBACK TO [SAVEPOINT] <name> must be tested before plain ROLLBACK.
	if name, ok := matchSavepoint(trimmed, upper, []string{"ROLLBACK TO SAVEPOINT ", "ROLLBACK TO "}); ok {
		return txCommand{action: txRollbackTo, tag: "ROLLBACK", name: name}, true
	}
	switch {
	case upper == "ROLLBACK" || strings.HasPrefix(upper, "ROLLBACK "):
		return txCommand{action: txRollback, tag: "ROLLBACK"}, true
	case upper == "ABORT" || strings.HasPrefix(upper, "ABORT "):
		return txCommand{action: txRollback, tag: "ROLLBACK"}, true
	}

	if name, ok := matchSavepoint(trimmed, upper, []string{"SAVEPOINT "}); ok {
		return txCommand{action: txSavepoint, tag: "SAVEPOINT", name: name}, true
	}
	if name, ok := matchSavepoint(trimmed, upper, []string{"RELEASE SAVEPOINT ", "RELEASE "}); ok {
		return txCommand{action: txReleaseSavepoint, tag: "RELEASE", name: name}, true
	}
	return txCommand{}, false
}

// matchSavepoint tries each prefix in turn (case-insensitive). On a
// match it returns the rest of the statement (the savepoint name) in
// its original case, with surrounding whitespace and one optional
// pair of double quotes stripped.
func matchSavepoint(orig, upper string, prefixes []string) (string, bool) {
	for _, p := range prefixes {
		if !strings.HasPrefix(upper, p) {
			continue
		}
		// Original-case slice: same byte offsets because matchPrefix is
		// upper-case-only and the original may be mixed-case but
		// length-aligned (ASCII keywords).
		rest := strings.TrimSpace(orig[len(p):])
		if rest == "" {
			return "", false
		}
		// Quoted identifier: strip one outer pair of double quotes.
		if len(rest) >= 2 && rest[0] == '"' && rest[len(rest)-1] == '"' {
			return rest[1 : len(rest)-1], true
		}
		// Otherwise take the first token (whitespace-delimited).
		if i := strings.IndexAny(rest, " \t\r\n"); i > 0 {
			rest = rest[:i]
		}
		return rest, true
	}
	return "", false
}
