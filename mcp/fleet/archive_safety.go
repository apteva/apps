package main

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Every write is relative to an opened root. Lexical checks alone cannot
// contain a chain of symlinks supplied by an archive.
func extractArchiveEntry(root *os.Root, name string, h *tar.Header, r io.Reader) error {
	name = filepath.Clean(filepath.FromSlash(name))
	if !filepath.IsLocal(name) {
		return fmt.Errorf("unsafe archive path %q", name)
	}
	if name == "." {
		if h.Typeflag == tar.TypeDir {
			return nil
		}
		return fmt.Errorf("invalid root entry")
	}
	// Reject aliasing entries even when their symlink target happens to remain
	// inside the root: an archive must not overwrite another entry by alias.
	for parent := filepath.Dir(name); parent != "."; parent = filepath.Dir(parent) {
		if info, err := root.Lstat(parent); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink archive parent %q", parent)
		}
	}
	if err := root.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	switch h.Typeflag {
	case tar.TypeDir:
		if info, err := root.Lstat(name); err == nil && !info.IsDir() {
			return fmt.Errorf("conflicting archive entry %q", name)
		}
		return root.MkdirAll(name, os.FileMode(h.Mode).Perm()|0700)
	case tar.TypeSymlink:
		link := filepath.FromSlash(h.Linkname)
		resolved := filepath.Clean(filepath.Join(filepath.Dir(name), link))
		if filepath.IsAbs(link) || !filepath.IsLocal(resolved) || strings.ContainsRune(link, '\x00') {
			return fmt.Errorf("unsafe archive symlink %q", h.Linkname)
		}
		return root.Symlink(link, name)
	case tar.TypeReg, tar.TypeRegA:
		f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode).Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(f, r, h.Size)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	default:
		return fmt.Errorf("unsupported archive entry type %d", h.Typeflag)
	}
}
