package main

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
)

// Sidecars are generated before pipeline tests and sealing. Serving and asset
// retention never change the sealed artifact tree.
func prepareStaticDelivery(ctx context.Context, framework, dist, startCmd string, log io.Writer) error {
	root := staticDeliveryRoot(framework, dist, startCmd)
	if root == "" {
		return nil
	}
	count := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.Type().IsRegular() || d.Name() == artifactManifestFilename || !compressibleStaticFile(path) {
			return nil
		}
		for _, encoding := range []string{"br", "gzip"} {
			if err := compressStaticFile(ctx, path, encoding); err != nil {
				return err
			}
		}
		count++
		return nil
	})
	if err != nil {
		return fmt.Errorf("precompress static assets: %w", err)
	}
	fmt.Fprintf(log, "precompressed %d static files (Brotli and gzip)\n", count)
	return nil
}

func staticDeliveryRoot(framework, dist, startCmd string) string {
	if framework == "static" {
		return dist
	} else if startCmd == "" && (framework == "bun" || framework == "node") {
		if sub := detectStaticOutput(dist); sub != "" {
			return filepath.Join(dist, sub)
		}
	}
	return ""
}

func compressibleStaticFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".html", ".htm", ".js", ".mjs", ".cjs", ".css", ".json", ".svg", ".xml", ".txt", ".wasm", ".map":
		return true
	}
	return false
}

func compressStaticFile(ctx context.Context, path, encoding string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(path), ".deploy-compress-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	var writer io.WriteCloser
	suffix := ".br"
	if encoding == "gzip" {
		suffix = ".gz"
		writer, err = gzip.NewWriterLevel(out, gzip.BestCompression)
	} else {
		writer = brotli.NewWriterLevel(out, 9)
	}
	if err != nil {
		return err
	}
	buf := make([]byte, 64*1024)
	for {
		if err = ctx.Err(); err != nil {
			break
		}
		var n int
		n, err = in.Read(buf)
		if n > 0 {
			if _, writeErr := writer.Write(buf[:n]); writeErr != nil {
				err = writeErr
				break
			}
		}
		if err == io.EOF {
			err = nil
			break
		}
		if err != nil {
			break
		}
	}
	closeErr := writer.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = out.Chmod(0644); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	return os.Rename(out.Name(), path+suffix)
}

var staticHashToken = regexp.MustCompile(`^[A-Za-z0-9_\-]{8,64}$`)

// Recognize common webpack, Vite, Next and Bun content hashes, conservatively:
// ordinary descriptive filenames must continue to revalidate.
func hashedStaticAsset(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".js", ".mjs", ".css", ".map", ".wasm", ".svg", ".png", ".jpg", ".jpeg", ".webp", ".avif", ".gif", ".ico", ".woff", ".woff2", ".ttf", ".otf":
	default:
		return false
	}
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if ext == ".map" {
		stem = strings.TrimSuffix(stem, filepath.Ext(stem))
	}
	type candidate struct {
		token string
		named bool
	}
	var tokens []candidate
	for index, component := range strings.Split(stem, ".") {
		if !strings.Contains(component, "-") {
			tokens = append(tokens, candidate{component, index > 0})
		}
		for i, char := range component {
			if char == '-' {
				tokens = append(tokens, candidate{component[i+1:], true})
			}
		}
	}
	for _, item := range tokens {
		token := item.token
		if !staticHashToken.MatchString(token) {
			continue
		}
		hex, lower, upper, digit := true, false, false, false
		for _, c := range token {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				hex = false
			}
			lower = lower || c >= 'a' && c <= 'z'
			upper = upper || c >= 'A' && c <= 'Z'
			digit = digit || c >= '0' && c <= '9'
		}
		// Base64/base36 suffixes are short in common bundler output. Requiring
		// a named suffix and mixed case for embedded '-' avoids treating font
		// names such as Inter-latin-400-normal as immutable content hashes.
		if hex || item.named && len(token) <= 12 && (!strings.Contains(token, "-") || lower && upper) && (digit && (lower || upper) || lower && upper) {
			return true
		}
	}
	return false
}

func staticHandler(root string) http.HandlerFunc { return staticHandlerWithCache(root, "") }

func staticHandlerWithCache(root, cache string) http.HandlerFunc {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}
	var etags sync.Map
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rel := strings.TrimPrefix(filepath.Clean("/"+r.URL.Path), string(filepath.Separator))
		if p, ok := staticFileWithin(absRoot, rel); ok {
			serveStaticFile(w, r, p, &etags)
			return
		}
		if p, ok := staticFileWithin(absRoot, filepath.Join(rel, "index.html")); ok {
			if rel != "." && rel != "" && !strings.HasSuffix(r.URL.Path, "/") {
				target := &url.URL{Path: "/" + strings.TrimLeft(r.URL.Path, "/") + "/", RawQuery: r.URL.RawQuery}
				http.Redirect(w, r, target.RequestURI(), http.StatusMovedPermanently)
				return
			}
			serveStaticFile(w, r, p, &etags)
			return
		}
		if hashedStaticAsset(rel) && cache != "" {
			for _, generation := range staticAssetGenerations(cache) {
				if p, ok := staticFileWithin(generation.path, rel); ok {
					serveStaticFile(w, r, p, &etags)
					return
				}
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		// Missing files and asset-directory requests must never become SPA HTML.
		assetRequest := filepath.Ext(rel) != "" && !strings.EqualFold(filepath.Ext(rel), ".html")
		for _, segment := range strings.Split(filepath.ToSlash(rel), "/") {
			if segment == "assets" || segment == "_next" || segment == "static" {
				assetRequest = true
			}
		}
		if !assetRequest {
			if p, ok := staticFileWithin(absRoot, "404.html"); ok {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusNotFound)
				if r.Method != http.MethodHead {
					if body, err := os.ReadFile(p); err == nil {
						_, _ = w.Write(body)
					}
				}
				return
			}
			if p, ok := staticFileWithin(absRoot, "index.html"); ok {
				serveStaticFile(w, r, p, &etags)
				return
			}
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
		} else {
			http.NotFound(w, r)
		}
	}
}

// Resolve symlinks as well as lexical traversal before opening a file. Internal
// symlinks remain supported; links out of the release/cache never get served.
func staticFileWithin(root, rel string) (string, bool) {
	absRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	logical := filepath.Join(absRoot, rel)
	p, err := filepath.EvalSymlinks(logical)
	if err != nil {
		return "", false
	}
	relative, err := filepath.Rel(absRoot, p)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	info, err := os.Stat(p)
	return logical, err == nil && info.Mode().IsRegular()
}

func staticEncoding(header string, br, gz bool) (string, bool) {
	q := map[string]float64{}
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		name := strings.ToLower(strings.TrimSpace(fields[0]))
		if name == "" {
			continue
		}
		quality := 1.0
		for _, field := range fields[1:] {
			key, value, found := strings.Cut(strings.TrimSpace(field), "=")
			if found && strings.EqualFold(key, "q") {
				parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil || !(parsed >= 0 && parsed <= 1) {
					quality = 0
				} else {
					quality = parsed
				}
			}
		}
		q[name] = quality
	}
	quality := func(name string) float64 {
		if v, ok := q[name]; ok {
			return v
		}
		if v, ok := q["*"]; ok {
			return v
		}
		return 0
	}
	best, score := "", 0.0
	for _, option := range []struct {
		name      string
		available bool
	}{{"br", br}, {"gzip", gz}} {
		if v := quality(option.name); option.available && v > score {
			best, score = option.name, v
		}
	}
	identity, explicit := q["identity"]
	if !explicit {
		identity = 1
		if q["*"] == 0 {
			if _, ok := q["*"]; ok {
				identity = 0
			}
		}
	}
	// Implicit identity is an acceptable fallback, rather than overriding the
	// client's explicitly requested compressed representations.
	if explicit && identity > score {
		return "", true
	}
	if score > 0 {
		return best, true
	}
	return "", identity > 0
}

func serveStaticFile(w http.ResponseWriter, r *http.Request, original string, etags *sync.Map) {
	if hashedStaticAsset(original) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	// Compression sidecars must stay within the same directory, even if supplied
	// by a custom build. Never follow an external sidecar symlink.
	brPath, br := staticFileWithin(filepath.Dir(original), filepath.Base(original)+".br")
	gzPath, gz := staticFileWithin(filepath.Dir(original), filepath.Base(original)+".gz")
	w.Header().Add("Vary", "Accept-Encoding")
	encoding, acceptable := staticEncoding(strings.Join(r.Header.Values("Accept-Encoding"), ","), br, gz)
	if !acceptable {
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusNotAcceptable)
		return
	}
	path := original
	if encoding == "br" {
		path = brPath
	} else if encoding == "gzip" {
		path = gzPath
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	key := fmt.Sprintf("%s:%d:%d", path, info.Size(), info.ModTime().UnixNano())
	etag, ok := etags.Load(key)
	if !ok {
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			http.Error(w, "read static asset", http.StatusInternalServerError)
			return
		}
		etag = fmt.Sprintf(`"%x"`, h.Sum(nil))
		etags.Store(key, etag)
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			http.Error(w, "read static asset", http.StatusInternalServerError)
			return
		}
	}
	contentType := mime.TypeByExtension(filepath.Ext(original))
	if contentType == "" {
		sample, err := os.Open(original)
		if err == nil {
			buf := make([]byte, 512)
			n, _ := sample.Read(buf)
			sample.Close()
			contentType = http.DetectContentType(buf[:n])
		}
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("ETag", etag.(string))
	if encoding != "" {
		w.Header().Set("Content-Encoding", encoding)
	}
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	http.ServeContent(w, r, filepath.Base(original), info.ModTime(), f)
}

const retainedStaticGenerations = 6 // current plus five previous release snapshots
const staticAssetMaxAge = 30 * 24 * time.Hour

var staticAssetRetentionMu sync.Mutex

type staticAssetGeneration struct {
	path     string
	modified time.Time
}

func staticAssetGenerations(cache string) []staticAssetGeneration {
	entries, _ := os.ReadDir(cache)
	var out []staticAssetGeneration
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.ParseInt(entry.Name(), 10, 64); err != nil {
			continue
		}
		if info, err := entry.Info(); err == nil {
			out = append(out, staticAssetGeneration{filepath.Join(cache, entry.Name()), info.ModTime()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].modified.After(out[j].modified) })
	return out
}

func retainStaticAssets(root, cache string, releaseID int64, now time.Time) error {
	staticAssetRetentionMu.Lock()
	defer staticAssetRetentionMu.Unlock()
	if err := os.MkdirAll(cache, 0700); err != nil {
		return err
	}
	generation := filepath.Join(cache, strconv.FormatInt(releaseID, 10))
	if _, err := os.Stat(generation); os.IsNotExist(err) {
		tmp, err := os.MkdirTemp(cache, ".snapshot-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !d.Type().IsRegular() || !hashedStaticAsset(path) {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			for _, suffix := range []string{"", ".br", ".gz"} {
				source, ok := staticFileWithin(root, rel+suffix)
				if !ok {
					if suffix == "" {
						return fmt.Errorf("unsafe static asset %s", rel)
					}
					continue
				}
				if err := copyStaticAsset(source, filepath.Join(tmp, rel+suffix)); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if err := os.Rename(tmp, generation); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	// Rollbacks/restarts refresh the active snapshot's retention position.
	if err := os.Chtimes(generation, now, now); err != nil {
		return err
	}
	for i, item := range staticAssetGenerations(cache) {
		if item.path != generation && (i >= retainedStaticGenerations || now.Sub(item.modified) > staticAssetMaxAge) {
			if err := os.RemoveAll(item.path); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyStaticAsset(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
