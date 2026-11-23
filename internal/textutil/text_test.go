package textutil

import (
	"strings"
	"testing"
)

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

func TestIsLikelyBinaryAcceptsTruncatedMultiByteUTF8Sample(t *testing.T) {
	// 2000 3-byte CJK runes sliced at 4096 bytes splits a rune at byte 4095.
	cjk := []byte(strings.Repeat("中", 2000))
	if IsLikelyBinary(cjk[:4096]) {
		t.Fatalf("expected valid UTF-8 CJK sample truncated at 4096 bytes to be accepted")
	}
	// Also test 2-byte, 3-byte (2 bytes left), and 4-byte emoji (1, 2, 3 bytes left).
	emoji := []byte(strings.Repeat("🚀", 1200))
	for _, cut := range []int{4093, 4094, 4095, 4096} {
		if IsLikelyBinary(emoji[:cut]) {
			t.Fatalf("expected valid UTF-8 emoji sample truncated at %d bytes to be accepted", cut)
		}
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
