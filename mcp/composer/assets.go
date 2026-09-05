package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// resolveAssetURL turns the canonical Edit's `asset.src` strings
// into URLs ffmpeg can fetch. Accepts three shapes:
//
//	"storage:<id>"     → mint a signed URL via storage.files_get_url
//	"mediastudio:<id>" → look up the media-studio generations row,
//	                     return its first storage URL (delegates to
//	                     media-studio which already wraps storage)
//	"http(s)://…"      → pass-through
//
// Any other shape is rejected — the validator should already have
// caught it but defending here avoids ffmpeg's opaque "unable to open
// input" errors.
func resolveAssetURL(app *sdk.AppCtx, src string) (string, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return "", errors.New("empty src")
	}
	switch {
	case strings.HasPrefix(src, "http://"), strings.HasPrefix(src, "https://"):
		return src, nil
	case strings.HasPrefix(src, "storage:"):
		id, err := strconv.ParseInt(src[len("storage:"):], 10, 64)
		if err != nil || id <= 0 {
			return "", errors.New("malformed storage handle: " + src)
		}
		return storageSignedURL(app, id)
	case strings.HasPrefix(src, "mediastudio:"):
		id, err := strconv.ParseInt(src[len("mediastudio:"):], 10, 64)
		if err != nil || id <= 0 {
			return "", errors.New("malformed mediastudio handle: " + src)
		}
		return mediastudioStorageURL(app, id)
	}
	return "", errors.New("unsupported src scheme (want storage:N | mediastudio:N | http(s)): " + src)
}

// resolveAssetLocal turns asset sources into ffmpeg-readable inputs for the
// local executor. Local storage files should be read from disk when possible:
// routing local ffmpeg through a public/signed URL is slower and can fail if the
// tunnel/public route is unhealthy. Remote/SaaS renderers still use
// resolveAssetURL so they receive fetchable URLs.
func resolveAssetLocal(app *sdk.AppCtx, src string) (string, error) {
	src = strings.TrimSpace(src)
	if strings.HasPrefix(src, "storage:") {
		id, err := strconv.ParseInt(src[len("storage:"):], 10, 64)
		if err != nil || id <= 0 {
			return "", errors.New("malformed storage handle: " + src)
		}
		if path, err := storageLocalPath(app, id); err == nil && path != "" {
			return path, nil
		}
	}
	return resolveAssetURL(app, src)
}

// storageSignedURL asks storage for a time-limited URL ffmpeg can GET.
// Storage's files_get_url returns {url, expires_at, …}; we only need
// the URL.
func storageSignedURL(app *sdk.AppCtx, id int64) (string, error) {
	var got struct {
		URL string `json:"url"`
	}
	err := app.PlatformAPI().CallAppResult("storage", "files_get_url",
		map[string]any{"id": id, "ttl_seconds": 3600}, &got)
	if err != nil {
		return "", err
	}
	if got.URL == "" {
		return "", errors.New("storage returned empty url for id " + strconv.FormatInt(id, 10))
	}
	return got.URL, nil
}

func storageLocalPath(app *sdk.AppCtx, id int64) (string, error) {
	var got struct {
		Found bool `json:"found"`
		File  *struct {
			StorageKey string `json:"storage_key"`
		} `json:"file"`
	}
	if err := app.PlatformAPI().CallAppResult("storage", "files_get", map[string]any{"id": id}, &got); err != nil {
		return "", err
	}
	if !got.Found || got.File == nil {
		return "", errors.New("storage file not found for id " + strconv.FormatInt(id, 10))
	}
	if got.File.StorageKey == "" {
		return "", errors.New("storage returned empty storage_key for id " + strconv.FormatInt(id, 10))
	}
	return storageLocalPathForKey(app.DataDir(), got.File.StorageKey)
}

func storageLocalPathForKey(appDataDir, storageKey string) (string, error) {
	storageKey = filepath.Base(strings.TrimSpace(storageKey))
	if storageKey == "" || storageKey == "." || storageKey == string(filepath.Separator) {
		return "", errors.New("empty storage key")
	}
	for _, root := range candidateStorageDataRoots(appDataDir) {
		patterns := []string{
			filepath.Join(root, "*", "storage-blobs", "*", storageKey),
			filepath.Join(root, "storage-blobs", "*", storageKey),
		}
		for _, pattern := range patterns {
			matches, _ := filepath.Glob(pattern)
			for _, match := range matches {
				if st, err := os.Stat(match); err == nil && !st.IsDir() {
					return match, nil
				}
			}
		}
	}
	return "", errors.New("local storage blob not found for key " + storageKey)
}

func candidateStorageDataRoots(appDataDir string) []string {
	seen := map[string]bool{}
	var roots []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		roots = append(roots, path)
	}
	add(os.Getenv("APTEVA_STORAGE_DATA_DIR"))
	if appDataDir != "" {
		// App data dirs are <apteva-home>/apps/<app-name>/data/<install-id>.
		// The sibling Storage app keeps its data under
		// <apteva-home>/apps/storage/data/<install-id>.
		appsRoot := filepath.Dir(filepath.Dir(filepath.Dir(appDataDir)))
		add(filepath.Join(appsRoot, "storage", "data"))
	}
	return roots
}

// mediastudioStorageURL fetches a media-studio generations row and
// returns its first storage URL. Avoids re-implementing media-studio's
// storage indirection.
func mediastudioStorageURL(app *sdk.AppCtx, genID int64) (string, error) {
	var got struct {
		Generation struct {
			StorageURLs []string `json:"storage_urls"`
		} `json:"generation"`
	}
	// media-studio doesn't have a single-row read tool today; pull
	// the recent history and find by id. Cheap because the history
	// query is paged + we filter client-side.
	var listing struct {
		Generations []struct {
			ID          int64    `json:"id"`
			StorageURLs []string `json:"storage_urls"`
		} `json:"generations"`
	}
	err := app.PlatformAPI().CallAppResult("media-studio", "media_history",
		map[string]any{"limit": 200}, &listing)
	if err != nil {
		return "", err
	}
	for _, g := range listing.Generations {
		if g.ID == genID {
			if len(g.StorageURLs) == 0 {
				return "", errors.New("mediastudio row has no storage URL (storage may be unbound)")
			}
			return g.StorageURLs[0], nil
		}
	}
	_ = got
	return "", errors.New("mediastudio generation not found in last 200 rows")
}
