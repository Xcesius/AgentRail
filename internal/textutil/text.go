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

	if utf8.Valid(sample) {
		controls := 0
		runes := 0
		for len(sample) > 0 {
			r, size := utf8.DecodeRune(sample)
			sample = sample[size:]
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
