package textutil

import "testing"

func TestIsLikelyBinaryAcceptsASCIIText(t *testing.T) {
	if IsLikelyBinary([]byte("hello\nworld\n")) {
		t.Fatalf("expected ASCII text to be accepted")
	}
}

func TestIsLikelyBinaryAcceptsUTF8Text(t *testing.T) {
	if IsLikelyBinary([]byte("Grüße 世界\n")) {
		t.Fatalf("expected UTF-8 text to be accepted")
	}
}

func TestIsLikelyBinaryRejectsNULData(t *testing.T) {
	if !IsLikelyBinary([]byte{'a', 0, 'b'}) {
		t.Fatalf("expected NUL-containing data to be binary")
	}
}

func TestIsLikelyBinaryRejectsInvalidControlHeavyData(t *testing.T) {
	sample := []byte{0x01, 0x02, 0x03, 0xff, 0xfe, 0x10, 0x11, 0x12}
	if !IsLikelyBinary(sample) {
		t.Fatalf("expected control-heavy invalid data to be binary")
	}
}
