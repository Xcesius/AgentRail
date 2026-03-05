package readmod

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"

	"agentrail/internal/protocol"
	"agentrail/internal/textutil"
)

const defaultMaxBytes int64 = 1024 * 1024

type Options struct {
	StartLine int
	EndLine   int
	MaxBytes  int64
}

type Result struct {
	Content       string `json:"content"`
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
		return Result{}, protocol.Err(protocol.CodeInvalidRequest, "end_line must be >= 0")
	}
	if options.EndLine > 0 && options.EndLine < options.StartLine {
		return Result{}, protocol.Err(protocol.CodeInvalidRequest, "end_line must be >= start_line")
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = defaultMaxBytes
	}

	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, protocol.Err(protocol.CodeNotFound, "path not found")
		}
		return Result{}, protocol.Err(protocol.CodeInvalidRequest, "unable to open file")
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return Result{}, protocol.Err(protocol.CodeInvalidRequest, "unable to stat file")
	}
	if info.IsDir() {
		return Result{}, protocol.Err(protocol.CodeInvalidRequest, "path is a directory")
	}

	reader := bufio.NewReaderSize(file, 64*1024)
	peek, _ := reader.Peek(4096)
	if textutil.IsLikelyBinary(peek) {
		return Result{}, protocol.Err(protocol.CodeBinaryFile, "binary file cannot be read")
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
						return Result{}, protocol.Err(protocol.CodeTooLarge, "first selected line exceeds max_bytes")
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
		StartLine:     options.StartLine,
		EndLine:       lastLine,
		Truncated:     truncated,
		HasMore:       hasMore,
		NextStartLine: nextStartLine,
	}, nil
}
