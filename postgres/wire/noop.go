package wire

import "strings"

// isClientNoop recognizes statements pgx (and similar clients) issue
// for connection setup that pgmem doesn't model semantically — SET in
// particular. Real BEGIN / COMMIT / ROLLBACK are handled by classifyTx
// (they need to drive conn-scoped state, not be silently acked).
func isClientNoop(sql string) bool {
	return noopTag(sql) != ""
}

// noopTag returns the CommandComplete tag for a recognized no-op, or
// "" if the statement is not in the no-op set.
func noopTag(sql string) string {
	upper := strings.ToUpper(strings.TrimSpace(strings.TrimRight(strings.TrimSpace(sql), ";")))
	if strings.HasPrefix(upper, "SET ") {
		return "SET"
	}
	return ""
}

// classifyTx maps the BEGIN / COMMIT / ROLLBACK family to a (txAction,
// CommandComplete tag) pair. PG accepts a few synonyms (`START
// TRANSACTION`, `END`, `ABORT`); we mirror that.
func classifyTx(sql string) (txAction, string) {
	upper := strings.ToUpper(strings.TrimSpace(strings.TrimRight(strings.TrimSpace(sql), ";")))
	switch {
	case upper == "BEGIN" || strings.HasPrefix(upper, "BEGIN "):
		return txBegin, "BEGIN"
	case upper == "START TRANSACTION" || strings.HasPrefix(upper, "START TRANSACTION "):
		return txBegin, "BEGIN"
	case upper == "COMMIT" || strings.HasPrefix(upper, "COMMIT "):
		return txCommit, "COMMIT"
	case upper == "END" || strings.HasPrefix(upper, "END "):
		return txCommit, "COMMIT"
	case upper == "ROLLBACK" || strings.HasPrefix(upper, "ROLLBACK "):
		return txRollback, "ROLLBACK"
	case upper == "ABORT" || strings.HasPrefix(upper, "ABORT "):
		return txRollback, "ROLLBACK"
	default:
		return txNone, ""
	}
}
