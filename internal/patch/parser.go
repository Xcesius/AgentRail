package patchmod

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"codex-tool/internal/protocol"
)

type HunkLine struct {
	Kind byte
	Text string
}

type Hunk struct {
	OldStart int
	OldLines int
	NewStart int
	NewLines int
	Lines    []HunkLine
}

type FilePatch struct {
	OldPath string
	NewPath string
	Hunks   []Hunk
}

type PatchSet struct {
	Files []FilePatch
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func Parse(diff string) (PatchSet, error) {
	normalized := strings.ReplaceAll(diff, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	patch := PatchSet{Files: make([]FilePatch, 0, 8)}
	i := 0

	for i < len(lines) {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "rename from "),
			strings.HasPrefix(line, "rename to "),
			strings.HasPrefix(line, "copy from "),
			strings.HasPrefix(line, "copy to "):
			return PatchSet{}, protocol.Err(protocol.CodePatchFailed, "rename/copy metadata is not supported")
		case strings.HasPrefix(line, "diff --git "),
			strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "new file mode "),
			strings.HasPrefix(line, "deleted file mode "):
			i++
			continue
		case strings.HasPrefix(line, "--- "):
			oldPath := parsePatchPath(strings.TrimPrefix(line, "--- "))
			i++
			if i >= len(lines) || !strings.HasPrefix(lines[i], "+++ ") {
				return PatchSet{}, protocol.Err(protocol.CodePatchFailed, "missing +++ header")
			}
			newPath := parsePatchPath(strings.TrimPrefix(lines[i], "+++ "))
			i++

			filePatch := FilePatch{OldPath: oldPath, NewPath: newPath, Hunks: []Hunk{}}
			for i < len(lines) {
				if strings.HasPrefix(lines[i], "@@ ") {
					hunk, nextIdx, err := parseHunk(lines, i)
					if err != nil {
						return PatchSet{}, err
					}
					filePatch.Hunks = append(filePatch.Hunks, hunk)
					i = nextIdx
					continue
				}
				if strings.HasPrefix(lines[i], "--- ") || strings.HasPrefix(lines[i], "diff --git ") {
					break
				}
				if strings.HasPrefix(lines[i], "index ") || strings.HasPrefix(lines[i], "new file mode ") || strings.HasPrefix(lines[i], "deleted file mode ") {
					i++
					continue
				}
				if strings.TrimSpace(lines[i]) == "" {
					i++
					continue
				}
				return PatchSet{}, protocol.Err(protocol.CodePatchFailed, fmt.Sprintf("unexpected patch content: %s", lines[i]))
			}
			patch.Files = append(patch.Files, filePatch)
		default:
			i++
		}
	}

	if len(patch.Files) == 0 {
		return PatchSet{}, protocol.Err(protocol.CodePatchFailed, "no file patches found")
	}
	return patch, nil
}

func parsePatchPath(raw string) string {
	path := strings.TrimSpace(raw)
	if idx := strings.IndexAny(path, "\t "); idx >= 0 {
		path = path[:idx]
	}
	if path == "/dev/null" {
		return path
	}
	if strings.HasPrefix(path, "a/") || strings.HasPrefix(path, "b/") {
		path = path[2:]
	}
	return path
}

func parseHunk(lines []string, start int) (Hunk, int, error) {
	header := lines[start]
	matches := hunkHeader.FindStringSubmatch(header)
	if len(matches) == 0 {
		return Hunk{}, 0, protocol.Err(protocol.CodePatchFailed, "invalid hunk header")
	}

	oldStart, _ := strconv.Atoi(matches[1])
	oldLines := 1
	if matches[2] != "" {
		oldLines, _ = strconv.Atoi(matches[2])
	}
	newStart, _ := strconv.Atoi(matches[3])
	newLines := 1
	if matches[4] != "" {
		newLines, _ = strconv.Atoi(matches[4])
	}

	hunk := Hunk{
		OldStart: oldStart,
		OldLines: oldLines,
		NewStart: newStart,
		NewLines: newLines,
		Lines:    make([]HunkLine, 0, oldLines+newLines+2),
	}

	i := start + 1
	oldCount := 0
	newCount := 0
	for i < len(lines) {
		line := lines[i]
		if strings.HasPrefix(line, "@@ ") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "diff --git ") {
			break
		}
		if strings.HasPrefix(line, `\ No newline at end of file`) {
			i++
			continue
		}
		if len(line) == 0 {
			return Hunk{}, 0, protocol.Err(protocol.CodePatchFailed, "invalid empty hunk line")
		}
		kind := line[0]
		if kind != ' ' && kind != '+' && kind != '-' {
			return Hunk{}, 0, protocol.Err(protocol.CodePatchFailed, fmt.Sprintf("invalid hunk line prefix: %q", kind))
		}
		hunk.Lines = append(hunk.Lines, HunkLine{Kind: kind, Text: line[1:]})
		if kind == ' ' || kind == '-' {
			oldCount++
		}
		if kind == ' ' || kind == '+' {
			newCount++
		}
		i++
	}

	if oldCount != hunk.OldLines || newCount != hunk.NewLines {
		return Hunk{}, 0, protocol.Err(protocol.CodePatchFailed, "hunk line counts do not match header")
	}
	return hunk, i, nil
}
