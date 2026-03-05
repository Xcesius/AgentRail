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

func Search(ctx context.Context, manager *workspace.Manager, options Options) ([]Match, error) {
	if strings.TrimSpace(options.Query) == "" {
		return nil, protocol.Err(protocol.CodeInvalidRequest, "query is required")
	}
	if options.Root == "" {
		options.Root = manager.Root
	}
	if options.Deterministic == false {
		// false is explicit, default in caller can force true.
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

	workerCount := runtime.NumCPU()
	if workerCount < 2 {
		workerCount = 2
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
					if options.Limit > 0 && int(count.Load()) >= options.Limit {
						continue
					}
					rel := manager.RelativePath(path)
					if options.Glob != "" {
						matched, globErr := filepath.Match(options.Glob, rel)
						if globErr != nil {
							continue
						}
						if !matched {
							continue
						}
					}
					fileMatches := scanFile(path, rel, options, compiled)
					if len(fileMatches) == 0 {
						continue
					}
					matchesMu.Lock()
					for _, match := range fileMatches {
						if options.Limit > 0 && int(count.Load()) >= options.Limit {
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

	for _, path := range paths {
		if options.Limit > 0 && int(count.Load()) >= options.Limit {
			break
		}
		select {
		case <-ctx.Done():
			break
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

func scanFile(path, rel string, options Options, rx *regexp.Regexp) []Match {
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

	reader := bufio.NewReaderSize(file, 64*1024)
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
	return text[:max]
}
