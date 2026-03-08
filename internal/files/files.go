package filesmod

import (
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"agentrail/internal/protocol"
	"agentrail/internal/workspace"
)

const cursorVersion = 1

type Page struct {
	Paths      []string `json:"paths"`
	HasMore    bool     `json:"has_more"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

type cursorPayload struct {
	Version int    `json:"v"`
	Root    string `json:"root"`
	After   string `json:"after"`
}

func ListFiles(root string, manager *workspace.Manager) ([]string, error) {
	page, err := ListFilesPage(root, manager, 0, "")
	if err != nil {
		return nil, err
	}
	return page.Paths, nil
}

func ListFilesPage(root string, manager *workspace.Manager, limit int, cursor string) (Page, error) {
	absolutePaths, err := CollectAbsoluteFiles(root, manager)
	if err != nil {
		return Page{}, err
	}

	displayPaths := make([]string, 0, len(absolutePaths))
	for _, path := range absolutePaths {
		displayPaths = append(displayPaths, manager.DisplayPath(path))
	}
	sort.Strings(displayPaths)

	if limit <= 0 {
		return Page{Paths: displayPaths, HasMore: false}, nil
	}

	rootIdentity := canonicalCursorRoot(root)
	startIndex, err := cursorStartIndex(displayPaths, rootIdentity, cursor)
	if err != nil {
		return Page{}, err
	}
	if startIndex >= len(displayPaths) {
		return Page{Paths: []string{}, HasMore: false}, nil
	}

	endIndex := startIndex + limit
	if endIndex > len(displayPaths) {
		endIndex = len(displayPaths)
	}
	page := Page{
		Paths:   displayPaths[startIndex:endIndex],
		HasMore: endIndex < len(displayPaths),
	}
	if page.HasMore && len(page.Paths) > 0 {
		nextCursor, err := encodeCursor(rootIdentity, page.Paths[len(page.Paths)-1])
		if err != nil {
			return Page{}, protocol.Err(protocol.CodeInvalidRequest, "unable to encode cursor")
		}
		page.NextCursor = nextCursor
	}
	return page, nil
}

func CollectAbsoluteFiles(root string, manager *workspace.Manager) ([]string, error) {
	files := make([]string, 0, 256)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && manager.ShouldSkipDir(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if manager.IsDeniedPath(path) {
			return nil
		}
		files = append(files, filepath.Clean(path))
		return nil
	})
	if err != nil {
		return nil, protocol.Err(protocol.CodeInvalidRequest, "unable to enumerate files")
	}
	sort.Strings(files)
	return files, nil
}

func cursorStartIndex(paths []string, rootDisplay, cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	payload, err := decodeCursor(cursor)
	if err != nil {
		return 0, err
	}
	if payload.Root != rootDisplay {
		return 0, protocol.ErrDetails(protocol.CodeCursorInvalid, "cursor does not match requested root", protocol.ErrorDetails{"field": "cursor", "reason": "root_mismatch"})
	}
	index := sort.SearchStrings(paths, payload.After)
	if index >= len(paths) || paths[index] != payload.After {
		return 0, protocol.ErrDetails(protocol.CodeCursorStale, "cursor anchor is no longer present", protocol.ErrorDetails{"field": "cursor", "reason": "stale_anchor"})
	}
	return index + 1, nil
}

func encodeCursor(rootDisplay, after string) (string, error) {
	payload := cursorPayload{Version: cursorVersion, Root: rootDisplay, After: after}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func canonicalCursorRoot(root string) string {
	clean := filepath.ToSlash(filepath.Clean(root))
	if runtime.GOOS == "windows" && len(clean) >= 2 && clean[1] == ':' {
		clean = strings.ToUpper(clean[:1]) + clean[1:]
	}
	return clean
}

func decodeCursor(raw string) (cursorPayload, error) {
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return cursorPayload{}, protocol.ErrDetails(protocol.CodeCursorInvalid, "invalid cursor", protocol.ErrorDetails{"field": "cursor", "reason": "malformed"})
	}
	var payload cursorPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return cursorPayload{}, protocol.ErrDetails(protocol.CodeCursorInvalid, "invalid cursor", protocol.ErrorDetails{"field": "cursor", "reason": "malformed"})
	}
	if payload.Version != cursorVersion {
		return cursorPayload{}, protocol.ErrDetails(protocol.CodeCursorInvalid, "unsupported cursor version", protocol.ErrorDetails{"field": "cursor", "reason": "version_mismatch"})
	}
	if payload.Root == "" || payload.After == "" {
		return cursorPayload{}, protocol.ErrDetails(protocol.CodeCursorInvalid, "invalid cursor", protocol.ErrorDetails{"field": "cursor", "reason": "missing_fields"})
	}
	return payload, nil
}
