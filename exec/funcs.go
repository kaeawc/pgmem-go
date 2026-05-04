package exec

import (
	"crypto/rand"
	"fmt"
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
