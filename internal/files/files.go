package filesmod

import (
	"container/heap"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"agentrail/internal/protocol"
	"agentrail/internal/workspace"
)

const cursorVersion = 1

var errPageComplete = errors.New("file page complete")

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
	if limit > 0 {
		return listFilesBoundedPage(root, manager, limit, cursor)
	}

	absolutePaths, err := CollectAbsoluteFiles(root, manager)
	if err != nil {
		return Page{}, err
	}

	displayPaths := make([]string, 0, len(absolutePaths))
	for _, path := range absolutePaths {
		displayPaths = append(displayPaths, manager.DisplayPath(path))
	}
	sort.Strings(displayPaths)

	return Page{Paths: displayPaths, HasMore: false}, nil
}

func listFilesBoundedPage(root string, manager *workspace.Manager, limit int, cursor string) (Page, error) {
	rootIdentity := canonicalCursorRoot(root)
	after := ""
	anchorFound := cursor == ""
	if cursor != "" {
		payload, err := decodeCursor(cursor)
		if err != nil {
			return Page{}, err
		}
		if payload.Root != rootIdentity {
			return Page{}, protocol.ErrDetails(protocol.CodeCursorInvalid, "cursor does not match requested root", protocol.ErrorDetails{"field": "cursor", "reason": "root_mismatch"})
		}
		after = payload.After
	}

	pageCapacity := limit + 1
	if pageCapacity <= 0 || pageCapacity > 1024 {
		pageCapacity = 1024
	}
	paths := make([]string, 0, pageCapacity)
	err := walkSortedFiles(root, manager, func(path string) error {
		display := manager.DisplayPath(path)
		if display == after {
			anchorFound = true
			return nil
		}
		if !anchorFound || (after != "" && display <= after) {
			return nil
		}
		paths = append(paths, display)
		if len(paths) > limit {
			return errPageComplete
		}
		return nil
	})
	if err != nil && !errors.Is(err, errPageComplete) {
		return Page{}, protocol.Err(protocol.CodeInvalidRequest, "unable to enumerate files")
	}
	if !anchorFound {
		return Page{}, protocol.ErrDetails(protocol.CodeCursorStale, "cursor anchor is no longer present", protocol.ErrorDetails{"field": "cursor", "reason": "stale_anchor"})
	}

	page := Page{Paths: paths, HasMore: len(paths) > limit}
	if page.HasMore {
		page.Paths = page.Paths[:limit]
		nextCursor, err := encodeCursor(rootIdentity, page.Paths[len(page.Paths)-1])
		if err != nil {
			return Page{}, protocol.Err(protocol.CodeInvalidRequest, "unable to encode cursor")
		}
		page.NextCursor = nextCursor
	}
	return page, nil
}

func walkSortedFiles(root string, manager *workspace.Manager, visit func(string) error) error {
	frontier := &pathEntryHeap{}
	heap.Init(frontier)
	if err := pushDirectoryEntries(frontier, root, manager); err != nil {
		return err
	}
	for frontier.Len() > 0 {
		entry := heap.Pop(frontier).(pathEntry)
		if entry.isDir {
			if manager.ShouldSkipDir(entry.path) {
				continue
			}
			if err := pushDirectoryEntries(frontier, entry.path, manager); err != nil {
				return err
			}
			continue
		}
		if manager.IsDeniedPath(entry.path) {
			continue
		}
		if err := visit(entry.path); err != nil {
			return err
		}
	}
	return nil
}

func pushDirectoryEntries(frontier *pathEntryHeap, dir string, manager *workspace.Manager) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Clean(filepath.Join(dir, entry.Name()))
		if entry.IsDir() && manager.ShouldSkipDir(path) {
			continue
		}
		heap.Push(frontier, pathEntry{path: path, display: manager.DisplayPath(path), isDir: entry.IsDir()})
	}
	return nil
}

type pathEntry struct {
	path    string
	display string
	isDir   bool
}

type pathEntryHeap []pathEntry

func (h pathEntryHeap) Len() int           { return len(h) }
func (h pathEntryHeap) Less(i, j int) bool { return h[i].display < h[j].display }
func (h pathEntryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *pathEntryHeap) Push(value any)    { *h = append(*h, value.(pathEntry)) }
func (h *pathEntryHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

func CollectAbsoluteFiles(root string, manager *workspace.Manager) ([]string, error) {
	files := make([]string, 0, 256)
	err := walkAbsoluteFiles(root, manager, func(path string) error {
		files = append(files, filepath.Clean(path))
		return nil
	})
	if err != nil {
		return nil, protocol.Err(protocol.CodeInvalidRequest, "unable to enumerate files")
	}
	sort.Strings(files)
	return files, nil
}

func walkAbsoluteFiles(root string, manager *workspace.Manager, visit func(string) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
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
		return visit(filepath.Clean(path))
	})
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
