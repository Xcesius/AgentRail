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
	if limit < 0 {
		return Page{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "limit must be >= 0", protocol.ErrorDetails{"field": "limit", "reason": "negative"})
	}
	if limit > 0 || cursor != "" {
		return listFilesBoundedPage(root, manager, limit, cursor)
	}

	allowOutside := !manager.IsWithinWorkspace(root)
	displayPaths := make([]string, 0, 256)
	err := walkAbsoluteFiles(root, manager, allowOutside, func(path string) error {
		displayPaths = append(displayPaths, manager.DisplayPath(path))
		return nil
	})
	if err != nil {
		return Page{}, protocol.Err(protocol.CodeInvalidRequest, "unable to enumerate files")
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

	pageCapacity := 256
	if limit > 0 {
		pageCapacity = limit + 1
		if pageCapacity <= 0 || pageCapacity > 1024 {
			pageCapacity = 1024
		}
	}
	paths := make([]string, 0, pageCapacity)
	allowOutside := !manager.IsWithinWorkspace(root)
	err := walkSortedFiles(root, manager, allowOutside, after, func(_ string, display string) error {
		if display == after {
			anchorFound = true
			return nil
		}
		if !anchorFound || (after != "" && display <= after) {
			return nil
		}
		paths = append(paths, display)
		if limit > 0 && len(paths) > limit {
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

	hasMore := limit > 0 && len(paths) > limit
	page := Page{Paths: paths, HasMore: hasMore}
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

func walkSortedFiles(root string, manager *workspace.Manager, allowOutside bool, after string, visit func(path, display string) error) error {
	frontier := &pathEntryHeap{}
	heap.Init(frontier)
	if err := pushDirectoryEntries(frontier, root, manager, allowOutside, after); err != nil {
		return err
	}
	for frontier.Len() > 0 {
		entry := heap.Pop(frontier).(pathEntry)
		if entry.isDir {
			if err := pushDirectoryEntries(frontier, entry.path, manager, allowOutside, after); err != nil {
				return err
			}
			continue
		}
		if err := visit(entry.path, entry.display); err != nil {
			return err
		}
	}
	return nil
}

func canPruneDirectory(dirDisplay, after string) bool {
	if after == "" {
		return false
	}
	prefix := dirDisplay + "/"
	return !strings.HasPrefix(after, prefix) && prefix < after
}

func pushDirectoryEntries(frontier *pathEntryHeap, dir string, manager *workspace.Manager, allowOutside bool, after string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Clean(filepath.Join(dir, entry.Name()))
		if entry.IsDir() {
			if manager.ShouldSkipDir(path) {
				continue
			}
			display := manager.DisplayPath(path)
			if canPruneDirectory(display, after) {
				continue
			}
			heap.Push(frontier, pathEntry{path: path, display: display, isDir: true})
			continue
		}
		if manager.IsDeniedPath(path) {
			continue
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			if !isAllowedSymlinkFile(path, manager, allowOutside) {
				continue
			}
		}
		display := manager.DisplayPath(path)
		if after != "" && display < after {
			continue
		}
		heap.Push(frontier, pathEntry{path: path, display: display, isDir: false})
	}
	return nil
}

func isAllowedSymlinkFile(path string, manager *workspace.Manager, allowOutside bool) bool {
	resolved, err := manager.ResolveReadPath(path, allowOutside)
	if err != nil {
		return false
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return true
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
	allowOutside := !manager.IsWithinWorkspace(root)
	err := walkAbsoluteFiles(root, manager, allowOutside, func(path string) error {
		files = append(files, filepath.Clean(path))
		return nil
	})
	if err != nil {
		return nil, protocol.Err(protocol.CodeInvalidRequest, "unable to enumerate files")
	}
	sort.Strings(files)
	return files, nil
}

func walkAbsoluteFiles(root string, manager *workspace.Manager, allowOutside bool, visit func(string) error) error {
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
		if d.Type()&fs.ModeSymlink != 0 {
			// Skip directory symlinks here; callers like search still inspect file symlinks via ResolveReadPath.
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				return nil
			}
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
