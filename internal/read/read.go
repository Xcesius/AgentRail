package readmod

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"

	"codex-tool/internal/protocol"
)

const defaultMaxBytes int64 = 1024 * 1024

type Options struct {
	StartLine int
	EndLine   int
	MaxBytes  int64
}

type Result struct {
	Content   string `json:"content"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Truncated bool   `json:"truncated"`
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
	if isBinary(peek) {
		return Result{}, protocol.Err(protocol.CodeBinaryFile, "binary file cannot be read")
	}

	var out bytes.Buffer
	lineNo := 0
	lastLine := options.StartLine - 1
	truncated := false

	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNo++
			if lineNo >= options.StartLine {
				if options.EndLine > 0 && lineNo > options.EndLine {
					break
				}

				remaining := options.MaxBytes - int64(out.Len())
				if remaining <= 0 {
					truncated = true
					break
				}
				if int64(len(line)) > remaining {
					out.Write(line[:remaining])
					truncated = true
					lastLine = lineNo
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
		Content:   out.String(),
		StartLine: options.StartLine,
		EndLine:   lastLine,
		Truncated: truncated,
	}, nil
}

func isBinary(sample []byte) bool {
	if len(sample) == 0 {
		return false
	}
	nonText := 0
	for _, b := range sample {
		if b == 0 {
			return true
		}
		if b == '\n' || b == '\r' || b == '\t' {
			continue
		}
		if b < 0x20 || b > 0x7e {
			nonText++
		}
	}
	return float64(nonText)/float64(len(sample)) > 0.30
}
