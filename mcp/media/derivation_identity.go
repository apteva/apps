package main

// Storage file IDs are cross-app references, not sufficient proof of
// ownership. A legacy Storage database could reuse the ROWID of a deleted
// derivation for a new user file. Every destructive or analytical Media path
// therefore resolves IDs in one batch and verifies the hidden folder,
// source-bound filename, content type, and (when present) source marker.

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func validateDerivationStorageFile(d DerivationRow, f *StorageFile) error {
	if f == nil {
		return fmt.Errorf("storage file is missing")
	}
	if d.FileID == "" || d.Kind == "" {
		return fmt.Errorf("derivation identity is incomplete")
	}
	expectedFolder := "/.media/" + d.Kind + "/"
	if f.Folder != expectedFolder {
		return fmt.Errorf("folder %q does not match %q", f.Folder, expectedFolder)
	}
	expectedStem := d.FileID
	if d.Kind == "keyframe" {
		if d.PositionMs <= 0 {
			return fmt.Errorf("keyframe position is invalid")
		}
		expectedStem = fmt.Sprintf("%s-%d", d.FileID, d.PositionMs)
	}
	ext := filepath.Ext(f.Name)
	if ext == "" || strings.TrimSuffix(f.Name, ext) != expectedStem {
		return fmt.Errorf("name %q does not belong to source %s", f.Name, d.FileID)
	}
	if !strings.HasPrefix(strings.ToLower(f.ContentType), "image/") {
		return fmt.Errorf("content type %q is not an image derivation", f.ContentType)
	}
	// Older remote derivations were uploaded through multipart and were
	// labelled "human" (or left empty). Exact hidden folder + source-bound
	// name remain authoritative for those rows. Explicit ownership by another
	// app/path, such as media-render, is always rejected.
	if f.Source != "" && f.Source != "human" && f.Source != "media-derivation" {
		return fmt.Errorf("source %q is not a media derivation", f.Source)
	}
	return nil
}

func derivationStorageIDs(derivs []DerivationRow) []string {
	seen := make(map[string]struct{}, len(derivs))
	ids := make([]string, 0, len(derivs))
	for _, d := range derivs {
		id, err := strconv.ParseInt(d.StorageFileID, 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		key := strconv.FormatInt(id, 10)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ids = append(ids, key)
	}
	return ids
}

func resolveValidDerivations(
	ctx context.Context,
	sc *storageClient,
	projectID string,
	derivs []DerivationRow,
) ([]DerivationRow, error) {
	if len(derivs) == 0 {
		return nil, nil
	}
	if sc == nil {
		return nil, fmt.Errorf("storage client is unavailable")
	}
	ids := derivationStorageIDs(derivs)
	if len(ids) == 0 {
		return nil, nil
	}
	resolved, err := sc.ResolveFiles(ctx, projectID, ids)
	if err != nil {
		return nil, fmt.Errorf("resolve derivation identities: %w", err)
	}
	return filterResolvedDerivations(derivs, resolved), nil
}

func filterResolvedDerivations(derivs []DerivationRow, resolved map[string]*StorageFile) []DerivationRow {
	out := make([]DerivationRow, 0, len(derivs))
	for _, d := range derivs {
		if err := validateDerivationStorageFile(d, resolved[d.StorageFileID]); err == nil {
			out = append(out, d)
		}
	}
	return out
}

func deleteOwnedDerivationFiles(
	ctx context.Context,
	app *sdk.AppCtx,
	sc *storageClient,
	projectID string,
	derivs []DerivationRow,
) {
	if sc == nil || len(derivs) == 0 {
		return
	}
	ids := derivationStorageIDs(derivs)
	if len(ids) == 0 {
		return
	}
	resolved, err := sc.ResolveFiles(ctx, projectID, ids)
	if err != nil {
		if app != nil {
			app.Logger().Warn("skip derivation blob deletion: ownership lookup failed", "err", err)
		}
		return
	}
	deleted := make(map[int64]struct{}, len(derivs))
	for _, d := range derivs {
		f := resolved[d.StorageFileID]
		if err := validateDerivationStorageFile(d, f); err != nil {
			if app != nil {
				app.Logger().Warn("skip unowned derivation storage id",
					"file_id", d.FileID, "kind", d.Kind,
					"storage_file_id", d.StorageFileID, "reason", err.Error())
			}
			continue
		}
		if _, ok := deleted[f.ID]; ok {
			continue
		}
		if err := sc.DeleteFile(ctx, projectID, f.ID); err != nil {
			if app != nil {
				app.Logger().Warn("delete owned derivation failed",
					"file_id", d.FileID, "kind", d.Kind,
					"storage_file_id", f.ID, "err", err)
			}
			continue
		}
		deleted[f.ID] = struct{}{}
	}
}
