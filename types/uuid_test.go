package types_test

import (
	"bytes"
	"testing"

	"github.com/kaeawc/pgmem-go/types"
)

const (
	canon  = "01020304-0506-0708-090a-0b0c0d0e0f10"
	noDash = "0102030405060708090a0b0c0d0e0f10"
)

var raw = [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

func TestUUID_TextEncodeDecodeRoundTrip(t *testing.T) {
	b, err := types.UUID.EncodeText(raw)
	if err != nil {
		t.Fatalf("EncodeText: %v", err)
	}
	if string(b) != canon {
		t.Errorf("EncodeText: got %q, want %q", b, canon)
	}
	v, err := types.UUID.DecodeText([]byte(canon))
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	got, ok := v.([16]byte)
	if !ok || got != raw {
		t.Errorf("DecodeText: got %v (%T), want %v", v, v, raw)
	}
}

func TestUUID_BinaryRoundTrip(t *testing.T) {
	b, err := types.UUID.EncodeBinary(raw)
	if err != nil {
		t.Fatalf("EncodeBinary: %v", err)
	}
	if !bytes.Equal(b, raw[:]) {
		t.Errorf("EncodeBinary: got %v, want %v", b, raw[:])
	}
	v, err := types.UUID.DecodeBinary(b)
	if err != nil {
		t.Fatalf("DecodeBinary: %v", err)
	}
	if got := v.([16]byte); got != raw {
		t.Errorf("DecodeBinary: got %v, want %v", got, raw)
	}
}

// TestUUID_AcceptsBareHexInput: PG accepts both the hyphenated form
// and the bare 32-hex form on input. We do too.
func TestUUID_AcceptsBareHexInput(t *testing.T) {
	v, err := types.UUID.DecodeText([]byte(noDash))
	if err != nil {
		t.Fatalf("DecodeText bare: %v", err)
	}
	if got := v.([16]byte); got != raw {
		t.Errorf("DecodeText bare: got %v, want %v", got, raw)
	}
}

// TestUUID_RejectsBadInput covers the not-uuid-shaped inputs we'd
// otherwise silently truncate.
func TestUUID_RejectsBadInput(t *testing.T) {
	cases := []string{
		"",
		"too-short",
		"01020304-0506-0708-090a-0b0c0d0e0f1g", // 'g' not hex
	}
	for _, in := range cases {
		if _, err := types.UUID.DecodeText([]byte(in)); err == nil {
			t.Errorf("DecodeText(%q): want error, got nil", in)
		}
	}
}

// TestUUID_LookupByOIDAndName confirms the registry hooks are wired.
func TestUUID_LookupByOIDAndName(t *testing.T) {
	if got, ok := types.ByOID(2950); !ok || got.Name() != "uuid" {
		t.Errorf("ByOID(2950): got %v ok=%v, want uuid", got, ok)
	}
	if got, ok := types.ByName("uuid"); !ok || got.Name() != "uuid" {
		t.Errorf("ByName(uuid): got %v ok=%v, want uuid", got, ok)
	}
}
