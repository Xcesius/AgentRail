package textutil

import (
	"bytes"
	"unicode/utf8"
)

// IsLikelyBinary applies a conservative text/binary heuristic to a sample.
// It accepts UTF-8 text and printable ASCII while rejecting NUL-heavy or
// control-heavy data.
func IsLikelyBinary(sample []byte) bool {
	if len(sample) == 0 {
		return false
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return true
	}

	utf8Sample := trimIncompleteUTF8Tail(sample)
	if len(utf8Sample) > 0 && utf8.Valid(utf8Sample) {
		controls := 0
		runes := 0
		for len(utf8Sample) > 0 {
			r, size := utf8.DecodeRune(utf8Sample)
			utf8Sample = utf8Sample[size:]
			runes++

			if r == '\n' || r == '\r' || r == '\t' {
				continue
			}
			if r < 0x20 || r == 0x7f {
				controls++
			}
		}
		return runes > 0 && float64(controls)/float64(runes) > 0.10
	}

	suspicious := 0
	for _, b := range sample {
		if b == '\n' || b == '\r' || b == '\t' {
			continue
		}
		if b < 0x20 || b == 0x7f || b >= 0x80 {
			suspicious++
		}
	}
	return float64(suspicious)/float64(len(sample)) > 0.30
}

func trimIncompleteUTF8Tail(sample []byte) []byte {
	n := len(sample)
	if n == 0 {
		return sample
	}
	limit := n - 4
	if limit < 0 {
		limit = 0
	}
	for i := n - 1; i >= limit; i-- {
		if utf8.RuneStart(sample[i]) {
			if !utf8.FullRune(sample[i:]) {
				return sample[:i]
			}
			return sample
		}
	}
	return sample
}
