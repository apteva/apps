package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Prepared uploads are private, bounded disk files scoped to one live session.
// No browser credentials or provider context are copied between backends.
type preparedUpload struct {
	consumed bool
	source   string
	files    []string
	meta     map[string]any
	cleanup  func()
	timer    *time.Timer
}

func uploadSourceKey(args map[string]any) string {
	values := map[string]string{}
	for _, key := range []string{"source_url", "base64", "file_path", "filename", "mime_type", "expected_sha256"} {
		values[key] = stringArg(args, key)
	}
	b, _ := json.Marshal(values)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s *session) clearPreparedUpload() {
	if p := s.preparedUpload; p != nil {
		p.timer.Stop()
		p.cleanup()
		s.preparedUpload = nil
	}
}

func (s *session) prepareUpload(args map[string]any) (map[string]any, error) {
	s.clearPreparedUpload()
	limit := int64(maxUploadBytes)
	if s.backend == "local" {
		limit = 5 * 1024 * 1024 * 1024
	} else if s.backend != "browserbase" {
		return nil, errors.New("this browser backend does not support file uploads")
	}
	// URL downloads stream to disk; base64 remains subject to the small-file cap.
	var files []string
	var meta map[string]any
	var cleanup func()
	var err error
	if raw := stringArg(args, "source_url"); raw != "" && stringArg(args, "base64") == "" && stringArg(args, "file_path") == "" {
		files, meta, cleanup, err = prepareUploadFileFromURLLimit(raw, args, limit, 110*time.Second)
	} else {
		files, meta, cleanup, err = prepareUploadFiles(args)
	}
	if err != nil {
		// Source URLs can be signed. Never put transport errors containing them in logs.
		return nil, fmt.Errorf("could not stage upload source for backend %s within %d bytes and 110 seconds; clip was not modified", s.backend, limit)
	}
	keep := false
	defer func() {
		if !keep {
			cleanup()
		}
	}()
	info, err := os.Stat(files[0])
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit {
		return nil, fmt.Errorf("upload source must be a nonempty regular file of at most %d bytes for backend %s", limit, s.backend)
	}
	// Freeze caller-owned file_path sources so preflight and upload use identical bytes.
	if stringArg(args, "file_path") != "" {
		path, remove, e := createUploadTempPath(safeUploadFilename(firstNonEmpty(stringArg(args, "filename"), info.Name())))
		if e != nil {
			return nil, e
		}
		in, e := os.Open(files[0])
		if e != nil {
			remove()
			return nil, e
		}
		out, e := os.Create(path)
		if e != nil {
			in.Close()
			remove()
			return nil, e
		}
		n, e := io.Copy(out, io.LimitReader(in, limit+1))
		in.Close()
		closeErr := out.Close()
		if e != nil || closeErr != nil || n != info.Size() {
			remove()
			return nil, errors.New("source changed or could not be staged")
		}
		cleanup()
		cleanup = remove
		files = []string{path}
		meta = uploadFileMeta(path, n, stringArg(args, "mime_type"), "file_path")
	}
	f, err := os.Open(files[0])
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	f.Close()
	if err != nil {
		return nil, err
	}
	meta["sha256"] = hex.EncodeToString(h.Sum(nil))
	if expected := stringArg(args, "expected_sha256"); expected != "" && expected != meta["sha256"] {
		return nil, errors.New("upload source SHA-256 mismatch; replacement blocked")
	}
	meta["uploaded"] = false
	meta["prepared"] = true
	meta["backend"] = s.backend
	meta["max_size_bytes"] = limit
	meta["expires_in_seconds"] = 900
	var once sync.Once
	remove := cleanup
	p := &preparedUpload{source: uploadSourceKey(args), files: files, meta: meta, cleanup: func() { once.Do(remove) }}
	p.timer = time.AfterFunc(15*time.Minute, p.cleanup)
	s.preparedUpload = p
	keep = true
	return meta, nil
}

func (s *session) takeUpload(args map[string]any) ([]string, map[string]any, func(), error) {
	if p := s.preparedUpload; p != nil && p.source == uploadSourceKey(args) {
		if p.consumed {
			return nil, nil, func() {}, errors.New("prepared upload already selected; inspect native upload outcome before another attempt")
		}
		if !p.timer.Stop() {
			p.cleanup()
			return nil, nil, func() {}, errors.New("prepared upload expired; restart preflight before changing the clip")
		}
		meta := map[string]any{}
		for k, v := range p.meta {
			meta[k] = v
		}
		meta["uploaded"] = true
		// Chrome may read the file asynchronously after native input selection.
		// Retain it through the site's upload/save verification, until session
		// close or the bounded expiry. Selecting an input is not upload completion.
		p.consumed = true
		p.timer = time.AfterFunc(15*time.Minute, p.cleanup)
		return p.files, meta, func() {}, nil
	}
	if boolArgDefault(args, "require_prepared", false) {
		return nil, nil, func() {}, errors.New("no matching prepared upload in this session; refusing to refetch after replacement")
	}
	// Compatibility path remains bounded even for file_path on cloud browsers.
	files, meta, cleanup, err := prepareUploadFiles(args)
	if err == nil && s.backend != "local" {
		if info, e := os.Stat(files[0]); e != nil || info.Size() > maxUploadBytes {
			cleanup()
			return nil, nil, func() {}, errors.New("file exceeds cloud browser transport limit; use an explicitly configured local context")
		}
	}
	return files, meta, cleanup, err
}
