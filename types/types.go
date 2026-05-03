// Package types defines the dialect-neutral type kit. Postgres-specific
// type registrations live in postgres/types, but the core encoders live
// here so ir/exec/wire can share them without depending on the postgres
// dialect package.
package types

import (
	"encoding/binary"
	"fmt"
	"strconv"
)

// Type identifies a value's logical type and supplies wire-format
// encoders/decoders. The engine compares Types by identity, not by
// name — there is exactly one *Int4 in the program.
type Type interface {
	Name() string
	OID() uint32
	// Size is the on-wire size for fixed-width types (int4=4), or -1 for
	// variable-length types (text, bytea, ...).
	Size() int16
	EncodeText(v any) ([]byte, error)
	EncodeBinary(v any) ([]byte, error)
	// DecodeText / DecodeBinary turn PG wire bytes back into a Go value.
	// Used to unpack Bind parameter values.
	DecodeText(b []byte) (any, error)
	DecodeBinary(b []byte) (any, error)
}

// Registry maps type names to Type values. Each dialect populates a
// Registry at server construction.
type Registry interface {
	Get(name string) (Type, bool)
	Register(t Type)
	GetByOID(oid uint32) (Type, bool)
}

// --- concrete types used by M2 ---

// Int4 is PG int4 (signed 32-bit).
var Int4 Type = &int4Type{}

type int4Type struct{}

func (*int4Type) Name() string { return "int4" }
func (*int4Type) OID() uint32  { return 23 }
func (*int4Type) Size() int16  { return 4 }

func (*int4Type) EncodeText(v any) ([]byte, error) {
	n, ok := asInt64(v)
	if !ok {
		return nil, fmt.Errorf("int4 EncodeText: unsupported %T", v)
	}
	return strconv.AppendInt(nil, n, 10), nil
}

func (*int4Type) EncodeBinary(v any) ([]byte, error) {
	n, ok := asInt64(v)
	if !ok {
		return nil, fmt.Errorf("int4 EncodeBinary: unsupported %T", v)
	}
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(int32(n)))
	return b, nil
}

func (*int4Type) DecodeText(b []byte) (any, error) {
	n, err := strconv.ParseInt(string(b), 10, 32)
	if err != nil {
		return nil, fmt.Errorf("int4 DecodeText: %w", err)
	}
	return int32(n), nil
}

func (*int4Type) DecodeBinary(b []byte) (any, error) {
	if len(b) != 4 {
		return nil, fmt.Errorf("int4 DecodeBinary: want 4 bytes, got %d", len(b))
	}
	return int32(binary.BigEndian.Uint32(b)), nil
}

// Int8 is PG int8 (signed 64-bit).
var Int8 Type = &int8Type{}

type int8Type struct{}

func (*int8Type) Name() string { return "int8" }
func (*int8Type) OID() uint32  { return 20 }
func (*int8Type) Size() int16  { return 8 }

func (*int8Type) EncodeText(v any) ([]byte, error) {
	n, ok := asInt64(v)
	if !ok {
		return nil, fmt.Errorf("int8 EncodeText: unsupported %T", v)
	}
	return strconv.AppendInt(nil, n, 10), nil
}

func (*int8Type) EncodeBinary(v any) ([]byte, error) {
	n, ok := asInt64(v)
	if !ok {
		return nil, fmt.Errorf("int8 EncodeBinary: unsupported %T", v)
	}
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(n))
	return b, nil
}

func (*int8Type) DecodeText(b []byte) (any, error) {
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("int8 DecodeText: %w", err)
	}
	return n, nil
}

func (*int8Type) DecodeBinary(b []byte) (any, error) {
	if len(b) != 8 {
		return nil, fmt.Errorf("int8 DecodeBinary: want 8 bytes, got %d", len(b))
	}
	return int64(binary.BigEndian.Uint64(b)), nil
}

// asInt64 normalizes the integer types we accept into a single channel
// for encoders. Decoders return the canonical Go type (int32 or int64).
func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int32:
		return int64(n), true
	case int:
		return int64(n), true
	default:
		return 0, false
	}
}

// Text is PG text (variable-length UTF-8).
var Text Type = &textType{}

type textType struct{}

func (*textType) Name() string { return "text" }
func (*textType) OID() uint32  { return 25 }
func (*textType) Size() int16  { return -1 }

func (*textType) EncodeText(v any) ([]byte, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("text EncodeText: unsupported %T", v)
	}
	return []byte(s), nil
}

func (*textType) EncodeBinary(v any) ([]byte, error) { return (&textType{}).EncodeText(v) }

func (*textType) DecodeText(b []byte) (any, error)   { return string(b), nil }
func (*textType) DecodeBinary(b []byte) (any, error) { return string(b), nil }

// Bool is PG bool.
var Bool Type = &boolType{}

type boolType struct{}

func (*boolType) Name() string { return "bool" }
func (*boolType) OID() uint32  { return 16 }
func (*boolType) Size() int16  { return 1 }

func (*boolType) EncodeText(v any) ([]byte, error) {
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("bool EncodeText: unsupported %T", v)
	}
	if b {
		return []byte{'t'}, nil
	}
	return []byte{'f'}, nil
}

func (*boolType) EncodeBinary(v any) ([]byte, error) {
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("bool EncodeBinary: unsupported %T", v)
	}
	if b {
		return []byte{1}, nil
	}
	return []byte{0}, nil
}

func (*boolType) DecodeText(b []byte) (any, error) {
	switch string(b) {
	case "t", "true", "TRUE", "T":
		return true, nil
	case "f", "false", "FALSE", "F":
		return false, nil
	default:
		return nil, fmt.Errorf("bool DecodeText: unrecognized %q", b)
	}
}

func (*boolType) DecodeBinary(b []byte) (any, error) {
	if len(b) != 1 {
		return nil, fmt.Errorf("bool DecodeBinary: want 1 byte, got %d", len(b))
	}
	return b[0] != 0, nil
}

// ByOID looks up one of the M2 types by OID. The dialect Registry
// supersedes this once it lands; until then this is the only lookup
// path the wire layer needs.
func ByOID(oid uint32) (Type, bool) {
	switch oid {
	case 16:
		return Bool, true
	case 20:
		return Int8, true
	case 23:
		return Int4, true
	case 25:
		return Text, true
	default:
		return nil, false
	}
}

// ByName looks up by SQL type name. Used by the parser to translate
// CREATE TABLE column types.
func ByName(name string) (Type, bool) {
	switch name {
	case "int", "integer", "int4":
		return Int4, true
	case "bigint", "int8":
		return Int8, true
	case "text", "varchar":
		return Text, true
	case "bool", "boolean":
		return Bool, true
	default:
		return nil, false
	}
}
