package main

import (
	"errors"
	"io/fs"
	"strings"
)

// treeReader is the read side of a file tree. Both FileStore-backed
// repos (user templates) and the embedded templatesFS (system
// templates) implement it, so fork() doesn't care which kind of
// source it's copying from.
type treeReader interface {
	ListPaths(slug string) ([]string, error)
	ReadFile(slug, relPath string) ([]byte, error)
}

// storeReader adapts a FileStore to treeReader. The slug parameter is
// the source repo's slug.
type storeReader struct{ s FileStore }

func (r storeReader) ListPaths(slug string) ([]string, error) {
	files, err := r.s.List(slug, "", true)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		if f.IsDir {
			continue
		}
		out = append(out, f.Path)
	}
	return out, nil
}

func (r storeReader) ReadFile(slug, p string) ([]byte, error) { return r.s.Read(slug, p) }

// embeddedReader exposes the //go:embed templatesFS as a treeReader.
// "slug" here is the framework name — the lookup is templates/<slug>/.
type embeddedReader struct{}

func (embeddedReader) ListPaths(framework string) ([]string, error) {
	if framework == "" || framework == "blank" {
		return nil, nil
	}
	root := "templates/" + framework
	var out []string
	err := fs.WalkDir(templatesFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		out = append(out, strings.TrimPrefix(p, root+"/"))
		return nil
	})
	return out, err
}

func (embeddedReader) ReadFile(framework, p string) ([]byte, error) {
	return templatesFS.ReadFile("templates/" + framework + "/" + p)
}

// embeddedTemplateNames returns the framework names that actually have
// a directory under templates/. Used by templates_list.
func embeddedTemplateNames() []string {
	entries, err := fs.ReadDir(templatesFS, "templates")
	if err != nil {
		return nil
	}
	out := []string{}
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// fork copies every file from the source tree under srcID into the
// destination repo's FileStore. Returns the count of files written.
// Errors from the destination Write fail fast — callers are
// responsible for cleaning up the partial repo if they want to.
func fork(src treeReader, srcID string, dst FileStore, dstSlug string) (int, error) {
	paths, err := src.ListPaths(srcID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range paths {
		body, err := src.ReadFile(srcID, p)
		if err != nil {
			return n, err
		}
		if _, err := dst.Write(dstSlug, p, body); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
