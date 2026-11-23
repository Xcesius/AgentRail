package filemeta

import (
	"strings"
	"testing"
)

func TestTokenFromBytesAndReaderMatch(t *testing.T) {
	input := "agentrail deterministic token\n"
	fromBytes := TokenFromBytes([]byte(input))
	fromReader, err := TokenFromReader(strings.NewReader(input))
	if err != nil {
		t.Fatalf("TokenFromReader: %v", err)
	}
	if fromBytes != fromReader {
		t.Fatalf("mismatch: bytes=%q reader=%q", fromBytes, fromReader)
	}
	if !strings.HasPrefix(fromBytes, "sha256:") || len(fromBytes) != len("sha256:")+64 {
		t.Fatalf("unexpected token format: %q", fromBytes)
	}
}
