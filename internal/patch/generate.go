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
	changed := !exists || originalText != desiredText

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

	fmt.Fprintf(
		&out,
		"@@ -%d,%d +%d,%d @@\n",
		diffRangeStart(len(originalLines)),
		len(originalLines),
		diffRangeStart(len(updatedLines)),
		len(updatedLines),
	)
	for i, line := range originalLines {
		fmt.Fprintf(&out, "-%s\n", line)
		if i == len(originalLines)-1 && !originalTrailingNewline {
			out.WriteString("\\ No newline at end of file\n")
		}
	}
	for i, line := range updatedLines {
		fmt.Fprintf(&out, "+%s\n", line)
		if i == len(updatedLines)-1 && !updatedTrailingNewline {
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

func diffRangeStart(lineCount int) int {
	if lineCount == 0 {
		return 0
	}
	return 1
}
