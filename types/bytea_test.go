package types_test

import (
	"bytes"
	"testing"

	"github.com/kaeawc/pgmem-go/types"
)

var byteaSample = []byte{0x00, 0x01, 0xfe, 0xff, 'h', 'i'}

func TestBytea_TextRoundTrip(t *testing.T) {
	enc, err := types.Bytea.EncodeText(byteaSample)
	if err != nil {
		t.Fatalf("EncodeText: %v", err)
	}
	if string(enc) != `\x0001feff6869` {
		t.Errorf("EncodeText: got %q, want %q", enc, `\x0001feff6869`)
	}
	v, err := types.Bytea.DecodeText(enc)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	if !bytes.Equal(v.([]byte), byteaSample) {
		t.Errorf("DecodeText: got %v, want %v", v, byteaSample)
	}
}

func TestBytea_BinaryRoundTrip(t *testing.T) {
	enc, err := types.Bytea.EncodeBinary(byteaSample)
	if err != nil {
		t.Fatalf("EncodeBinary: %v", err)
	}
	if !bytes.Equal(enc, byteaSample) {
		t.Errorf("EncodeBinary: got %v, want %v", enc, byteaSample)
	}
	v, err := types.Bytea.DecodeBinary(enc)
	if err != nil {
		t.Fatalf("DecodeBinary: %v", err)
	}
	if !bytes.Equal(v.([]byte), byteaSample) {
		t.Errorf("DecodeBinary: got %v, want %v", v, byteaSample)
	}
}

// TestBytea_RejectsLegacyEscapeFormat: the older `'foo\\000'` escape
// form isn't on the menu here. Only \x... is supported on input.
func TestBytea_RejectsLegacyEscapeFormat(t *testing.T) {
	if _, err := types.Bytea.DecodeText([]byte(`abc`)); err == nil {
		t.Error("plain text input: want error, got nil")
	}
}

func TestBytea_LookupByOIDAndName(t *testing.T) {
	if got, ok := types.ByOID(17); !ok || got.Name() != "bytea" {
		t.Errorf("ByOID(17): got %v ok=%v", got, ok)
	}
	if got, ok := types.ByName("bytea"); !ok || got.Name() != "bytea" {
		t.Errorf("ByName(bytea): got %v ok=%v", got, ok)
	}
}
