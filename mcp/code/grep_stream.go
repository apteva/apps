package main

import (
	"bufio"
	"bytes"
	"io"
	"os"
)

// Keep only the current line and the requested surrounding context. Grep no
// longer materializes every line of every source file before matching it.
type grepLineStream struct {
	scanner       *bufio.Scanner
	closer        io.Closer
	before, queue []string
	line          int
	radius        int
}

func openGrepLines(store FileStore, slug, path string, radius int) (*grepLineStream, error) {
	var reader io.Reader
	var closer io.Closer
	if local, ok := store.(FileStoreLocalPath); ok {
		full, err := safeJoinSource(local.RepoPath(slug), path)
		if err != nil {
			return nil, err
		}
		f, err := os.Open(full)
		if err != nil {
			return nil, err
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxFileBytes() {
			f.Close()
			return nil, err
		}
		reader = f
		closer = f
	} else {
		body, err := store.Read(slug, path)
		if err != nil {
			return nil, err
		}
		if int64(len(body)) > maxFileBytes() {
			return nil, nil
		}
		reader = bytes.NewReader(body)
	}
	br := bufio.NewReader(reader)
	prefix, _ := br.Peek(4096)
	if !isLikelyText(prefix) {
		if closer != nil {
			closer.Close()
		}
		return nil, nil
	}
	scanner := bufio.NewScanner(br)
	scanner.Buffer(make([]byte, 64*1024), int(maxFileBytes()+1))
	return &grepLineStream{scanner: scanner, closer: closer, radius: radius}, nil
}
func (s *grepLineStream) Next() bool {
	if s.radius == 0 {
		if !s.scanner.Scan() {
			return false
		}
		s.line++
		return true
	}
	if s.line > 0 && len(s.queue) > 0 {
		if s.radius > 0 {
			if len(s.before) == s.radius {
				copy(s.before, s.before[1:])
				s.before = s.before[:len(s.before)-1]
			}
			s.before = append(s.before, s.queue[0])
		}
		copy(s.queue, s.queue[1:])
		s.queue = s.queue[:len(s.queue)-1]
	}
	for len(s.queue) <= s.radius && s.scanner.Scan() {
		s.queue = append(s.queue, s.scanner.Text())
	}
	if len(s.queue) == 0 {
		return false
	}
	s.line++
	return true
}
func (s *grepLineStream) Close() {
	if s.closer != nil {
		s.closer.Close()
	}
}
