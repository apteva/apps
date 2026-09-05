package main

import (
	"golang.org/x/sys/unix"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func staticPreviewRoot(repoRoot string) string {
	repoReal, _ := filepath.EvalSymlinks(repoRoot)
	for _, candidate := range []string{"dist", "build", "public"} {
		root := filepath.Join(repoRoot, candidate)
		rootReal, rootErr := filepath.EvalSymlinks(root)
		inside := rootErr == nil && (rootReal == repoReal || strings.HasPrefix(rootReal, repoReal+string(filepath.Separator)))
		if info, err := os.Stat(filepath.Join(root, "index.html")); inside && err == nil && !info.IsDir() {
			return root
		}
	}
	return repoRoot
}

func staticPreviewHandler(repoRoot string) http.Handler {
	root := staticPreviewRoot(repoRoot)
	files := http.FileServer(safePreviewFS{root: root})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := path.Clean("/" + r.URL.Path)
		for _, segment := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
			lower := strings.ToLower(segment)
			if strings.HasPrefix(segment, ".") || lower == "node_modules" || lower == "package-lock.json" || lower == "bun.lockb" {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		files.ServeHTTP(w, r)
	})
}

type safePreviewFS struct{ root string }

func (s safePreviewFS) Open(name string) (http.File, error) {
	// Open every component relative to the previous directory descriptor. No
	// symlink can redirect the lookup, including during a concurrent rename.
	fd, err := unix.Open(s.root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(path.Clean("/"+name), "/"), "/")
	for i, part := range parts {
		if part == "" {
			continue
		}
		if previewHidden(part) {
			_ = unix.Close(fd)
			return nil, fs.ErrPermission
		}
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, e := unix.Openat(fd, part, flags, 0)
		_ = unix.Close(fd)
		if e != nil {
			return nil, fs.ErrNotExist
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), name)
	info, err := f.Stat()
	if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
		f.Close()
		return nil, fs.ErrPermission
	}
	return previewFile{f}, nil
}
func previewHidden(name string) bool {
	return strings.HasPrefix(name, ".") || strings.EqualFold(name, "node_modules") || strings.EqualFold(name, "package-lock.json") || strings.EqualFold(name, "bun.lockb")
}

type previewFile struct{ *os.File }

func (f previewFile) Readdir(n int) ([]os.FileInfo, error) {
	out := []os.FileInfo{}
	for {
		entries, err := f.File.Readdir(1)
		for _, entry := range entries {
			if !previewHidden(entry.Name()) && entry.Mode()&os.ModeSymlink == 0 && (entry.IsDir() || entry.Mode().IsRegular()) {
				out = append(out, entry)
			}
		}
		if n > 0 && len(out) >= n {
			return out, nil
		}
		if err != nil {
			if err == io.EOF && (n <= 0 || len(out) > 0) {
				err = nil
			}
			return out, err
		}
	}
}
