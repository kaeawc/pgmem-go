// Package types defines the dialect-neutral type kit. Postgres-specific
// type registrations live in postgres/types, but the core encoders live
// here so ir/exec/wire can share them without depending on the postgres
// dialect package.
package types

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"time"
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

// Bytea is PG bytea (variable-length binary). Internally we hold
// values as []byte. PG's text format is `\xHEX` (lower-case hex with
// a `\x` prefix); binary is the raw bytes themselves.
var Bytea Type = &byteaType{}

type byteaType struct{}

func (*byteaType) Name() string { return "bytea" }
func (*byteaType) OID() uint32  { return 17 }
func (*byteaType) Size() int16  { return -1 }

func (*byteaType) EncodeText(v any) ([]byte, error) {
	b, err := byteaBytes(v)
	if err != nil {
		return nil, fmt.Errorf("bytea EncodeText: %w", err)
	}
	out := make([]byte, 2+hex.EncodedLen(len(b)))
	out[0] = '\\'
	out[1] = 'x'
	hex.Encode(out[2:], b)
	return out, nil
}

func (*byteaType) EncodeBinary(v any) ([]byte, error) {
	b, err := byteaBytes(v)
	if err != nil {
		return nil, fmt.Errorf("bytea EncodeBinary: %w", err)
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out, nil
}

func (*byteaType) DecodeText(b []byte) (any, error) {
	if len(b) >= 2 && b[0] == '\\' && (b[1] == 'x' || b[1] == 'X') {
		out := make([]byte, hex.DecodedLen(len(b)-2))
		if _, err := hex.Decode(out, b[2:]); err != nil {
			return nil, fmt.Errorf("bytea DecodeText: %w", err)
		}
		return out, nil
	}
	return nil, fmt.Errorf("bytea DecodeText: only the \\xHEX form is supported (got %q)", b)
}

func (*byteaType) DecodeBinary(b []byte) (any, error) {
	out := make([]byte, len(b))
	copy(out, b)
	return out, nil
}

func byteaBytes(v any) ([]byte, error) {
	switch x := v.(type) {
	case []byte:
		return x, nil
	case string:
		return []byte(x), nil
	default:
		return nil, fmt.Errorf("unsupported %T", v)
	}
}

// JSONB is PG jsonb (OID 3802). Internally we hold the canonical JSON
// bytes; the binary wire format is a single 0x01 version byte followed
// by the JSON, the text format is the raw JSON. This is enough for
// pgx-style clients to round-trip JSON values without a parser.
//
// We do not normalize (no key sorting, no whitespace stripping) — v1
// keeps whatever the client sent. Two equivalent-but-differently-
// formatted documents won't compare equal yet; matches PG only for
// already-canonical input.
var JSONB Type = &jsonbType{}

type jsonbType struct{}

func (*jsonbType) Name() string { return "jsonb" }
func (*jsonbType) OID() uint32  { return 3802 }
func (*jsonbType) Size() int16  { return -1 }

func (*jsonbType) EncodeText(v any) ([]byte, error) {
	b, err := jsonbBytes(v)
	if err != nil {
		return nil, fmt.Errorf("jsonb EncodeText: %w", err)
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out, nil
}

func (*jsonbType) EncodeBinary(v any) ([]byte, error) {
	b, err := jsonbBytes(v)
	if err != nil {
		return nil, fmt.Errorf("jsonb EncodeBinary: %w", err)
	}
	out := make([]byte, len(b)+1)
	out[0] = 1 // jsonb binary version
	copy(out[1:], b)
	return out, nil
}

func (*jsonbType) DecodeText(b []byte) (any, error) {
	out := make([]byte, len(b))
	copy(out, b)
	return out, nil
}

func (*jsonbType) DecodeBinary(b []byte) (any, error) {
	if len(b) < 1 || b[0] != 1 {
		return nil, fmt.Errorf("jsonb DecodeBinary: unsupported version byte (got %x)", b)
	}
	out := make([]byte, len(b)-1)
	copy(out, b[1:])
	return out, nil
}

func jsonbBytes(v any) ([]byte, error) {
	switch x := v.(type) {
	case []byte:
		return x, nil
	case string:
		return []byte(x), nil
	default:
		return nil, fmt.Errorf("unsupported %T", v)
	}
}

// UUID is PG uuid (16 raw bytes). Internally we hold values as
// [16]byte so they're comparable (UNIQUE / map keys).
var UUID Type = &uuidType{}

type uuidType struct{}

func (*uuidType) Name() string { return "uuid" }
func (*uuidType) OID() uint32  { return 2950 }
func (*uuidType) Size() int16  { return 16 }

func (*uuidType) EncodeText(v any) ([]byte, error) {
	b, err := uuidBytes(v)
	if err != nil {
		return nil, fmt.Errorf("uuid EncodeText: %w", err)
	}
	return []byte(uuidFormat(b)), nil
}

func (*uuidType) EncodeBinary(v any) ([]byte, error) {
	b, err := uuidBytes(v)
	if err != nil {
		return nil, fmt.Errorf("uuid EncodeBinary: %w", err)
	}
	out := make([]byte, 16)
	copy(out, b[:])
	return out, nil
}

func (*uuidType) DecodeText(b []byte) (any, error) {
	parsed, err := uuidParse(string(b))
	if err != nil {
		return nil, fmt.Errorf("uuid DecodeText: %w", err)
	}
	return parsed, nil
}

func (*uuidType) DecodeBinary(b []byte) (any, error) {
	if len(b) != 16 {
		return nil, fmt.Errorf("uuid DecodeBinary: want 16 bytes, got %d", len(b))
	}
	var out [16]byte
	copy(out[:], b)
	return out, nil
}

// uuidBytes normalizes accepted Go representations of a UUID into a
// [16]byte. We intentionally accept both the array (the canonical
// internal form) and a string (so test code can write literals
// without importing a uuid package).
func uuidBytes(v any) ([16]byte, error) {
	switch x := v.(type) {
	case [16]byte:
		return x, nil
	case string:
		return uuidParse(x)
	case []byte:
		if len(x) != 16 {
			return [16]byte{}, fmt.Errorf("[]byte len %d, want 16", len(x))
		}
		var out [16]byte
		copy(out[:], x)
		return out, nil
	default:
		return [16]byte{}, fmt.Errorf("unsupported %T", v)
	}
}

// uuidFormat emits the canonical 8-4-4-4-12 hyphenated form.
func uuidFormat(b [16]byte) string {
	digits := hex.EncodeToString(b[:])
	return digits[0:8] + "-" + digits[8:12] + "-" + digits[12:16] + "-" + digits[16:20] + "-" + digits[20:32]
}

// uuidParse accepts the canonical hyphenated form and the bare 32-hex
// form (PG accepts both on input).
func uuidParse(s string) ([16]byte, error) {
	var out [16]byte
	stripped := make([]byte, 0, 32)
	for i := 0; i < len(s); i++ {
		if s[i] == '-' {
			continue
		}
		stripped = append(stripped, s[i])
	}
	if len(stripped) != 32 {
		return out, fmt.Errorf("expected 32 hex chars, got %d", len(stripped))
	}
	if _, err := hex.Decode(out[:], stripped); err != nil {
		return out, fmt.Errorf("non-hex character in uuid: %w", err)
	}
	return out, nil
}

// Timestamptz is PG `timestamp with time zone` (OID 1184). PG's binary
// format is microseconds since the 2000-01-01 UTC epoch as a big-endian
// signed int64. The text format we emit is the canonical
// "YYYY-MM-DD HH:MM:SS.ffffff±HH" PG produces. Internally we hold the
// value as time.Time so Go callers can use it directly.
var Timestamptz Type = &timestamptzType{}

type timestamptzType struct{}

// pgEpoch is 2000-01-01 00:00:00 UTC. PG's binary timestamp is the
// microsecond offset from this point.
var pgEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

func (*timestamptzType) Name() string { return "timestamptz" }
func (*timestamptzType) OID() uint32  { return 1184 }
func (*timestamptzType) Size() int16  { return 8 }

func (*timestamptzType) EncodeText(v any) ([]byte, error) {
	t, err := asTime(v)
	if err != nil {
		return nil, fmt.Errorf("timestamptz EncodeText: %w", err)
	}
	return []byte(t.UTC().Format("2006-01-02 15:04:05.999999-07")), nil
}

func (*timestamptzType) EncodeBinary(v any) ([]byte, error) {
	t, err := asTime(v)
	if err != nil {
		return nil, fmt.Errorf("timestamptz EncodeBinary: %w", err)
	}
	micros := t.UTC().Sub(pgEpoch).Microseconds()
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(micros))
	return b, nil
}

// timestampLayouts is the (small) set of input formats we accept. PG
// accepts more (extended ISO etc.); these cover what pgx normally
// produces for text-format binds.
var timestampLayouts = []string{
	"2006-01-02 15:04:05.999999-07",
	"2006-01-02 15:04:05.999999Z07:00",
	"2006-01-02 15:04:05.999999",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05",
	time.RFC3339Nano,
	time.RFC3339,
}

func (*timestamptzType) DecodeText(b []byte) (any, error) {
	s := string(b)
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return nil, fmt.Errorf("timestamptz DecodeText: unrecognized format %q", s)
}

func (*timestamptzType) DecodeBinary(b []byte) (any, error) {
	if len(b) != 8 {
		return nil, fmt.Errorf("timestamptz DecodeBinary: want 8 bytes, got %d", len(b))
	}
	micros := int64(binary.BigEndian.Uint64(b))
	return pgEpoch.Add(time.Duration(micros) * time.Microsecond).UTC(), nil
}

// asTime normalizes the accepted Go forms (time.Time or string) into a
// time.Time in UTC.
func asTime(v any) (time.Time, error) {
	switch x := v.(type) {
	case time.Time:
		return x, nil
	case string:
		for _, layout := range timestampLayouts {
			if t, err := time.Parse(layout, x); err == nil {
				return t, nil
			}
		}
		return time.Time{}, fmt.Errorf("unrecognized time string %q", x)
	default:
		return time.Time{}, fmt.Errorf("unsupported %T", v)
	}
}

// ByOID looks up one of the supported PG types by OID. The dialect
// Registry supersedes this once it lands; until then this is the only
// lookup path the wire layer needs.
func ByOID(oid uint32) (Type, bool) {
	switch oid {
	case 16:
		return Bool, true
	case 17:
		return Bytea, true
	case 20:
		return Int8, true
	case 23:
		return Int4, true
	case 25:
		return Text, true
	case 1184:
		return Timestamptz, true
	case 2950:
		return UUID, true
	case 3802:
		return JSONB, true
	case 700:
		return Float4, true
	case 701:
		return Float8, true
	case 1007:
		return Int4Array, true
	case 1009:
		return TextArray, true
	case 1016:
		return Int8Array, true
	default:
		return nil, false
	}
}

// Float8 is PG `double precision` (OID 701, 8 bytes IEEE-754 BE).
var Float8 Type = &float8Type{}

type float8Type struct{}

func (*float8Type) Name() string { return "float8" }
func (*float8Type) OID() uint32  { return 701 }
func (*float8Type) Size() int16  { return 8 }

func (*float8Type) EncodeText(v any) ([]byte, error) {
	f, ok := asFloat64(v)
	if !ok {
		return nil, fmt.Errorf("float8 EncodeText: unsupported %T", v)
	}
	return strconv.AppendFloat(nil, f, 'g', -1, 64), nil
}

func (*float8Type) EncodeBinary(v any) ([]byte, error) {
	f, ok := asFloat64(v)
	if !ok {
		return nil, fmt.Errorf("float8 EncodeBinary: unsupported %T", v)
	}
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, math.Float64bits(f))
	return b, nil
}

func (*float8Type) DecodeText(b []byte) (any, error) {
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return nil, fmt.Errorf("float8 DecodeText: %w", err)
	}
	return f, nil
}

func (*float8Type) DecodeBinary(b []byte) (any, error) {
	if len(b) != 8 {
		return nil, fmt.Errorf("float8 DecodeBinary: want 8 bytes, got %d", len(b))
	}
	return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
}

// Float4 is PG `real` (OID 700, 4 bytes IEEE-754 BE).
var Float4 Type = &float4Type{}

type float4Type struct{}

func (*float4Type) Name() string { return "float4" }
func (*float4Type) OID() uint32  { return 700 }
func (*float4Type) Size() int16  { return 4 }

func (*float4Type) EncodeText(v any) ([]byte, error) {
	f, ok := asFloat64(v)
	if !ok {
		return nil, fmt.Errorf("float4 EncodeText: unsupported %T", v)
	}
	return strconv.AppendFloat(nil, f, 'g', -1, 32), nil
}

func (*float4Type) EncodeBinary(v any) ([]byte, error) {
	f, ok := asFloat64(v)
	if !ok {
		return nil, fmt.Errorf("float4 EncodeBinary: unsupported %T", v)
	}
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, math.Float32bits(float32(f)))
	return b, nil
}

func (*float4Type) DecodeText(b []byte) (any, error) {
	f, err := strconv.ParseFloat(string(b), 32)
	if err != nil {
		return nil, fmt.Errorf("float4 DecodeText: %w", err)
	}
	return float32(f), nil
}

func (*float4Type) DecodeBinary(b []byte) (any, error) {
	if len(b) != 4 {
		return nil, fmt.Errorf("float4 DecodeBinary: want 4 bytes, got %d", len(b))
	}
	return math.Float32frombits(binary.BigEndian.Uint32(b)), nil
}

// asFloat64 normalises numeric inputs into a single channel for the
// float encoders. Decoders return the canonical Go type (float32 or
// float64).
func asFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int64:
		return float64(x), true
	case int32:
		return float64(x), true
	case int:
		return float64(x), true
	default:
		return 0, false
	}
}

// Int8Array is PG bigint[] (OID 1016). Element is int8 (OID 20).
// Internally we hold the value as []int64.
var Int8Array Type = &arrayType{
	name:    "int8[]",
	oid:     1016,
	elemOID: 20,
	encodeText: func(v any) (string, error) {
		arr, ok := v.([]int64)
		if !ok {
			return "", fmt.Errorf("unsupported %T", v)
		}
		var b []byte
		b = append(b, '{')
		for i, n := range arr {
			if i > 0 {
				b = append(b, ',')
			}
			b = strconv.AppendInt(b, n, 10)
		}
		b = append(b, '}')
		return string(b), nil
	},
	decodeTextElem: func(s string) (any, error) {
		n, err := strconv.ParseInt(s, 10, 64)
		return n, err
	},
	encodeBinElem: func(v any) ([]byte, error) {
		n, ok := asInt64(v)
		if !ok {
			return nil, fmt.Errorf("int8 array element: unsupported %T", v)
		}
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, uint64(n))
		return b, nil
	},
	decodeBinElem: func(b []byte) (any, error) {
		if len(b) != 8 {
			return nil, fmt.Errorf("int8 array element: want 8 bytes, got %d", len(b))
		}
		return int64(binary.BigEndian.Uint64(b)), nil
	},
	zero: func() any { return []int64(nil) },
	appendElem: func(arr any, e any) (any, error) {
		s, _ := arr.([]int64)
		n, err := toInt64Loose(e)
		if err != nil {
			return nil, err
		}
		return append(s, n), nil
	},
}

// Int4Array is PG integer[] (OID 1007). Element is int4 (OID 23).
// Internally we hold the value as []int32.
var Int4Array Type = &arrayType{
	name:    "int4[]",
	oid:     1007,
	elemOID: 23,
	encodeText: func(v any) (string, error) {
		arr, ok := v.([]int32)
		if !ok {
			return "", fmt.Errorf("unsupported %T", v)
		}
		var b []byte
		b = append(b, '{')
		for i, n := range arr {
			if i > 0 {
				b = append(b, ',')
			}
			b = strconv.AppendInt(b, int64(n), 10)
		}
		b = append(b, '}')
		return string(b), nil
	},
	decodeTextElem: func(s string) (any, error) {
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return nil, err
		}
		return int32(n), nil
	},
	encodeBinElem: func(v any) ([]byte, error) {
		n, ok := asInt64(v)
		if !ok {
			return nil, fmt.Errorf("int4 array element: unsupported %T", v)
		}
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(int32(n)))
		return b, nil
	},
	decodeBinElem: func(b []byte) (any, error) {
		if len(b) != 4 {
			return nil, fmt.Errorf("int4 array element: want 4 bytes, got %d", len(b))
		}
		return int32(binary.BigEndian.Uint32(b)), nil
	},
	zero: func() any { return []int32(nil) },
	appendElem: func(arr any, e any) (any, error) {
		s, _ := arr.([]int32)
		n, err := toInt64Loose(e)
		if err != nil {
			return nil, err
		}
		return append(s, int32(n)), nil
	},
}

// TextArray is PG text[] (OID 1009). Element is text (OID 25).
// Internally we hold the value as []string.
var TextArray Type = &arrayType{
	name:    "text[]",
	oid:     1009,
	elemOID: 25,
	encodeText: func(v any) (string, error) {
		arr, ok := v.([]string)
		if !ok {
			return "", fmt.Errorf("unsupported %T", v)
		}
		var b []byte
		b = append(b, '{')
		for i, s := range arr {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendArrayQuoted(b, s)
		}
		b = append(b, '}')
		return string(b), nil
	},
	decodeTextElem: func(s string) (any, error) {
		return s, nil
	},
	encodeBinElem: func(v any) ([]byte, error) {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("text array element: unsupported %T", v)
		}
		return []byte(s), nil
	},
	decodeBinElem: func(b []byte) (any, error) {
		out := make([]byte, len(b))
		copy(out, b)
		return string(out), nil
	},
	zero: func() any { return []string(nil) },
	appendElem: func(arr any, e any) (any, error) {
		s, _ := arr.([]string)
		v, ok := e.(string)
		if !ok {
			return nil, fmt.Errorf("text array element: %T", e)
		}
		return append(s, v), nil
	},
}

// arrayType is a small helper that supplies the shared text + binary
// codec scaffolding for PG one-dimensional array types. Per-element
// encode / decode and the slice-typed accumulator are passed in.
type arrayType struct {
	name           string
	oid            uint32
	elemOID        uint32
	encodeText     func(v any) (string, error)
	decodeTextElem func(s string) (any, error)
	encodeBinElem  func(v any) ([]byte, error)
	decodeBinElem  func(b []byte) (any, error)
	zero           func() any
	appendElem     func(arr any, e any) (any, error)
}

func (a *arrayType) Name() string { return a.name }
func (a *arrayType) OID() uint32  { return a.oid }
func (a *arrayType) Size() int16  { return -1 }

func (a *arrayType) EncodeText(v any) ([]byte, error) {
	s, err := a.encodeText(v)
	if err != nil {
		return nil, fmt.Errorf("%s EncodeText: %w", a.name, err)
	}
	return []byte(s), nil
}

func (a *arrayType) DecodeText(b []byte) (any, error) {
	s := string(b)
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil, fmt.Errorf("%s DecodeText: bad shape %q", a.name, s)
	}
	body := s[1 : len(s)-1]
	out := a.zero()
	if body == "" {
		return out, nil
	}
	for _, raw := range splitArrayElems(body) {
		v, err := a.decodeTextElem(raw)
		if err != nil {
			return nil, fmt.Errorf("%s DecodeText: %w", a.name, err)
		}
		out, err = a.appendElem(out, v)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (a *arrayType) EncodeBinary(v any) ([]byte, error) {
	// PG one-dim array binary header:
	//   int32 ndim
	//   int32 hasnull (always 0 for the slices we accept)
	//   int32 elemoid
	//   per dim: int32 length, int32 lower-bound (1)
	//   per element: int32 size, then size bytes (size = -1 for NULL)
	var elems [][]byte
	switch arr := v.(type) {
	case []int64:
		for _, n := range arr {
			b, err := a.encodeBinElem(n)
			if err != nil {
				return nil, fmt.Errorf("%s EncodeBinary: %w", a.name, err)
			}
			elems = append(elems, b)
		}
	case []int32:
		for _, n := range arr {
			b, err := a.encodeBinElem(n)
			if err != nil {
				return nil, fmt.Errorf("%s EncodeBinary: %w", a.name, err)
			}
			elems = append(elems, b)
		}
	case []string:
		for _, s := range arr {
			b, err := a.encodeBinElem(s)
			if err != nil {
				return nil, fmt.Errorf("%s EncodeBinary: %w", a.name, err)
			}
			elems = append(elems, b)
		}
	default:
		return nil, fmt.Errorf("%s EncodeBinary: unsupported %T", a.name, v)
	}
	out := make([]byte, 0, 20+len(elems)*8)
	out = appendInt32(out, 1) // ndim
	out = appendInt32(out, 0) // hasnull
	out = appendInt32(out, int32(a.elemOID))
	out = appendInt32(out, int32(len(elems)))
	out = appendInt32(out, 1) // lower bound
	for _, e := range elems {
		out = appendInt32(out, int32(len(e)))
		out = append(out, e...)
	}
	return out, nil
}

func (a *arrayType) DecodeBinary(b []byte) (any, error) {
	if len(b) < 20 {
		return nil, fmt.Errorf("%s DecodeBinary: want >= 20 bytes, got %d", a.name, len(b))
	}
	ndim := int32(binary.BigEndian.Uint32(b[0:4]))
	if ndim != 1 {
		return nil, fmt.Errorf("%s DecodeBinary: only 1-D arrays supported (got ndim=%d)", a.name, ndim)
	}
	dimLen := int32(binary.BigEndian.Uint32(b[12:16]))
	pos := 20
	out := a.zero()
	for i := int32(0); i < dimLen; i++ {
		if pos+4 > len(b) {
			return nil, fmt.Errorf("%s DecodeBinary: truncated", a.name)
		}
		sz := int32(binary.BigEndian.Uint32(b[pos : pos+4]))
		pos += 4
		if sz < 0 {
			return nil, fmt.Errorf("%s DecodeBinary: NULL elements not supported yet", a.name)
		}
		if pos+int(sz) > len(b) {
			return nil, fmt.Errorf("%s DecodeBinary: truncated", a.name)
		}
		v, err := a.decodeBinElem(b[pos : pos+int(sz)])
		if err != nil {
			return nil, err
		}
		pos += int(sz)
		out, err = a.appendElem(out, v)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func appendInt32(b []byte, n int32) []byte {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(n))
	return append(b, buf[:]...)
}

// splitArrayElems splits the PG array-text body on commas while
// respecting a single level of double-quoting. We don't support
// embedded backslash escapes beyond `\\` and `\"`, which is enough
// for the strings sqlc-generated tests roundtrip.
func splitArrayElems(body string) []string {
	var out []string
	var buf []byte
	inQuote := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '"' && !inQuote:
			inQuote = true
		case c == '"' && inQuote:
			inQuote = false
		case c == '\\' && inQuote && i+1 < len(body):
			buf = append(buf, body[i+1])
			i++
		case c == ',' && !inQuote:
			out = append(out, string(buf))
			buf = buf[:0]
		default:
			buf = append(buf, c)
		}
	}
	out = append(out, string(buf))
	return out
}

// appendArrayQuoted appends s as an array element, wrapping in
// double quotes when the value would otherwise be ambiguous (commas,
// braces, whitespace, quotes, or empty).
func appendArrayQuoted(b []byte, s string) []byte {
	if needsArrayQuote(s) {
		b = append(b, '"')
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c == '"' || c == '\\' {
				b = append(b, '\\')
			}
			b = append(b, c)
		}
		b = append(b, '"')
		return b
	}
	return append(b, s...)
}

func needsArrayQuote(s string) bool {
	if s == "" {
		return true
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ',' || c == '{' || c == '}' || c == '"' || c == '\\' || c == ' ' || c == '\t' {
			return true
		}
	}
	return false
}

// toInt64Loose accepts the integer-like inputs we'd see from a wire
// decode (int / int32 / int64) and reports a parse error otherwise.
func toInt64Loose(v any) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case int32:
		return int64(n), nil
	case int:
		return int64(n), nil
	}
	return 0, fmt.Errorf("not an integer: %T", v)
}

// Interval is a stripped-down PG `interval` type. We model it as a
// time.Duration so timestamp ± interval falls out of standard Go
// arithmetic. The wire codec is a stub: we don't accept interval as a
// bound parameter or emit it in column metadata yet — intervals
// typically appear inline as literals (`interval '1 day'`).
var Interval Type = &intervalType{}

type intervalType struct{}

func (*intervalType) Name() string { return "interval" }
func (*intervalType) OID() uint32  { return 1186 }
func (*intervalType) Size() int16  { return 16 }
func (*intervalType) EncodeText(_ any) ([]byte, error) {
	return nil, fmt.Errorf("interval EncodeText: not supported")
}
func (*intervalType) EncodeBinary(_ any) ([]byte, error) {
	return nil, fmt.Errorf("interval EncodeBinary: not supported")
}
func (*intervalType) DecodeText(_ []byte) (any, error) {
	return nil, fmt.Errorf("interval DecodeText: not supported")
}
func (*intervalType) DecodeBinary(_ []byte) (any, error) {
	return nil, fmt.Errorf("interval DecodeBinary: not supported")
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
	case "bytea":
		return Bytea, true
	case "uuid":
		return UUID, true
	case "timestamptz":
		return Timestamptz, true
	case "jsonb":
		return JSONB, true
	case "float8", "double precision":
		return Float8, true
	case "float4", "real":
		return Float4, true
	case "int[]", "integer[]", "int4[]":
		return Int4Array, true
	case "bigint[]", "int8[]":
		return Int8Array, true
	case "text[]", "varchar[]":
		return TextArray, true
	default:
		return nil, false
	}
}
