package main

import (
	"errors"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// resolveAssetURL turns the canonical Edit's `asset.src` strings
// into URLs ffmpeg can fetch. Accepts three shapes:
//
//   "storage:<id>"     → mint a signed URL via storage.files_get_url
//   "mediastudio:<id>" → look up the media-studio generations row,
//                        return its first storage URL (delegates to
//                        media-studio which already wraps storage)
//   "http(s)://…"      → pass-through
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
