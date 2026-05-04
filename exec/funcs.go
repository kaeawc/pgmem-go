package exec

import (
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/types"
)

// builtinFunc is the runtime shape of a SQL builtin: it reports its
// return type given the (already type-resolved) argument list and
// computes the value from the evaluated argument values. Eval gets
// env so functions like now() can consult the per-server clock the
// test harness pinned via Server.SetNow.
type builtinFunc struct {
	ResultType func(args []ir.Expr) (types.Type, error)
	Eval       func(env *Env, args []any) (any, error)
}

// builtins is the static registry of supported builtins. Lower-case
// names. Each function is expected to validate arity in ResultType so
// the arity error surfaces at exec.Build time rather than per-row.
var builtins = map[string]builtinFunc{
	"gen_random_uuid": {
		ResultType: noArgs(types.UUID),
		Eval: func(_ *Env, _ []any) (any, error) {
			var b [16]byte
			if _, err := rand.Read(b[:]); err != nil {
				return nil, fmt.Errorf("gen_random_uuid: %w", err)
			}
			// RFC 4122 §4.4: set the version (4) and variant (RFC 4122)
			// bits so clients that introspect the value see a v4 UUID.
			b[6] = (b[6] & 0x0F) | 0x40
			b[8] = (b[8] & 0x3F) | 0x80
			return b, nil
		},
	},
	"current_timestamp": {
		// SQL-standard synonym for now(). Real PG returns the
		// transaction start time; we use the engine clock.
		ResultType: noArgs(types.Timestamptz),
		Eval: func(env *Env, _ []any) (any, error) {
			if env != nil && env.Now != nil {
				return env.Now().UTC(), nil
			}
			return time.Now().UTC(), nil
		},
	},
	"current_date": {
		// Real PG returns `date`; we don't yet model `date` as a
		// distinct type, so we emit a midnight-UTC timestamptz. Tests
		// can read the date part via EXTRACT or a comparison.
		ResultType: noArgs(types.Timestamptz),
		Eval: func(env *Env, _ []any) (any, error) {
			now := time.Now().UTC()
			if env != nil && env.Now != nil {
				now = env.Now().UTC()
			}
			return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), nil
		},
	},
	"current_time": {
		// Real PG returns `time with time zone`; we approximate with
		// a timestamptz pinned to the Unix epoch's date plus the time
		// component. Good enough for the typical sqlc usage.
		ResultType: noArgs(types.Timestamptz),
		Eval: func(env *Env, _ []any) (any, error) {
			now := time.Now().UTC()
			if env != nil && env.Now != nil {
				now = env.Now().UTC()
			}
			return time.Date(1970, 1, 1, now.Hour(), now.Minute(), now.Second(), now.Nanosecond(), time.UTC), nil
		},
	},
	"now": {
		ResultType: noArgs(types.Timestamptz),
		// PG's real now() returns the transaction start time (constant
		// within a tx). We approximate with the engine clock at eval
		// time so test code that pinned Server.SetNow(...) sees a
		// deterministic value; if no clock is pinned we fall back to
		// time.Now().
		Eval: func(env *Env, _ []any) (any, error) {
			if env != nil && env.Now != nil {
				return env.Now().UTC(), nil
			}
			return time.Now().UTC(), nil
		},
	},
	"lower": {
		ResultType: oneArg(types.Text),
		Eval:       evalUnaryString(strings.ToLower),
	},
	"upper": {
		ResultType: oneArg(types.Text),
		Eval:       evalUnaryString(strings.ToUpper),
	},
	"coalesce": {
		// PG resolves COALESCE's result type to the common element type
		// of its arguments. For our subset we take args[0].Type() and
		// require subsequent args to share it (or be untyped NULL with
		// a nil Type) — sqlc-generated calls always have homogeneous
		// argument types.
		ResultType: func(args []ir.Expr) (types.Type, error) {
			if len(args) == 0 {
				return nil, fmt.Errorf("coalesce: no arguments")
			}
			return firstNonNilType(args), nil
		},
		Eval: func(_ *Env, args []any) (any, error) {
			for _, a := range args {
				if a != nil {
					return a, nil
				}
			}
			return nil, nil
		},
	},
	"nullif": {
		// NULLIF(a, b) returns NULL if a == b else a, with the result
		// type of a.
		ResultType: func(args []ir.Expr) (types.Type, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("nullif: takes 2 arguments, got %d", len(args))
			}
			t := args[0].Type()
			if t == nil {
				t = args[1].Type()
			}
			if t == nil {
				t = types.Text
			}
			return t, nil
		},
		Eval: func(_ *Env, args []any) (any, error) {
			a, b := args[0], args[1]
			if a == nil || b == nil {
				return a, nil
			}
			cmp, err := compareForEquality(a, b)
			if err != nil {
				return nil, err
			}
			if cmp {
				return nil, nil
			}
			return a, nil
		},
	},
	"length": {
		ResultType: oneArg(types.Int4),
		// PG length() on text returns int (int4) and counts characters
		// — UTF-8 code points, not bytes. We use rune count for the
		// same behaviour.
		Eval: func(_ *Env, args []any) (any, error) {
			if args[0] == nil {
				return nil, nil
			}
			s, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("length: want text, got %T", args[0])
			}
			return int32(len([]rune(s))), nil
		},
	},
	"trim": {
		ResultType: oneArg(types.Text),
		// trim(s) — PG default is "trim BOTH whitespace from s". Other
		// variants (LEADING / TRAILING / custom-character set) require
		// keyword syntax we don't parse yet.
		Eval: evalUnaryString(strings.TrimSpace),
	},
	"ltrim": {
		ResultType: trimResultType("ltrim"),
		Eval:       evalTrim(strings.TrimLeft, " \t\n\r\v\f"),
	},
	"rtrim": {
		ResultType: trimResultType("rtrim"),
		Eval:       evalTrim(strings.TrimRight, " \t\n\r\v\f"),
	},
	"btrim": {
		ResultType: trimResultType("btrim"),
		Eval:       evalTrim(trimBoth, " \t\n\r\v\f"),
	},
	"char_length": {
		ResultType: oneArg(types.Int4),
		// char_length / character_length: same character count as
		// length(text) — runes, not bytes.
		Eval: func(_ *Env, args []any) (any, error) {
			if args[0] == nil {
				return nil, nil
			}
			s, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("char_length: want text, got %T", args[0])
			}
			return int32(len([]rune(s))), nil
		},
	},
	"character_length": {
		ResultType: oneArg(types.Int4),
		Eval: func(_ *Env, args []any) (any, error) {
			if args[0] == nil {
				return nil, nil
			}
			s, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("character_length: want text, got %T", args[0])
			}
			return int32(len([]rune(s))), nil
		},
	},
	"octet_length": {
		ResultType: oneArg(types.Int4),
		// octet_length: byte length of the UTF-8 encoding for text;
		// raw byte length for bytea.
		Eval: func(_ *Env, args []any) (any, error) {
			if args[0] == nil {
				return nil, nil
			}
			switch v := args[0].(type) {
			case string:
				return int32(len(v)), nil
			case []byte:
				return int32(len(v)), nil
			default:
				return nil, fmt.Errorf("octet_length: want text or bytea, got %T", args[0])
			}
		},
	},
	"replace": {
		// replace(s, from, to) replaces *every* non-overlapping
		// occurrence of from with to. Three text args; returns text.
		ResultType: func(args []ir.Expr) (types.Type, error) {
			if len(args) != 3 {
				return nil, fmt.Errorf("replace: takes 3 arguments, got %d", len(args))
			}
			return types.Text, nil
		},
		Eval: func(_ *Env, args []any) (any, error) {
			if args[0] == nil || args[1] == nil || args[2] == nil {
				return nil, nil
			}
			return strings.ReplaceAll(textArg(args[0]), textArg(args[1]), textArg(args[2])), nil
		},
	},
	"substring": {
		// substring(s, from[, length]) — 1-indexed character offsets,
		// matching PG. Two- and three-arg forms supported.
		ResultType: func(args []ir.Expr) (types.Type, error) {
			if len(args) != 2 && len(args) != 3 {
				return nil, fmt.Errorf("substring: takes 2 or 3 arguments, got %d", len(args))
			}
			return types.Text, nil
		},
		Eval: func(_ *Env, args []any) (any, error) {
			if args[0] == nil || args[1] == nil || (len(args) == 3 && args[2] == nil) {
				return nil, nil
			}
			s := []rune(textArg(args[0]))
			from, err := substringIntArg(args[1])
			if err != nil {
				return nil, err
			}
			// PG substring is 1-indexed. A from < 1 doesn't shift the
			// start *backwards* into negative territory — it shortens
			// the captured slice instead. So `substring('abc', 0, 2)`
			// is `'a'`, not `'ab'`. We mimic that by treating count as
			// "characters between from and from+count-1, intersected
			// with positions [1, len]".
			//
			// Without a count (2-arg form) the upper bound is "to the
			// end of the string".
			if len(args) == 3 {
				count, err := substringIntArg(args[2])
				if err != nil {
					return nil, err
				}
				if count < 0 {
					return nil, &SQLError{Code: "22011", Message: "negative substring length not allowed"}
				}
				start, end := clampSubstringRange(from, count, len(s))
				return string(s[start:end]), nil
			}
			start := from - 1
			if start < 0 {
				start = 0
			}
			if start > len(s) {
				start = len(s)
			}
			return string(s[start:]), nil
		},
	},
	"concat": {
		// concat(...) — variadic, returns text. NULL arguments are
		// skipped (treated as empty), matching real PG and unlike `||`
		// which propagates NULL.
		ResultType: func(_ []ir.Expr) (types.Type, error) { return types.Text, nil },
		Eval: func(_ *Env, args []any) (any, error) {
			var b strings.Builder
			for _, a := range args {
				if a == nil {
					continue
				}
				b.WriteString(concatString(a))
			}
			return b.String(), nil
		},
	},
	"concat_ws": {
		// concat_ws(sep, args...) — separator-joined concat. A NULL
		// separator returns NULL (matches PG); NULLs among the args are
		// skipped.
		ResultType: func(args []ir.Expr) (types.Type, error) {
			if len(args) < 1 {
				return nil, fmt.Errorf("concat_ws: takes at least 1 argument (separator)")
			}
			return types.Text, nil
		},
		Eval: func(_ *Env, args []any) (any, error) {
			if args[0] == nil {
				return nil, nil
			}
			sep := concatString(args[0])
			var b strings.Builder
			first := true
			for _, a := range args[1:] {
				if a == nil {
					continue
				}
				if !first {
					b.WriteString(sep)
				}
				b.WriteString(concatString(a))
				first = false
			}
			return b.String(), nil
		},
	},
	"abs": {
		// abs(int) — preserves the operand's integer width.
		ResultType: func(args []ir.Expr) (types.Type, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("abs: takes 1 argument, got %d", len(args))
			}
			t := args[0].Type()
			if t == nil {
				t = types.Int4
			}
			return t, nil
		},
		Eval: func(_ *Env, args []any) (any, error) {
			if args[0] == nil {
				return nil, nil
			}
			switch v := args[0].(type) {
			case int32:
				if v < 0 {
					return -v, nil
				}
				return v, nil
			case int64:
				if v < 0 {
					return -v, nil
				}
				return v, nil
			default:
				return nil, fmt.Errorf("abs: want integer, got %T", args[0])
			}
		},
	},
	"mod": {
		// mod(a, b) — integer remainder. Sign follows the dividend, like
		// PG. Division by zero is SQLSTATE 22012.
		ResultType: func(args []ir.Expr) (types.Type, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("mod: takes 2 arguments, got %d", len(args))
			}
			t := args[0].Type()
			if args[1].Type() == types.Int8 {
				t = types.Int8
			}
			if t == nil {
				t = types.Int4
			}
			return t, nil
		},
		Eval: func(_ *Env, args []any) (any, error) {
			if args[0] == nil || args[1] == nil {
				return nil, nil
			}
			a, err := toInt64(args[0])
			if err != nil {
				return nil, fmt.Errorf("mod: %w", err)
			}
			b, err := toInt64(args[1])
			if err != nil {
				return nil, fmt.Errorf("mod: %w", err)
			}
			if b == 0 {
				return nil, &SQLError{Code: "22012", Message: "division by zero"}
			}
			return a % b, nil
		},
	},
	"greatest": {
		ResultType: variadicSameType("greatest"),
		Eval:       variadicReduce(func(cmp int) bool { return cmp > 0 }),
	},
	"least": {
		ResultType: variadicSameType("least"),
		Eval:       variadicReduce(func(cmp int) bool { return cmp < 0 }),
	},
	"current_setting": {
		// current_setting(name [, missing_ok]) — returns the GUC value as
		// text. We don't yet model PG's full configuration system, so we
		// answer from a small static table of values ORMs typically
		// probe at connect time. Unknown names error unless missing_ok
		// is true (returns NULL).
		ResultType: func(args []ir.Expr) (types.Type, error) {
			if len(args) != 1 && len(args) != 2 {
				return nil, fmt.Errorf("current_setting: takes 1 or 2 arguments, got %d", len(args))
			}
			return types.Text, nil
		},
		Eval: func(_ *Env, args []any) (any, error) {
			if args[0] == nil {
				return nil, nil
			}
			name, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("current_setting: name must be text, got %T", args[0])
			}
			if v, ok := defaultGUC(name); ok {
				return v, nil
			}
			missingOK := false
			if len(args) == 2 {
				if b, ok := args[1].(bool); ok {
					missingOK = b
				}
			}
			if missingOK {
				return nil, nil
			}
			return nil, &SQLError{
				Code:    "42704",
				Message: fmt.Sprintf("unrecognized configuration parameter %q", name),
			}
		},
	},
	"version": {
		ResultType: noArgs(types.Text),
		Eval: func(_ *Env, _ []any) (any, error) {
			return "PostgreSQL 16.0 (pgmem-go) on " + runtimeArch() + ", compiled by Go", nil
		},
	},
	"interval": {
		// interval('1 day') / interval('5 hours') — parses a small
		// subset of the PG interval string. Returns time.Duration; the
		// arithmetic path in evalArith handles timestamp ± interval.
		ResultType: func(args []ir.Expr) (types.Type, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("interval: takes 1 argument, got %d", len(args))
			}
			return types.Interval, nil
		},
		Eval: func(_ *Env, args []any) (any, error) {
			if args[0] == nil {
				return nil, nil
			}
			s, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("interval: expected text, got %T", args[0])
			}
			return parseInterval(s)
		},
	},
	"date_trunc": {
		// date_trunc(field, ts) returns the timestamp truncated to the
		// named precision. Fields: year, month, day, hour, minute,
		// second, week. Real PG also accepts millennium / century /
		// decade / quarter / millisecond / microsecond — those arrive
		// when a real query needs them.
		ResultType: func(args []ir.Expr) (types.Type, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("date_trunc: takes 2 arguments, got %d", len(args))
			}
			return types.Timestamptz, nil
		},
		Eval: func(_ *Env, args []any) (any, error) {
			if args[0] == nil || args[1] == nil {
				return nil, nil
			}
			field, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("date_trunc: field must be text, got %T", args[0])
			}
			ts, ok := args[1].(time.Time)
			if !ok {
				return nil, fmt.Errorf("date_trunc: source must be timestamp, got %T", args[1])
			}
			return dateTrunc(field, ts)
		},
	},
	"date_part": {
		// date_part(field, ts) — real PG returns double precision; our
		// supported fields are whole numbers, so int8 is the closest
		// fit (epoch in particular can exceed int4 for far-future
		// timestamps). EXTRACT desugars to this.
		ResultType: func(args []ir.Expr) (types.Type, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("date_part: takes 2 arguments, got %d", len(args))
			}
			return types.Int8, nil
		},
		Eval: func(_ *Env, args []any) (any, error) {
			if args[0] == nil || args[1] == nil {
				return nil, nil
			}
			field, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("date_part: field must be text, got %T", args[0])
			}
			ts, ok := args[1].(time.Time)
			if !ok {
				return nil, fmt.Errorf("date_part: source must be timestamp, got %T", args[1])
			}
			return datePart(field, ts)
		},
	},
	"strpos": {
		// strpos(haystack, needle) — 1-indexed position of needle in
		// haystack, or 0 when not found. Function-form alias for
		// `position(needle in haystack)` which uses keyword syntax we
		// don't parse yet.
		ResultType: func(args []ir.Expr) (types.Type, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("strpos: takes 2 arguments, got %d", len(args))
			}
			return types.Int4, nil
		},
		Eval: func(_ *Env, args []any) (any, error) {
			if args[0] == nil || args[1] == nil {
				return nil, nil
			}
			haystack := textArg(args[0])
			needle := textArg(args[1])
			if needle == "" {
				return int32(1), nil
			}
			byteIdx := strings.Index(haystack, needle)
			if byteIdx < 0 {
				return int32(0), nil
			}
			// PG returns a 1-indexed *character* position; convert from
			// byte offset by counting runes in the prefix.
			return int32(len([]rune(haystack[:byteIdx])) + 1), nil
		},
	},
}

// defaultGUC returns the canned value we serve for a small set of PG
// configuration parameters that ORMs and pgx commonly probe at
// connect time. Names are matched case-insensitively (PG GUCs are).
func defaultGUC(name string) (string, bool) {
	switch strings.ToLower(name) {
	case "server_version":
		return "16.0", true
	case "server_version_num":
		return "160000", true
	case "search_path":
		return "public", true
	case "standard_conforming_strings", "integer_datetimes":
		return "on", true
	case "timezone":
		return "UTC", true
	case "client_encoding", "server_encoding":
		return "UTF8", true
	case "application_name":
		return "", true
	}
	return "", false
}

// runtimeArch returns a human label for version()'s formatted output.
// We don't pull runtime.GOARCH here because adding the runtime import
// for a single string isn't worth it; "x86_64-pc-linux-gnu" is what
// most clients expect to see anyway.
func runtimeArch() string { return "x86_64-pc-linux-gnu" }

// parseInterval handles the small subset of PG interval text we need:
// one or more `<n> <unit>` pairs separated by whitespace, where unit
// is one of day(s)/hour(s)/minute(s)/second(s)/week(s)/month(s)/year(s).
// Months and years approximate (30 / 365.25 days).
func parseInterval(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("interval: empty string")
	}
	parts := strings.Fields(s)
	if len(parts)%2 != 0 {
		return 0, fmt.Errorf("interval: odd token count in %q", s)
	}
	var total time.Duration
	for i := 0; i < len(parts); i += 2 {
		n, err := strconv.ParseInt(parts[i], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("interval: bad number %q", parts[i])
		}
		unit := strings.ToLower(parts[i+1])
		unit = strings.TrimSuffix(unit, "s") // plural → singular
		var step time.Duration
		switch unit {
		case "second", "sec":
			step = time.Second
		case "minute", "min":
			step = time.Minute
		case "hour":
			step = time.Hour
		case "day":
			step = 24 * time.Hour
		case "week":
			step = 7 * 24 * time.Hour
		case "month":
			step = 30 * 24 * time.Hour
		case "year":
			step = time.Duration(float64(24*time.Hour) * 365.25)
		default:
			return 0, fmt.Errorf("interval: unsupported unit %q", parts[i+1])
		}
		total += time.Duration(n) * step
	}
	return total, nil
}

// dateTrunc returns ts truncated to the named precision. Behaviour
// matches real PG for the supported fields.
func dateTrunc(field string, ts time.Time) (any, error) {
	t := ts.UTC()
	switch strings.ToLower(field) {
	case "year":
		return time.Date(t.Year(), 1, 1, 0, 0, 0, 0, time.UTC), nil
	case "month":
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC), nil
	case "day":
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
	case "hour":
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC), nil
	case "minute":
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC), nil
	case "second":
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC), nil
	case "week":
		// Truncate to Monday — matches PG. Go's Weekday: Sunday=0
		// through Saturday=6; we convert to ISO Monday-based offset.
		offset := (int(t.Weekday()) + 6) % 7
		monday := t.AddDate(0, 0, -offset)
		return time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, time.UTC), nil
	default:
		return nil, fmt.Errorf("date_trunc: unsupported field %q", field)
	}
}

// datePart returns the value of `field` extracted from `ts`. Field
// names are lower-case; we cover the set sqlc-generated queries
// commonly emit. Unknown fields error so callers don't silently get
// zero.
func datePart(field string, ts time.Time) (any, error) {
	t := ts.UTC()
	switch strings.ToLower(field) {
	case "year":
		return int64(t.Year()), nil
	case "month":
		return int64(int(t.Month())), nil
	case "day":
		return int64(t.Day()), nil
	case "hour":
		return int64(t.Hour()), nil
	case "minute":
		return int64(t.Minute()), nil
	case "second":
		return int64(t.Second()), nil
	case "dow":
		// PG: 0 = Sunday … 6 = Saturday. Go's time.Weekday matches.
		return int64(int(t.Weekday())), nil
	case "doy":
		return int64(t.YearDay()), nil
	case "week":
		_, w := t.ISOWeek()
		return int64(w), nil
	case "epoch":
		return ts.Unix(), nil
	default:
		return nil, fmt.Errorf("date_part: unsupported field %q", field)
	}
}

// trimResultType validates the arity and returns text. ltrim, rtrim,
// btrim accept either one argument (default whitespace) or two
// arguments (custom character set).
func trimResultType(name string) func(args []ir.Expr) (types.Type, error) {
	return func(args []ir.Expr) (types.Type, error) {
		if len(args) != 1 && len(args) != 2 {
			return nil, fmt.Errorf("%s: takes 1 or 2 arguments, got %d", name, len(args))
		}
		return types.Text, nil
	}
}

// trimBoth strips a cutset from both ends. Closure-compatible with
// strings.TrimLeft / TrimRight so all three trims share evalTrim.
func trimBoth(s, cutset string) string {
	return strings.Trim(s, cutset)
}

// evalTrim builds an Eval that trims using the supplied function. The
// trim character set comes from args[1] when present, otherwise the
// fallback (whitespace) is used. NULLs propagate.
func evalTrim(fn func(string, string) string, defaultCutset string) func(env *Env, args []any) (any, error) {
	return func(_ *Env, args []any) (any, error) {
		if args[0] == nil {
			return nil, nil
		}
		s, ok := args[0].(string)
		if !ok {
			return nil, fmt.Errorf("trim: want text, got %T", args[0])
		}
		cutset := defaultCutset
		if len(args) == 2 {
			if args[1] == nil {
				return nil, nil
			}
			c, ok := args[1].(string)
			if !ok {
				return nil, fmt.Errorf("trim: cutset must be text, got %T", args[1])
			}
			cutset = c
		}
		return fn(s, cutset), nil
	}
}

// variadicSameType is the ResultType for greatest/least: the result
// type matches the operands. We pick the first non-nil-typed argument
// (mirroring PG's "first known type wins" rule for these polymorphic
// builtins).
func variadicSameType(name string) func(args []ir.Expr) (types.Type, error) {
	return func(args []ir.Expr) (types.Type, error) {
		if len(args) == 0 {
			return nil, fmt.Errorf("%s: takes at least 1 argument", name)
		}
		for _, a := range args {
			if a.Type() != nil {
				return a.Type(), nil
			}
		}
		return types.Int4, nil
	}
}

// variadicReduce folds args left-to-right with prefer(cmp(current,
// candidate)) selecting the running winner. NULLs are skipped; if all
// args are NULL the result is NULL — matching real PG.
func variadicReduce(prefer func(cmp int) bool) func(env *Env, args []any) (any, error) {
	return func(_ *Env, args []any) (any, error) {
		var winner any
		for _, a := range args {
			if a == nil {
				continue
			}
			if winner == nil {
				winner = a
				continue
			}
			cmp, err := compareValues(a, winner)
			if err != nil {
				return nil, err
			}
			if prefer(cmp) {
				winner = a
			}
		}
		return winner, nil
	}
}

// textArg is the soft cast every string-shaped builtin uses on its
// already-non-nil argument. Plain strings pass through; everything
// else gets fmt.Sprint as a forgiving fallback.
func textArg(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// clampSubstringRange computes the [start, end) rune-index slice for
// substring(s, from, count) under PG's "1-indexed; from<1 shortens the
// captured slice rather than shifting it" rule. length is len([]rune(s)).
//
// Examples (from, count, length) → (start, end):
//
//	(7, 5, 11) → (6, 11)   `substring('hello world', 7, 5)` = "world"
//	(0, 2, 3)  → (0, 1)    `substring('abc', 0, 2)`         = "a"
//	(2, 100, 3) → (1, 3)   `substring('abc', 2, 100)`       = "bc"
func clampSubstringRange(from, count, length int) (int, int) {
	upper := from + count - 1 // 1-indexed inclusive upper bound
	if from < 1 {
		from = 1
	}
	start := from - 1
	end := upper
	if end > length {
		end = length
	}
	if end < start {
		end = start
	}
	return start, end
}

// substringIntArg coerces a substring offset/length into a Go int.
// substring's int args are int4 in PG so we accept int32, int64, and
// int for ergonomics.
func substringIntArg(v any) (int, error) {
	switch n := v.(type) {
	case int32:
		return int(n), nil
	case int64:
		return int(n), nil
	case int:
		return n, nil
	default:
		return 0, fmt.Errorf("substring: int arg got %T", v)
	}
}

// firstNonNilType returns the first arg's non-nil Type, falling back
// to text if every arg is an untyped NULL literal. PG would error in
// that case ("could not determine data type"); we pick a defensible
// stand-in so the wire layer doesn't crash on a nil OID lookup.
func firstNonNilType(args []ir.Expr) types.Type {
	for _, a := range args {
		if t := a.Type(); t != nil {
			return t
		}
	}
	return types.Text
}

// compareForEquality is a thin wrapper around exec.compareValues that
// returns true iff the two values compare equal. Both are guaranteed
// non-nil by the caller.
func compareForEquality(a, b any) (bool, error) {
	cmp, err := compareValues(a, b)
	if err != nil {
		return false, err
	}
	return cmp == 0, nil
}

// oneArg is a ResultType for fixed-1-arg functions returning t.
func oneArg(t types.Type) func([]ir.Expr) (types.Type, error) {
	return func(args []ir.Expr) (types.Type, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("function takes 1 argument, got %d", len(args))
		}
		return t, nil
	}
}

// evalUnaryString lifts a string→string function into the builtin
// Eval shape. NULL input → NULL output. Non-string input is rejected.
func evalUnaryString(fn func(string) string) func(*Env, []any) (any, error) {
	return func(_ *Env, args []any) (any, error) {
		if args[0] == nil {
			return nil, nil
		}
		s, ok := args[0].(string)
		if !ok {
			return nil, fmt.Errorf("want text, got %T", args[0])
		}
		return fn(s), nil
	}
}

// noArgs returns a ResultType that errors unless the call has zero
// arguments, then yields t.
func noArgs(t types.Type) func([]ir.Expr) (types.Type, error) {
	return func(args []ir.Expr) (types.Type, error) {
		if len(args) != 0 {
			return nil, fmt.Errorf("function takes no arguments")
		}
		return t, nil
	}
}

// lookupBuiltin finds the named builtin and returns a clean error when
// it isn't registered. Lower-cases the lookup so SQL's case-insensitive
// behaviour for unquoted identifiers works.
func lookupBuiltin(name string) (builtinFunc, error) {
	if fn, ok := builtins[name]; ok {
		return fn, nil
	}
	return builtinFunc{}, fmt.Errorf("function %q does not exist", name)
}
