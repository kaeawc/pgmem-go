package wire

import "strings"

// isClientNoop recognizes statements pgx (and similar clients) issue
// for connection setup that pgmem doesn't model yet. We acknowledge
// them with a CommandComplete so the client doesn't error out.
//
// This list grows as we discover more pgx warm-up queries. M3 turns
// BEGIN/COMMIT/ROLLBACK into real handlers.
func isClientNoop(sql string) bool {
	return noopTag(sql) != ""
}

// noopTag returns the CommandComplete tag for a recognized no-op, or
// "" if the statement is not in the no-op set.
func noopTag(sql string) string {
	upper := strings.ToUpper(strings.TrimSpace(strings.TrimRight(strings.TrimSpace(sql), ";")))
	switch {
	case strings.HasPrefix(upper, "SET "):
		return "SET"
	case upper == "BEGIN" || strings.HasPrefix(upper, "BEGIN "):
		return "BEGIN"
	case upper == "COMMIT" || strings.HasPrefix(upper, "COMMIT "):
		return "COMMIT"
	case upper == "ROLLBACK" || strings.HasPrefix(upper, "ROLLBACK "):
		return "ROLLBACK"
	default:
		return ""
	}
}
