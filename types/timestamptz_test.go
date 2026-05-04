package types_test

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/kaeawc/pgmem-go/types"
)

func TestTimestamptz_BinaryRoundTrip(t *testing.T) {
	want := time.Date(2025, 7, 4, 12, 30, 45, 123456000, time.UTC)
	b, err := types.Timestamptz.EncodeBinary(want)
	if err != nil {
		t.Fatalf("EncodeBinary: %v", err)
	}
	if len(b) != 8 {
		t.Fatalf("EncodeBinary len: got %d, want 8", len(b))
	}
	v, err := types.Timestamptz.DecodeBinary(b)
	if err != nil {
		t.Fatalf("DecodeBinary: %v", err)
	}
	got := v.(time.Time)
	if !got.Equal(want) {
		t.Errorf("round trip: got %v, want %v", got, want)
	}
}

// TestTimestamptz_BinaryEpoch confirms our microsecond offset is taken
// from the 2000-01-01 UTC PG epoch — that exact moment encodes to 0.
func TestTimestamptz_BinaryEpoch(t *testing.T) {
	epoch := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	b, err := types.Timestamptz.EncodeBinary(epoch)
	if err != nil {
		t.Fatalf("EncodeBinary: %v", err)
	}
	if !bytes.Equal(b, []byte{0, 0, 0, 0, 0, 0, 0, 0}) {
		t.Errorf("epoch: got %v, want all zeros", b)
	}

	one := []byte{0, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint64(one, uint64(time.Hour.Microseconds()))
	v, _ := types.Timestamptz.DecodeBinary(one)
	got := v.(time.Time)
	want := epoch.Add(time.Hour)
	if !got.Equal(want) {
		t.Errorf("epoch+1h: got %v, want %v", got, want)
	}
}

func TestTimestamptz_TextRoundTrip(t *testing.T) {
	want := time.Date(2025, 7, 4, 12, 30, 45, 0, time.UTC)
	b, err := types.Timestamptz.EncodeText(want)
	if err != nil {
		t.Fatalf("EncodeText: %v", err)
	}
	v, err := types.Timestamptz.DecodeText(b)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	got := v.(time.Time)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v (text was %q)", got, want, b)
	}
}

func TestTimestamptz_LookupByOIDAndName(t *testing.T) {
	if got, ok := types.ByOID(1184); !ok || got.Name() != "timestamptz" {
		t.Errorf("ByOID(1184): got %v ok=%v, want timestamptz", got, ok)
	}
	if got, ok := types.ByName("timestamptz"); !ok || got.Name() != "timestamptz" {
		t.Errorf("ByName(timestamptz): got %v ok=%v, want timestamptz", got, ok)
	}
}
