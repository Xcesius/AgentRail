package readmod

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"agentrail/internal/filemeta"
	"agentrail/internal/protocol"
	"agentrail/internal/textutil"
)

const defaultMaxBytes int64 = 1024 * 1024

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
	if options.MaxBytes <= 0 {
		options.MaxBytes = defaultMaxBytes
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

	fileToken, err := filemeta.TokenFromReader(file)
	if err != nil {
		return Result{}, protocol.Err(protocol.CodeInvalidRequest, "unable to hash file")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Result{}, protocol.Err(protocol.CodeInvalidRequest, "unable to seek file")
	}

	reader := bufio.NewReaderSize(file, 64*1024)
	peek, _ := reader.Peek(4096)
	if textutil.IsLikelyBinary(peek) {
		return Result{}, protocol.ErrDetails(protocol.CodeBinaryFile, "binary file cannot be read", protocol.ErrorDetails{"path": displayPath(path, options.DisplayPath)})
	}

	var out bytes.Buffer
	lineNo := 0
	lastLine := options.StartLine - 1
	truncated := false
	hasMore := false
	nextStartLine := 0

	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNo++
			if lineNo >= options.StartLine {
				if options.EndLine > 0 && lineNo > options.EndLine {
					hasMore = true
					nextStartLine = lineNo
					break
				}

				remaining := options.MaxBytes - int64(out.Len())
				if int64(len(line)) > remaining {
					if out.Len() == 0 {
						return Result{}, protocol.ErrDetails(protocol.CodeTooLarge, "first selected line exceeds max_bytes", protocol.ErrorDetails{"field": "max_bytes", "limit_bytes": options.MaxBytes, "actual_bytes": len(line)})
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

func displayPath(path, override string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	return filepath.ToSlash(filepath.Clean(path))
}
