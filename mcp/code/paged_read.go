package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

type pagedFileReader interface {
	ReadPage(slug, path string, offset, limit int) (*ReadResult, error)
}

func (s *LocalFileStore) ReadPage(slug, relPath string, offset, limit int) (*ReadResult, error) {
	full, err := s.resolve(slug, relPath)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	if info.Size() <= 1<<20 {
		return cachedReadPage(f, full, relPath, info, offset, limit)
	}
	if offset <= 0 {
		offset = 1
	}
	if limit <= 0 {
		limit = defaultReadLimit
	}
	if limit > maxReadLimit {
		limit = maxReadLimit
	}

	hash := sha256.New()
	reader := bufio.NewReaderSize(f, 64*1024)
	var page []string
	var current strings.Builder
	line := 1
	total := 0
	var size int64
	longLines := 0
	lineWasTruncated := false
	pendingLine := false
	for {
		fragment, readErr := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			pendingLine = true
			_, _ = hash.Write(fragment)
			size += int64(len(fragment))
			if line >= offset && line < offset+limit {
				content := fragment
				if content[len(content)-1] == '\n' {
					content = content[:len(content)-1]
				}
				if current.Len() < maxReadLineChars*4 {
					remaining := maxReadLineChars*4 - current.Len()
					if len(content) > remaining {
						content = content[:remaining]
						lineWasTruncated = true
					}
					_, _ = current.Write(content)
				} else if len(content) > 0 {
					lineWasTruncated = true
				}
			}
		}
		lineComplete := readErr == nil || (errors.Is(readErr, io.EOF) && pendingLine)
		if lineComplete {
			pendingLine = false
			total++
			if line >= offset && line < offset+limit {
				value, truncated := truncateLine(current.String(), maxReadLineChars)
				if truncated || lineWasTruncated {
					longLines++
					if !strings.Contains(value, "[line truncated]") {
						value += " ... [line truncated]"
					}
				}
				page = append(page, value)
			}
			current.Reset()
			lineWasTruncated = false
			line++
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil && !errors.Is(readErr, bufio.ErrBufferFull) {
			return nil, readErr
		}
	}

	start := offset
	if start > total {
		start = total + 1
	}
	end := start + len(page) - 1
	if len(page) == 0 {
		end = total
	}
	width := numWidth(end)
	var content strings.Builder
	for i, value := range page {
		fmt.Fprintf(&content, "%*d\t%s\n", width, start+i, value)
	}
	truncated := end < total
	next := 0
	hint := ""
	if truncated {
		next = end + 1
		hint = fmt.Sprintf("partial read; call code_read_file with offset=%d and limit=%d to continue", next, limit)
	}
	return &ReadResult{
		Path:               relPath,
		Content:            content.String(),
		TotalLines:         total,
		StartLine:          start,
		EndLine:            end,
		NextOffset:         next,
		Size:               size,
		SHA256:             hex.EncodeToString(hash.Sum(nil)),
		Truncated:          truncated,
		LongLinesTruncated: longLines,
		Hint:               hint,
	}, nil
}
