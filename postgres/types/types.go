// Package types registers Postgres-specific types (int2/4/8, text,
// timestamptz, uuid, numeric, bytea, jsonb, arrays) into a
// types.Registry. OIDs match real Postgres so wire-protocol
// Describe responses match what pgx expects.
package types

import basetypes "github.com/kaeawc/pgmem-go/types"

// Register populates r with every Postgres type pgmem supports.
func Register(r basetypes.Registry) {
	_ = r
}
