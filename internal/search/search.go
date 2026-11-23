package searchmod

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

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
	AllowOutside  bool
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
	searchReaderBufSize       = 64 * 1024
)

var searchReaderPool = sync.Pool{
	New: func() any {
		return bufio.NewReaderSize(nil, searchReaderBufSize)
	},
}

type searchTarget struct {
	path string
	rel  string
}

type searchMatcher struct {
	regex             *regexp.Regexp
	literal           []byte
	literalLowerASCII []byte
	literalASCII      bool
	caseSensitive     bool
	unicodeFoldRx     *regexp.Regexp
}

func newSearchMatcher(options Options) (*searchMatcher, error) {
	if options.Regex {
		pattern := options.Query
		if !options.CaseSensitive {
			pattern = "(?i)" + pattern
		}
		rx, err := regexp.Compile(pattern)
		if err != nil {
			return nil, protocol.Err(protocol.CodeInvalidRequest, "invalid regex query")
		}
		return &searchMatcher{regex: rx, caseSensitive: options.CaseSensitive}, nil
	}

	m := &searchMatcher{
		literal:       []byte(options.Query),
		caseSensitive: options.CaseSensitive,
	}
	if !options.CaseSensitive {
		m.literalASCII = isAllASCII(m.literal)
		if m.literalASCII {
			lower := make([]byte, len(m.literal))
			for i, b := range m.literal {
				lower[i] = asciiLower(b)
			}
			m.literalLowerASCII = lower
		}
		rx, err := regexp.Compile("(?i)" + regexp.QuoteMeta(options.Query))
		if err != nil {
			return nil, protocol.Err(protocol.CodeInvalidRequest, "invalid query")
		}
		m.unicodeFoldRx = rx
	}
	return m, nil
}

func Search(ctx context.Context, manager *workspace.Manager, options Options) ([]Match, error) {
	return searchWithWorkerCount(ctx, manager, options, 0)
}

func searchWithWorkerCount(ctx context.Context, manager *workspace.Manager, options Options, workerOverride int) ([]Match, error) {
	if options.Query == "" {
		return nil, protocol.ErrDetails(protocol.CodeInvalidRequest, "query is required", protocol.ErrorDetails{"field": "query", "reason": "required"})
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

	globPattern := ""
	if options.Glob != "" {
		globPattern = normalizeGlob(options.Glob)
		if err := validateGlob(globPattern); err != nil {
			return nil, protocol.ErrDetails(protocol.CodeInvalidRequest, "invalid glob pattern", protocol.ErrorDetails{"field": "glob", "reason": "invalid_pattern"})
		}
	}

	matcher, err := newSearchMatcher(options)
	if err != nil {
		return nil, err
	}

	targets, err := collectSearchTargets(ctx, manager, options.Root, globPattern, options.AllowOutside)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return []Match{}, nil
	}

	if options.Deterministic {
		sort.Slice(targets, func(i, j int) bool {
			return targets[i].rel < targets[j].rel
		})
		if options.Limit > 0 {
			return searchDeterministicWithLimit(ctx, targets, options, matcher)
		}
	}

	workerCount := workerOverride
	if workerCount <= 0 {
		workerCount = min(max(runtime.NumCPU(), 2), maxSearchWorkers)
	}
	if len(targets) < workerCount {
		workerCount = len(targets)
	}

	jobs := make(chan searchTarget, workerCount*2)

	var count atomic.Int64
	var matchesMu sync.Mutex
	matches := make([]Match, 0, 128)

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Go(func() {
			var overflowBuf bytes.Buffer
			for {
				select {
				case <-ctx.Done():
					return
				case target, ok := <-jobs:
					if !ok {
						return
					}
					remaining := 0
					if !options.Deterministic && options.Limit > 0 {
						cur := int(count.Load())
						if cur >= options.Limit {
							continue
						}
						remaining = options.Limit - cur
					}
					fileMatches := scanFile(ctx, target.path, target.rel, options.MaxFileBytes, remaining, matcher, &overflowBuf)
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
		})
	}

queueLoop:
	for _, target := range targets {
		if !options.Deterministic && options.Limit > 0 && int(count.Load()) >= options.Limit {
			break
		}
		select {
		case <-ctx.Done():
			break queueLoop
		case jobs <- target:
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

func searchDeterministicWithLimit(ctx context.Context, targets []searchTarget, options Options, matcher *searchMatcher) ([]Match, error) {
	capacity := min(options.Limit, 128)
	matches := make([]Match, 0, capacity)
	var overflowBuf bytes.Buffer
	for _, target := range targets {
		if ctx.Err() != nil {
			return nil, protocol.Err(protocol.CodeSearchError, "search canceled")
		}
		remaining := options.Limit - len(matches)
		if remaining <= 0 {
			return matches, nil
		}
		fileMatches := scanFile(ctx, target.path, target.rel, options.MaxFileBytes, remaining, matcher, &overflowBuf)
		for _, match := range fileMatches {
			matches = append(matches, match)
			if len(matches) >= options.Limit {
				return matches, nil
			}
		}
	}
	return matches, nil
}

func collectSearchTargets(ctx context.Context, manager *workspace.Manager, root, globPattern string, allowOutside bool) ([]searchTarget, error) {
	targets := make([]searchTarget, 0, 256)
	seen := make(map[string]struct{}, 256)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if p != root && manager.ShouldSkipDir(p) {
				return filepath.SkipDir
			}
			return nil
		}
		if manager.IsDeniedPath(p) {
			return nil
		}

		clean := filepath.Clean(p)
		if d.Type()&fs.ModeSymlink != 0 {
			resolved, resolveErr := manager.ResolveReadPath(clean, allowOutside)
			if resolveErr != nil {
				if te, ok := protocol.AsToolError(resolveErr); ok && te.Code == protocol.CodePathDenied {
					return resolveErr
				}
				// Skip dangling or unresolvable symlinks without aborting the search.
				return nil
			}
			info, statErr := os.Stat(resolved)
			if statErr != nil || !info.Mode().IsRegular() {
				return nil
			}
			clean = resolved
		} else if !d.Type().IsRegular() {
			return nil
		}

		rel := manager.DisplayPath(clean)
		if globPattern != "" && !matchGlob(globPattern, rel) {
			return nil
		}

		key := clean
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if _, dup := seen[key]; dup {
			return nil
		}
		seen[key] = struct{}{}
		targets = append(targets, searchTarget{path: clean, rel: rel})
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, protocol.Err(protocol.CodeSearchError, "search canceled")
		}
		if _, ok := protocol.AsToolError(err); ok {
			return nil, err
		}
		return nil, protocol.Err(protocol.CodeInvalidRequest, "unable to enumerate files")
	}
	return targets, nil
}

func scanFile(ctx context.Context, filePath, rel string, maxFileBytes int64, limit int, matcher *searchMatcher, overflowBuf *bytes.Buffer) []Match {
	file, err := os.Open(filePath)
	if err != nil {
		return nil
	}
	defer file.Close()

	if maxFileBytes > 0 {
		if info, statErr := file.Stat(); statErr == nil && info.Size() > maxFileBytes {
			return nil
		}
	}

	reader := searchReaderPool.Get().(*bufio.Reader)
	reader.Reset(io.LimitReader(file, maxFileBytes+1))
	defer func() {
		reader.Reset(nil)
		searchReaderPool.Put(reader)
	}()

	peek, _ := reader.Peek(4096)
	if textutil.IsLikelyBinary(peek) {
		return nil
	}

	var results []Match
	lineNo := 0
	var readBytes int64

	for {
		if ctx.Err() != nil {
			break
		}
		lineBytes, readErr := readLineZeroAlloc(reader, overflowBuf)
		if len(lineBytes) > 0 {
			readBytes += int64(len(lineBytes))
			if maxFileBytes > 0 && readBytes > maxFileBytes {
				break
			}
			lineNo++
			trimmed := trimLineEnding(lineBytes)
			remaining := -1
			if limit > 0 {
				remaining = limit - len(results)
				if remaining <= 0 {
					return results
				}
			}

			cols := matcher.findCols(trimmed, remaining)
			if len(cols) > 0 {
				if results == nil {
					results = make([]Match, 0, 4)
				}
				preview := bounded(string(trimmed), 512)
				for _, col := range cols {
					results = append(results, Match{
						Path:    rel,
						Line:    lineNo,
						Col:     col,
						Preview: preview,
					})
					if limit > 0 && len(results) >= limit {
						return results
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

func readLineZeroAlloc(reader *bufio.Reader, overflow *bytes.Buffer) ([]byte, error) {
	frag, err := reader.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) {
		return frag, err
	}
	overflow.Reset()
	_, _ = overflow.Write(frag)
	for errors.Is(err, bufio.ErrBufferFull) {
		frag, err = reader.ReadSlice('\n')
		_, _ = overflow.Write(frag)
	}
	return overflow.Bytes(), err
}

func trimLineEnding(line []byte) []byte {
	n := len(line)
	if n > 0 && line[n-1] == '\n' {
		n--
	}
	if n > 0 && line[n-1] == '\r' {
		n--
	}
	return line[:n]
}

func (m *searchMatcher) findCols(line []byte, maxMatches int) []int {
	if m.regex != nil {
		indices := m.regex.FindAllIndex(line, maxMatches)
		if len(indices) == 0 {
			return nil
		}
		cols := make([]int, len(indices))
		for i, idx := range indices {
			cols[i] = idx[0] + 1
		}
		return cols
	}

	if m.caseSensitive {
		return findLiteralBytes(line, m.literal, maxMatches)
	}

	if m.literalASCII && isAllASCII(line) {
		return findLiteralASCIIFold(line, m.literalLowerASCII, maxMatches)
	}

	indices := m.unicodeFoldRx.FindAllIndex(line, maxMatches)
	if len(indices) == 0 {
		return nil
	}
	cols := make([]int, len(indices))
	for i, idx := range indices {
		cols[i] = idx[0] + 1
	}
	return cols
}

func findLiteralBytes(line, needle []byte, maxMatches int) []int {
	var cols []int
	start := 0
	for start <= len(line)-len(needle) {
		idx := bytes.Index(line[start:], needle)
		if idx == -1 {
			break
		}
		cols = append(cols, start+idx+1)
		if maxMatches > 0 && len(cols) >= maxMatches {
			break
		}
		start += idx + len(needle)
	}
	return cols
}

func findLiteralASCIIFold(line, lowerNeedle []byte, maxMatches int) []int {
	nLen := len(lowerNeedle)
	if nLen == 0 || len(line) < nLen {
		return nil
	}
	first := lowerNeedle[0]
	var cols []int
	for i := 0; i <= len(line)-nLen; {
		if asciiLower(line[i]) != first {
			i++
			continue
		}
		matched := true
		for j := 1; j < nLen; j++ {
			if asciiLower(line[i+j]) != lowerNeedle[j] {
				matched = false
				break
			}
		}
		if matched {
			cols = append(cols, i+1)
			if maxMatches > 0 && len(cols) >= maxMatches {
				break
			}
			i += nLen
		} else {
			i++
		}
	}
	return cols
}

func isAllASCII(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return false
		}
	}
	return true
}

func asciiLower(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

func normalizeGlob(glob string) string {
	return strings.ReplaceAll(glob, `\`, "/")
}

func validateGlob(glob string) error {
	for seg := range strings.SplitSeq(glob, "/") {
		if seg == "**" {
			continue
		}
		if _, err := path.Match(seg, ""); err != nil {
			return err
		}
	}
	return nil
}

func matchGlob(pattern, target string) bool {
	if !strings.Contains(pattern, "**") {
		matched, err := path.Match(pattern, target)
		return err == nil && matched
	}
	patSegs := strings.Split(pattern, "/")
	tgtSegs := strings.Split(target, "/")
	return matchGlobSegments(patSegs, tgtSegs)
}

func matchGlobSegments(pat, tgt []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// Collapse consecutive ** segments.
			for len(pat) > 1 && pat[1] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(tgt); i++ {
				if matchGlobSegments(pat[1:], tgt[i:]) {
					return true
				}
			}
			return false
		}
		if len(tgt) == 0 {
			return false
		}
		matched, err := path.Match(pat[0], tgt[0])
		if err != nil || !matched {
			return false
		}
		pat = pat[1:]
		tgt = tgt[1:]
	}
	return len(tgt) == 0
}

func bounded(text string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(text) <= max {
		return text
	}
	end := max
	limit := end - 4
	if limit < 0 {
		limit = 0
	}
	for i := end - 1; i >= limit; i-- {
		if utf8.RuneStart(text[i]) {
			if !utf8.FullRuneInString(text[i:end]) {
				return text[:i]
			}
			return text[:end]
		}
	}
	return text[:end]
}
