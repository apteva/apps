package main

import (
	"errors"
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
	rootReal, err := filepath.EvalSymlinks(s.root)
	if err != nil {
		return nil, err
	}
	clean := path.Clean("/" + name)
	full := filepath.Join(s.root, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return nil, err
	}
	if real != rootReal && !strings.HasPrefix(real, rootReal+string(filepath.Separator)) {
		return nil, fs.ErrPermission
	}
	file, err := os.Open(real)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fs.ErrNotExist
	}
	return file, err
}
