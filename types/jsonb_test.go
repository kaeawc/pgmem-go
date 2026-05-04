package types_test

import (
	"bytes"
	"testing"

	"github.com/kaeawc/pgmem-go/types"
)

const sampleJSON = `{"k":"v","n":42}`

func TestJSONB_TextRoundTrip(t *testing.T) {
	enc, err := types.JSONB.EncodeText([]byte(sampleJSON))
	if err != nil {
		t.Fatalf("EncodeText: %v", err)
	}
	if string(enc) != sampleJSON {
		t.Errorf("EncodeText: got %q, want %q", enc, sampleJSON)
	}
	v, err := types.JSONB.DecodeText(enc)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	if !bytes.Equal(v.([]byte), []byte(sampleJSON)) {
		t.Errorf("DecodeText: got %v", v)
	}
}

func TestJSONB_BinaryRoundTrip(t *testing.T) {
	enc, err := types.JSONB.EncodeBinary([]byte(sampleJSON))
	if err != nil {
		t.Fatalf("EncodeBinary: %v", err)
	}
	if len(enc) != len(sampleJSON)+1 || enc[0] != 1 {
		t.Errorf("EncodeBinary: missing version byte: %v", enc)
	}
	v, err := types.JSONB.DecodeBinary(enc)
	if err != nil {
		t.Fatalf("DecodeBinary: %v", err)
	}
	if !bytes.Equal(v.([]byte), []byte(sampleJSON)) {
		t.Errorf("DecodeBinary: got %v", v)
	}
}

// TestJSONB_RejectsBadVersionByte: pgx always sends version=1; if some
// future version arrives we should fail loudly rather than silently
// truncating.
func TestJSONB_RejectsBadVersionByte(t *testing.T) {
	if _, err := types.JSONB.DecodeBinary([]byte{0x02, 'x'}); err == nil {
		t.Error("version 2: want error, got nil")
	}
}

func TestJSONB_LookupByOIDAndName(t *testing.T) {
	if got, ok := types.ByOID(3802); !ok || got.Name() != "jsonb" {
		t.Errorf("ByOID(3802): got %v ok=%v", got, ok)
	}
	if got, ok := types.ByName("jsonb"); !ok || got.Name() != "jsonb" {
		t.Errorf("ByName(jsonb): got %v ok=%v", got, ok)
	}
}
