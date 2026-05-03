package exec

import (
	"crypto/rand"
	"fmt"

	"github.com/kaeawc/pgmem-go/ir"
	"github.com/kaeawc/pgmem-go/types"
)

// builtinFunc is the runtime shape of a SQL builtin: it reports its
// return type given the (already type-resolved) argument list and
// computes the value from the evaluated argument values.
type builtinFunc struct {
	ResultType func(args []ir.Expr) (types.Type, error)
	Eval       func(args []any) (any, error)
}

// builtins is the static registry of supported builtins. Lower-case
// names. Each function is expected to validate arity in ResultType so
// the arity error surfaces at exec.Build time rather than per-row.
var builtins = map[string]builtinFunc{
	"gen_random_uuid": {
		ResultType: noArgs(types.UUID),
		Eval: func(_ []any) (any, error) {
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
