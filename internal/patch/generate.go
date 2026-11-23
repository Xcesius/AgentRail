package patchmod

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"agentrail/internal/filemeta"
	"agentrail/internal/protocol"
	"agentrail/internal/textutil"
	"agentrail/internal/workspace"
)

type GeneratedFilePatch struct {
	Path      string `json:"path"`
	FileToken string `json:"file_token,omitempty"`
	Changed   bool   `json:"changed"`
	Diff      string `json:"diff"`
}

func BuildFilePatch(manager *workspace.Manager, target, desiredContent, expectedFileToken string) (GeneratedFilePatch, error) {
	resolved, err := manager.ResolveWritePath(target)
	if err != nil {
		return GeneratedFilePatch{}, err
	}

	displayPath := manager.DisplayPath(resolved)
	originalBytes, exists, err := readTextTarget(resolved, displayPath)
	if err != nil {
		return GeneratedFilePatch{Path: displayPath}, err
	}

	fileToken := ""
	if exists {
		fileToken = filemeta.TokenFromBytes(originalBytes)
	}
	if expectedFileToken != "" && expectedFileToken != fileToken {
		return GeneratedFilePatch{Path: displayPath, FileToken: fileToken}, protocol.ErrDetails(
			protocol.CodeTokenMismatch,
			"file token mismatch",
			protocol.ErrorDetails{
				"path":                displayPath,
				"expected_file_token": expectedFileToken,
				"actual_file_token":   fileToken,
			},
		)
	}

	originalText := normalizePatchContent(string(originalBytes))
	desiredText := normalizePatchContent(desiredContent)
	changed := !exists || string(originalBytes) != desiredContent

	diff := ""
	if changed {
		diff = buildUnifiedDiff(displayPath, originalText, exists, desiredText)
	}

	return GeneratedFilePatch{
		Path:      displayPath,
		FileToken: fileToken,
		Changed:   changed,
		Diff:      diff,
	}, nil
}

func readTextTarget(path, displayPath string) ([]byte, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, protocol.Err(protocol.CodeInvalidRequest, "unable to inspect target file")
	}
	if info.IsDir() {
		return nil, false, protocol.ErrDetails(
			protocol.CodeInvalidRequest,
			"path is a directory",
			protocol.ErrorDetails{"field": "path", "reason": "directory"},
		)
	}
	if !info.Mode().IsRegular() {
		return nil, false, protocol.ErrDetails(
			protocol.CodeInvalidRequest,
			"path is not a regular file",
			protocol.ErrorDetails{"field": "path", "reason": "unsupported_file_type"},
		)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, protocol.Err(protocol.CodeInvalidRequest, "unable to read target file")
	}

	sample := data
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	if textutil.IsLikelyBinary(sample) {
		return nil, false, protocol.ErrDetails(
			protocol.CodeBinaryFile,
			"binary file cannot be replaced",
			protocol.ErrorDetails{"path": displayPath},
		)
	}
	return data, true, nil
}

func buildUnifiedDiff(displayPath, original string, originalExists bool, updated string) string {
	var out strings.Builder
	if originalExists {
		fmt.Fprintf(&out, "--- a/%s\n", displayPath)
	} else {
		out.WriteString("--- /dev/null\n")
	}
	fmt.Fprintf(&out, "+++ b/%s\n", displayPath)

	originalLines, originalTrailingNewline := diffContentState(original)
	updatedLines, updatedTrailingNewline := diffContentState(updated)
	if !originalExists && len(updatedLines) == 0 {
		return out.String()
	}

	// Trim unchanged common prefix and suffix lines so single-line edits on large
	// files produce compact hunks with standard 3-line context instead of full-file rewrites.
	prefix := 0
	for prefix < len(originalLines) && prefix < len(updatedLines) && originalLines[prefix] == updatedLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(originalLines)-prefix && suffix < len(updatedLines)-prefix &&
		originalLines[len(originalLines)-1-suffix] == updatedLines[len(updatedLines)-1-suffix] {
		suffix++
	}

	// If normalized lines are identical (trailing-newline or CRLF/LF-only change),
	// keep the final line in the changed range so the hunk reflects the mutation.
	if prefix == len(originalLines) && prefix == len(updatedLines) && len(originalLines) > 0 {
		prefix = len(originalLines) - 1
		suffix = 0
	}

	const contextLines = 3
	ctxStart := prefix - contextLines
	if ctxStart < 0 {
		ctxStart = 0
	}
	oldChangeEnd := len(originalLines) - suffix
	newChangeEnd := len(updatedLines) - suffix
	oldEnd := oldChangeEnd + contextLines
	if oldEnd > len(originalLines) {
		oldEnd = len(originalLines)
	}
	newEnd := newChangeEnd + contextLines
	if newEnd > len(updatedLines) {
		newEnd = len(updatedLines)
	}

	// If the trailing newline changed and the suffix was trimmed past EOF, include EOF.
	if originalTrailingNewline != updatedTrailingNewline && (oldEnd < len(originalLines) || newEnd < len(updatedLines)) {
		oldEnd = len(originalLines)
		newEnd = len(updatedLines)
		oldChangeEnd = len(originalLines)
		newChangeEnd = len(updatedLines)
	}

	oldCount := oldEnd - ctxStart
	newCount := newEnd - ctxStart
	oldStart := diffRangeStartAt(ctxStart, oldCount)
	newStart := diffRangeStartAt(ctxStart, newCount)

	fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)

	// Leading context
	for i := ctxStart; i < prefix; i++ {
		fmt.Fprintf(&out, " %s\n", originalLines[i])
	}
	// Removed lines
	for i := prefix; i < oldChangeEnd; i++ {
		fmt.Fprintf(&out, "-%s\n", originalLines[i])
		if i == len(originalLines)-1 && !originalTrailingNewline {
			out.WriteString("\\ No newline at end of file\n")
		}
	}
	// Added lines
	for i := prefix; i < newChangeEnd; i++ {
		fmt.Fprintf(&out, "+%s\n", updatedLines[i])
		if i == len(updatedLines)-1 && !updatedTrailingNewline {
			out.WriteString("\\ No newline at end of file\n")
		}
	}
	// Trailing context
	for i := oldChangeEnd; i < oldEnd; i++ {
		fmt.Fprintf(&out, " %s\n", originalLines[i])
		if i == len(originalLines)-1 && !originalTrailingNewline && !updatedTrailingNewline {
			out.WriteString("\\ No newline at end of file\n")
		}
	}
	return out.String()
}

func diffContentState(content string) ([]string, bool) {
	normalized := normalizePatchContent(content)
	return splitLines(normalized), strings.HasSuffix(normalized, "\n")
}

func normalizePatchContent(content string) string {
	return strings.ReplaceAll(content, "\r\n", "\n")
}

func diffRangeStartAt(zeroBasedStart, lineCount int) int {
	if lineCount == 0 {
		return zeroBasedStart
	}
	return zeroBasedStart + 1
}
