package searchmod

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	filesmod "agentrail/internal/files"
	"agentrail/internal/protocol"
	"agentrail/internal/textutil"
	"agentrail/internal/workspace"
)

type Options struct {
	Query         string
	Root          string
	CaseSensitive bool
	Regex         bool
	Glob          string
	Limit         int
	MaxFileBytes  int64
	Deterministic bool
}

type Match struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Preview string `json:"preview"`
}

const (
	defaultMaxFileBytes int64 = 16 * 1024 * 1024
	hardMaxFileBytes    int64 = 64 * 1024 * 1024
	maxSearchWorkers          = 8
)

func Search(ctx context.Context, manager *workspace.Manager, options Options) ([]Match, error) {
	if strings.TrimSpace(options.Query) == "" {
		return nil, protocol.Err(protocol.CodeInvalidRequest, "query is required")
	}
	if options.Root == "" {
		options.Root = manager.Root
	}
	if options.MaxFileBytes < 0 {
		return nil, protocol.ErrDetails(protocol.CodeInvalidRequest, "max_file_bytes must be >= 0", protocol.ErrorDetails{"field": "max_file_bytes", "reason": "negative"})
	}
	if options.MaxFileBytes == 0 {
		options.MaxFileBytes = defaultMaxFileBytes
	}
	if options.MaxFileBytes > hardMaxFileBytes {
		return nil, protocol.ErrDetails(protocol.CodeInvalidRequest, "max_file_bytes exceeds limit", protocol.ErrorDetails{"field": "max_file_bytes", "reason": "too_large", "limit_bytes": hardMaxFileBytes})
	}
	if options.Limit < 0 {
		return nil, protocol.ErrDetails(protocol.CodeInvalidRequest, "invalid search limit", protocol.ErrorDetails{"field": "limit", "reason": "invalid_value"})
	}
	if options.Glob != "" {
		if _, err := filepath.Match(options.Glob, ""); err != nil {
			return nil, protocol.ErrDetails(protocol.CodeInvalidRequest, "invalid glob pattern", protocol.ErrorDetails{"field": "glob", "reason": "invalid_pattern"})
		}
	}

	var compiled *regexp.Regexp
	if options.Regex {
		pattern := options.Query
		if !options.CaseSensitive {
			pattern = "(?i)" + pattern
		}
		rx, err := regexp.Compile(pattern)
		if err != nil {
			return nil, protocol.Err(protocol.CodeInvalidRequest, "invalid regex query")
		}
		compiled = rx
	}

	paths, err := filesmod.CollectAbsoluteFiles(options.Root, manager)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return []Match{}, nil
	}
	if options.Deterministic {
		sort.Slice(paths, func(i, j int) bool {
			return manager.DisplayPath(paths[i]) < manager.DisplayPath(paths[j])
		})
		if options.Limit > 0 {
			return searchDeterministicWithLimit(ctx, manager, paths, options, compiled)
		}
	}

	workerCount := runtime.NumCPU()
	if workerCount < 2 {
		workerCount = 2
	}
	if workerCount > maxSearchWorkers {
		workerCount = maxSearchWorkers
	}
	// A single worker consumes the already sorted path list in order. Limiting
	// concurrent workers before sorting would otherwise select an arbitrary
	// subset and violate deterministic mode.
	if options.Deterministic {
		workerCount = 1
	}
	jobs := make(chan string, workerCount*2)

	var count atomic.Int64
	var matchesMu sync.Mutex
	matches := make([]Match, 0, 128)

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case path, ok := <-jobs:
					if !ok {
						return
					}
					if !options.Deterministic && options.Limit > 0 && int(count.Load()) >= options.Limit {
						continue
					}
					rel := manager.DisplayPath(path)
					if options.Glob != "" {
						matched, globErr := filepath.Match(options.Glob, rel)
						if globErr != nil {
							continue
						}
						if !matched {
							continue
						}
					}
					fileMatches := scanFile(ctx, path, rel, options, compiled)
					if len(fileMatches) == 0 {
						continue
					}
					matchesMu.Lock()
					for _, match := range fileMatches {
						if !options.Deterministic && options.Limit > 0 && int(count.Load()) >= options.Limit {
							break
						}
						matches = append(matches, match)
						count.Add(1)
					}
					matchesMu.Unlock()
				}
			}
		}()
	}

queueLoop:
	for _, path := range paths {
		if !options.Deterministic && options.Limit > 0 && int(count.Load()) >= options.Limit {
			break
		}
		select {
		case <-ctx.Done():
			break queueLoop
		case jobs <- path:
		}
	}
	close(jobs)
	wg.Wait()

	if ctx.Err() != nil {
		return nil, protocol.Err(protocol.CodeSearchError, "search canceled")
	}

	if options.Deterministic {
		sort.Slice(matches, func(i, j int) bool {
			if matches[i].Path != matches[j].Path {
				return matches[i].Path < matches[j].Path
			}
			if matches[i].Line != matches[j].Line {
				return matches[i].Line < matches[j].Line
			}
			return matches[i].Col < matches[j].Col
		})
		if options.Limit > 0 && len(matches) > options.Limit {
			matches = matches[:options.Limit]
		}
	}

	return matches, nil
}

func searchDeterministicWithLimit(ctx context.Context, manager *workspace.Manager, paths []string, options Options, rx *regexp.Regexp) ([]Match, error) {
	matches := make([]Match, 0, options.Limit)
	for _, path := range paths {
		if ctx.Err() != nil {
			return nil, protocol.Err(protocol.CodeSearchError, "search canceled")
		}
		rel := manager.DisplayPath(path)
		if options.Glob != "" {
			matched, globErr := filepath.Match(options.Glob, rel)
			if globErr != nil || !matched {
				continue
			}
		}
		for _, match := range scanFile(ctx, path, rel, options, rx) {
			matches = append(matches, match)
			if len(matches) >= options.Limit {
				return matches, nil
			}
		}
	}
	return matches, nil
}

func scanFile(ctx context.Context, path, rel string, options Options, rx *regexp.Regexp) []Match {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	if options.MaxFileBytes > 0 {
		if info, statErr := file.Stat(); statErr == nil && info.Size() > options.MaxFileBytes {
			return nil
		}
	}

	reader := bufio.NewReaderSize(io.LimitReader(file, options.MaxFileBytes+1), 64*1024)
	peek, _ := reader.Peek(4096)
	if textutil.IsLikelyBinary(peek) {
		return nil
	}

	var lowerQuery []byte
	if !options.Regex {
		if options.CaseSensitive {
			lowerQuery = []byte(options.Query)
		} else {
			lowerQuery = bytes.ToLower([]byte(options.Query))
		}
	}

	results := make([]Match, 0, 4)
	lineNo := 0
	var readBytes int64

	for {
		if ctx.Err() != nil {
			break
		}
		lineBytes, readErr := reader.ReadBytes('\n')
		if len(lineBytes) > 0 {
			readBytes += int64(len(lineBytes))
			if options.MaxFileBytes > 0 && readBytes > options.MaxFileBytes {
				break
			}
			lineNo++
			trimmed := bytes.TrimRight(lineBytes, "\r\n")
			lineText := string(trimmed)
			preview := bounded(lineText, 512)
			if options.Regex {
				indices := rx.FindAllStringIndex(lineText, -1)
				for _, idx := range indices {
					results = append(results, Match{
						Path:    rel,
						Line:    lineNo,
						Col:     idx[0] + 1,
						Preview: preview,
					})
					if options.Limit > 0 && len(results) >= options.Limit {
						return results
					}
				}
			} else {
				haystack := trimmed
				if !options.CaseSensitive {
					haystack = bytes.ToLower(trimmed)
				}
				start := 0
				for {
					idx := bytes.Index(haystack[start:], lowerQuery)
					if idx == -1 {
						break
					}
					results = append(results, Match{
						Path:    rel,
						Line:    lineNo,
						Col:     start + idx + 1,
						Preview: preview,
					})
					if options.Limit > 0 && len(results) >= options.Limit {
						return results
					}
					start += idx + len(lowerQuery)
					if start >= len(haystack) {
						break
					}
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			break
		}
	}

	return results
}

func bounded(text string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(text) <= max {
		return text
	}
	end := max
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	return text[:end]
}
