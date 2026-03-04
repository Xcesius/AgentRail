package patchmod

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codex-tool/internal/protocol"
	"codex-tool/internal/workspace"
	writemod "codex-tool/internal/write"
)

type FileResult struct {
	Path         string `json:"path"`
	OK           bool   `json:"ok"`
	HunksApplied int    `json:"hunks_applied,omitempty"`
	Error        string `json:"error,omitempty"`
}

type ApplyResult struct {
	FilesChanged []string     `json:"files_changed"`
	HunksApplied int          `json:"hunks_applied"`
	Results      []FileResult `json:"results"`
}

func Apply(manager *workspace.Manager, diff string) (ApplyResult, error) {
	parsed, err := Parse(diff)
	if err != nil {
		return ApplyResult{}, err
	}

	result := ApplyResult{
		FilesChanged: make([]string, 0, len(parsed.Files)),
		Results:      make([]FileResult, 0, len(parsed.Files)),
	}
	anyFailure := false

	for _, filePatch := range parsed.Files {
		fileResult := applySingle(manager, filePatch)
		result.Results = append(result.Results, fileResult)
		if fileResult.OK {
			result.FilesChanged = append(result.FilesChanged, fileResult.Path)
			result.HunksApplied += fileResult.HunksApplied
		} else {
			anyFailure = true
		}
	}

	if anyFailure {
		return result, protocol.Err(protocol.CodePatchFailed, "one or more file patches failed")
	}
	return result, nil
}

func applySingle(manager *workspace.Manager, filePatch FilePatch) FileResult {
	opType := patchType(filePatch)
	target := filePatch.NewPath
	if opType == "delete" {
		target = filePatch.OldPath
	}
	resolved, err := manager.ResolveWritePath(target)
	if err != nil {
		_, message := protocol.GetCodeAndMessage(err, protocol.CodePatchFailed)
		return FileResult{Path: target, OK: false, Error: message}
	}

	displayPath := manager.RelativePath(resolved)
	original, err := os.ReadFile(resolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if opType != "create" {
				return FileResult{Path: displayPath, OK: false, Error: "target file does not exist"}
			}
			original = []byte{}
		} else {
			return FileResult{Path: displayPath, OK: false, Error: "unable to read target file"}
		}
	}

	if opType == "create" {
		if _, statErr := os.Stat(resolved); statErr == nil {
			return FileResult{Path: displayPath, OK: false, Error: "target file already exists"}
		}
	}

	updated, hunksApplied, err := applyToContent(string(original), filePatch)
	if err != nil {
		return FileResult{Path: displayPath, OK: false, Error: err.Error()}
	}

	if opType == "delete" {
		if strings.TrimSpace(updated) != "" {
			return FileResult{Path: displayPath, OK: false, Error: "delete patch did not produce empty file"}
		}
		if removeErr := os.Remove(resolved); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return FileResult{Path: displayPath, OK: false, Error: "unable to delete file"}
		}
		return FileResult{Path: displayPath, OK: true, HunksApplied: hunksApplied}
	}

	if _, writeErr := writemod.WriteFileAtomic(resolved, []byte(updated), true); writeErr != nil {
		_, message := protocol.GetCodeAndMessage(writeErr, protocol.CodePatchFailed)
		return FileResult{Path: displayPath, OK: false, Error: message}
	}

	return FileResult{Path: displayPath, OK: true, HunksApplied: hunksApplied}
}

func patchType(filePatch FilePatch) string {
	if filePatch.OldPath == "/dev/null" && filePatch.NewPath != "/dev/null" {
		return "create"
	}
	if filePatch.NewPath == "/dev/null" && filePatch.OldPath != "/dev/null" {
		return "delete"
	}
	if filePatch.OldPath != filePatch.NewPath {
		return "rename"
	}
	return "modify"
}

func applyToContent(content string, filePatch FilePatch) (string, int, error) {
	if patchType(filePatch) == "rename" {
		return "", 0, protocol.Err(protocol.CodePatchFailed, "rename patches are not supported")
	}

	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	hasTrailingNewline := strings.HasSuffix(normalized, "\n")
	lines := splitLines(normalized)

	result := make([]string, 0, len(lines)+8)
	cursor := 0
	hunksApplied := 0

	for _, hunk := range filePatch.Hunks {
		expected := hunk.OldStart - 1
		if hunk.OldStart == 0 {
			expected = 0
		}
		if expected < cursor || expected > len(lines) {
			return "", hunksApplied, protocol.Err(protocol.CodePatchFailed, fmt.Sprintf("hunk start out of range for %s", filePatch.NewPath))
		}

		result = append(result, lines[cursor:expected]...)
		idx := expected

		for _, line := range hunk.Lines {
			switch line.Kind {
			case ' ':
				if idx >= len(lines) || lines[idx] != line.Text {
					return "", hunksApplied, protocol.Err(protocol.CodePatchFailed, "patch context mismatch")
				}
				result = append(result, lines[idx])
				idx++
			case '-':
				if idx >= len(lines) || lines[idx] != line.Text {
					return "", hunksApplied, protocol.Err(protocol.CodePatchFailed, "patch deletion mismatch")
				}
				idx++
			case '+':
				result = append(result, line.Text)
			default:
				return "", hunksApplied, protocol.Err(protocol.CodePatchFailed, "invalid hunk line")
			}
		}

		cursor = idx
		hunksApplied++
	}

	result = append(result, lines[cursor:]...)
	joined := strings.Join(result, "\n")
	if len(result) > 0 && hasTrailingNewline {
		joined += "\n"
	}
	if len(result) == 0 {
		joined = ""
	}
	return joined, hunksApplied, nil
}

func splitLines(content string) []string {
	if content == "" {
		return []string{}
	}
	parts := strings.Split(content, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func cleanPatchPath(path string) string {
	return filepath.ToSlash(filepath.Clean(path))
}
