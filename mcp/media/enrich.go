package main

// Auto-enrichment of media tool responses with storage-side metadata
// (URL, name, folder, visibility, size, content-type) so an agent
// bound to media alone never needs to chain a separate storage call.
// Single batch round-trip per response, regardless of row count.
//
// Internal MediaRow stays the indexer's source of truth — write
// schema unchanged, no new columns. Enrichment lives at the handler
// boundary: pull rows from media's DB, collect file ids, batch-fetch
// from storage, merge into a separate MediaResponseRow output type
// that embeds the original.
//
// Failure modes:
//   - storage unreachable: enrichment returns empty map, handlers
//     return un-enriched rows + a `storage_unavailable: true` flag
//     so agents can distinguish "no URL because broken" from
//     "no URL because deleted".
//   - file deleted from storage between probe + tool call:
//     enrichment map has no entry; URL stays "" on the output row.
//     Graceful degrade — the row still carries probe metadata,
//     description, and the file_id for any followup call.

import (
	"context"
	"strconv"
)

// MediaResponseRow is the wire shape every enriched media tool
// returns. Embeds MediaRow (so existing fields keep their JSON tags
// at the top level) and adds storage-derived fields. Derivations are
// replaced with their enriched counterparts.
type MediaResponseRow struct {
	MediaRow
	URL         string `json:"url,omitempty"`
	Name        string `json:"name,omitempty"`
	Folder      string `json:"folder,omitempty"`
	Visibility  string `json:"visibility,omitempty"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	// Derivations carries enriched DerivationRows. Re-tagged with
	// the same JSON name as MediaRow.Derivations so it overrides
	// the embedded field's serialization.
	Derivations []EnrichedDerivation `json:"derivations,omitempty"`
}

// EnrichedDerivation is DerivationRow + its storage-side URL.
type EnrichedDerivation struct {
	DerivationRow
	URL         string `json:"url,omitempty"`
	Name        string `json:"name,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// EnrichedRender is RenderRow + the storage URL of the output once
// the render reaches status=ok. URL stays empty for pending/running/
// failed/cancelled rows.
type EnrichedRender struct {
	RenderRow
	URL         string `json:"url,omitempty"`
	Name        string `json:"name,omitempty"`
	Visibility  string `json:"visibility,omitempty"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// enrichRender merges storage metadata onto one RenderRow.
func enrichRender(r RenderRow, files map[string]*StorageFile) EnrichedRender {
	out := EnrichedRender{RenderRow: r}
	if r.OutputFileID == "" {
		return out
	}
	if f := files[r.OutputFileID]; f != nil {
		out.URL = f.URL
		out.Name = f.Name
		out.Visibility = f.Visibility
		out.SizeBytes = f.SizeBytes
		out.ContentType = f.ContentType
	}
	return out
}

// enrichRows turns []MediaRow into []MediaResponseRow with one batch
// storage round-trip. Returns the map of (file_id → StorageFile) used
// for the merge so callers can apply it to one-off shapes (single-row
// tools, derivation-only tools) without re-fetching.
//
// projectID is the install's project; ids are collected from rows AND
// their derivations' StorageFileIDs (so thumbnail/waveform URLs are
// resolved in the same call).
func enrichRows(ctx context.Context, projectID string, rows []MediaRow) ([]MediaResponseRow, map[string]*StorageFile, error) {
	ids := collectFileIDs(rows)
	files, err := newStorageClient().ResolveFiles(ctx, projectID, ids)
	if err != nil {
		return nil, nil, err
	}
	out := make([]MediaResponseRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, mergeRow(r, files))
	}
	return out, files, nil
}

// mergeRow applies the storage-resolved metadata to one MediaRow,
// producing a MediaResponseRow with URLs populated. Missing entries
// in `files` (deleted, unreachable) leave URL/Name empty — the row
// still ships with everything else.
func mergeRow(r MediaRow, files map[string]*StorageFile) MediaResponseRow {
	out := MediaResponseRow{MediaRow: r}
	if f := files[r.FileID]; f != nil {
		out.URL = f.URL
		out.Name = f.Name
		out.Folder = f.Folder
		out.Visibility = f.Visibility
		out.SizeBytes = f.SizeBytes
		out.ContentType = f.ContentType
	}
	if len(r.Derivations) > 0 {
		out.Derivations = make([]EnrichedDerivation, len(r.Derivations))
		for i, d := range r.Derivations {
			out.Derivations[i] = enrichDerivation(d, files)
		}
	}
	return out
}

func enrichDerivation(d DerivationRow, files map[string]*StorageFile) EnrichedDerivation {
	out := EnrichedDerivation{DerivationRow: d}
	if f := files[d.StorageFileID]; f != nil {
		out.URL = f.URL
		out.Name = f.Name
		out.ContentType = f.ContentType
	}
	return out
}

// collectFileIDs gathers every storage id touched by a result set —
// the rows themselves plus their derivations' StorageFileIDs. Dedups
// so the batch call doesn't waste requests on shared ids (a row + its
// thumbnail derivation point to different storage files; rare to
// dedupe but cheap to run).
func collectFileIDs(rows []MediaRow) []string {
	seen := make(map[string]struct{}, len(rows)*2)
	out := make([]string, 0, len(rows)*2)
	add := func(id string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, r := range rows {
		add(r.FileID)
		for _, d := range r.Derivations {
			add(d.StorageFileID)
		}
	}
	return out
}

// idStrFromInt64 — small helper for handlers that have an int64 id.
func idStrFromInt64(id int64) string {
	return strconv.FormatInt(id, 10)
}
