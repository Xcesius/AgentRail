package readmod

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"agentrail/internal/protocol"
	"agentrail/internal/textutil"
)

const (
	defaultMaxBytes int64 = 1024 * 1024
	hardMaxBytes    int64 = 64 * 1024 * 1024
)

type Options struct {
	DisplayPath string
	StartLine   int
	EndLine     int
	MaxBytes    int64
}

type Result struct {
	Content       string `json:"content"`
	FileToken     string `json:"file_token"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
	Truncated     bool   `json:"truncated"`
	HasMore       bool   `json:"has_more"`
	NextStartLine int    `json:"next_start_line"`
}

func ReadFile(path string, options Options) (Result, error) {
	if options.StartLine <= 0 {
		options.StartLine = 1
	}
	if options.EndLine < 0 {
		return Result{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "end_line must be >= 0", protocol.ErrorDetails{"field": "end_line", "reason": "negative"})
	}
	if options.EndLine > 0 && options.EndLine < options.StartLine {
		return Result{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "end_line must be >= start_line", protocol.ErrorDetails{"field": "end_line", "reason": "before_start_line"})
	}
	if options.MaxBytes < 0 {
		return Result{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "max_bytes must be >= 0", protocol.ErrorDetails{"field": "max_bytes", "reason": "negative"})
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = defaultMaxBytes
	}
	if options.MaxBytes > hardMaxBytes {
		return Result{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "max_bytes exceeds limit", protocol.ErrorDetails{"field": "max_bytes", "reason": "too_large", "limit_bytes": hardMaxBytes})
	}

	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, protocol.ErrDetails(protocol.CodeNotFound, "path not found", protocol.ErrorDetails{"path": displayPath(path, options.DisplayPath), "kind": "file"})
		}
		return Result{}, protocol.Err(protocol.CodeInvalidRequest, "unable to open file")
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return Result{}, protocol.Err(protocol.CodeInvalidRequest, "unable to stat file")
	}
	if info.IsDir() {
		return Result{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "path is a directory", protocol.ErrorDetails{"field": "path", "reason": "directory"})
	}
	if !info.Mode().IsRegular() {
		return Result{}, protocol.ErrDetails(protocol.CodeInvalidRequest, "path is not a regular file", protocol.ErrorDetails{"field": "path", "reason": "unsupported_file_type"})
	}

	sample := make([]byte, 4096)
	sampleBytes, sampleErr := file.ReadAt(sample, 0)
	if sampleErr != nil && !errors.Is(sampleErr, io.EOF) {
		return Result{}, protocol.Err(protocol.CodeInvalidRequest, "unable to inspect file")
	}
	if textutil.IsLikelyBinary(sample[:sampleBytes]) {
		return Result{}, protocol.ErrDetails(protocol.CodeBinaryFile, "binary file cannot be read", protocol.ErrorDetails{"path": displayPath(path, options.DisplayPath)})
	}

	hasher := sha256.New()
	reader := bufio.NewReaderSize(io.TeeReader(file, hasher), 64*1024)

	var out bytes.Buffer
	lineNo := 0
	lastLine := options.StartLine - 1
	truncated := false
	hasMore := false
	nextStartLine := 0

	for {
		retainLimit := int64(0)
		nextLine := lineNo + 1
		if nextLine >= options.StartLine && (options.EndLine == 0 || nextLine <= options.EndLine) {
			retainLimit = options.MaxBytes - int64(out.Len())
		}
		line, lineBytes, readErr := readBoundedLine(reader, retainLimit)
		if lineBytes > 0 {
			lineNo++
			if lineNo >= options.StartLine {
				if options.EndLine > 0 && lineNo > options.EndLine {
					hasMore = true
					nextStartLine = lineNo
					break
				}

				remaining := options.MaxBytes - int64(out.Len())
				if lineBytes > remaining {
					if out.Len() == 0 {
						return Result{}, protocol.ErrDetails(protocol.CodeTooLarge, "first selected line exceeds max_bytes", protocol.ErrorDetails{"field": "max_bytes", "limit_bytes": options.MaxBytes, "actual_bytes": lineBytes})
					}
					truncated = true
					hasMore = true
					nextStartLine = lineNo
					break
				}
				out.Write(line)
				lastLine = lineNo
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return Result{}, protocol.Err(protocol.CodeInvalidRequest, "unable to read file")
		}
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return Result{}, protocol.Err(protocol.CodeInvalidRequest, "unable to hash file")
	}
	fileToken := "sha256:" + hex.EncodeToString(hasher.Sum(nil))

	return Result{
		Content:       out.String(),
		FileToken:     fileToken,
		StartLine:     options.StartLine,
		EndLine:       lastLine,
		Truncated:     truncated,
		HasMore:       hasMore,
		NextStartLine: nextStartLine,
	}, nil
}

func readBoundedLine(reader *bufio.Reader, retainLimit int64) ([]byte, int64, error) {
	var line bytes.Buffer
	var total int64
	for {
		fragment, err := reader.ReadSlice('\n')
		total += int64(len(fragment))
		if int64(line.Len()) < retainLimit {
			remaining := retainLimit - int64(line.Len())
			keep := len(fragment)
			if int64(keep) > remaining {
				keep = int(remaining)
			}
			_, _ = line.Write(fragment[:keep])
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line.Bytes(), total, err
	}
}

func displayPath(path, override string) string {
	if override != "" {
		return override
	}
	return filepath.ToSlash(filepath.Clean(path))
}
