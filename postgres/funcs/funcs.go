// Package funcs registers Postgres builtin functions (now, coalesce,
// jsonb_*, gen_random_uuid, ...) into the expression evaluator.
//
// Time and randomness route through the server's clock.Clock and
// rand.Source so tests can pin them.
package funcs

// Func is a callable builtin. Concrete signatures and the registry
// shape land with M2.
type Func interface {
	Name() string
}
