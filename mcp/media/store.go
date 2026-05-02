package main

// SQLite reads/writes for the media + derivations tables. Every
// query is project-scoped — the indexer is single-tenant per
// install, but a future global-scope deploy will rely on this.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MediaRow is the canonical shape returned by reads. JSON tags drive
// what /media and media_get expose; raw_probe stays a string so
// callers that want it pretty-print client-side.
type MediaRow struct {
	FileID       string  `json:"file_id"`
	ProjectID    string  `json:"project_id"`
	SourceSHA256 string  `json:"source_sha256"`

	FormatName string `json:"format_name,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Bitrate    int64  `json:"bitrate,omitempty"`

	HasVideo bool `json:"has_video"`
	HasAudio bool `json:"has_audio"`
	IsImage  bool `json:"is_image"`

	Width      int     `json:"width,omitempty"`
	Height     int     `json:"height,omitempty"`
	FPS        float64 `json:"fps,omitempty"`
	VideoCodec string  `json:"video_codec,omitempty"`

	Channels   int    `json:"channels,omitempty"`
	SampleRate int    `json:"sample_rate,omitempty"`
	AudioCodec string `json:"audio_codec,omitempty"`

	ProbeStatus string `json:"probe_status"`
	ProbeError  string `json:"probe_error,omitempty"`
	ProbeAt     string `json:"probe_at,omitempty"`

	RawProbe json.RawMessage `json:"raw_probe,omitempty"`

	// User/agent-supplied prose. Written via media_set_description;
	// never touched by upsertMedia (the indexer's probe upsert) so a
	// reprobe never wipes them.
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	AltText     string `json:"alt_text,omitempty"`

	// Lifted from the transcripts table via LEFT JOIN so the row
	// carries enough state for the panel to render a status icon
	// without a second roundtrip. Empty when no transcript exists.
	TranscriptStatus string `json:"transcript_status,omitempty"`

	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`

	Derivations []DerivationRow `json:"derivations,omitempty"`
}

type DerivationRow struct {
	ID            int64  `json:"id"`
	FileID        string `json:"file_id"`
	Kind          string `json:"kind"`
	StorageFileID string `json:"storage_file_id"`
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
	GeneratedAt   string `json:"generated_at,omitempty"`
}

// upsertMedia INSERT-or-UPDATEs the media row. Source-of-truth fields
// (durations, codecs) are written every time; status fields too.
func upsertMedia(db *sql.DB, projectID string, fileID string, p *Probe, sha string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		INSERT INTO media (
			file_id, project_id, source_sha256,
			format_name, duration_ms, bitrate,
			has_video, has_audio, is_image,
			width, height, fps, video_codec,
			channels, sample_rate, audio_codec,
			probe_status, probe_error, probe_at,
			raw_probe, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'ok', '', ?, ?, ?)
		ON CONFLICT(file_id) DO UPDATE SET
			project_id=excluded.project_id,
			source_sha256=excluded.source_sha256,
			format_name=excluded.format_name,
			duration_ms=excluded.duration_ms,
			bitrate=excluded.bitrate,
			has_video=excluded.has_video,
			has_audio=excluded.has_audio,
			is_image=excluded.is_image,
			width=excluded.width,
			height=excluded.height,
			fps=excluded.fps,
			video_codec=excluded.video_codec,
			channels=excluded.channels,
			sample_rate=excluded.sample_rate,
			audio_codec=excluded.audio_codec,
			probe_status='ok',
			probe_error='',
			probe_at=excluded.probe_at,
			raw_probe=excluded.raw_probe,
			updated_at=excluded.updated_at`,
		fileID, projectID, sha,
		p.FormatName, p.DurationMs, p.Bitrate,
		boolInt(p.HasVideo), boolInt(p.HasAudio), boolInt(p.IsImage),
		nullableInt(p.Width), nullableInt(p.Height), nullableFloat(p.FPS), nullableStr(p.VideoCodec),
		nullableInt(p.Channels), nullableInt(p.SampleRate), nullableStr(p.AudioCodec),
		now, p.Raw, now,
	)
	return err
}

// markFailed flips a row to probe_status='failed' with the message
// trimmed so we don't dump an entire ffmpeg blob into the column.
func markFailed(db *sql.DB, projectID, fileID, sha, kind, msg string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if len(msg) > 1000 {
		msg = msg[:1000] + "…"
	}
	_, err := db.Exec(`
		INSERT INTO media (file_id, project_id, source_sha256, probe_status, probe_error, probe_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(file_id) DO UPDATE SET
			source_sha256=excluded.source_sha256,
			probe_status=excluded.probe_status,
			probe_error=excluded.probe_error,
			probe_at=excluded.probe_at,
			updated_at=excluded.updated_at`,
		fileID, projectID, sha, kind, msg, now, now,
	)
	return err
}

func upsertDerivation(db *sql.DB, projectID, fileID, kind string, storageFileID int64, w, h int) error {
	_, err := db.Exec(`
		INSERT INTO derivations (file_id, project_id, kind, storage_file_id, width, height, status, error)
		VALUES (?, ?, ?, ?, ?, ?, 'ok', '')
		ON CONFLICT(file_id, kind) DO UPDATE SET
			project_id=excluded.project_id,
			storage_file_id=excluded.storage_file_id,
			width=excluded.width,
			height=excluded.height,
			status='ok',
			error='',
			generated_at=CURRENT_TIMESTAMP`,
		fileID, projectID, kind, fmt.Sprintf("%d", storageFileID), nullableInt(w), nullableInt(h),
	)
	return err
}

// getMedia loads one row + its derivations.
// DescriptionFields carries a partial-update payload for a media
// row's prose columns. Pointer types let callers distinguish
// "preserve existing value" from "clear to empty string". Empty-
// string pointers explicitly clear; nil pointers leave the column
// untouched.
type DescriptionFields struct {
	Title       *string
	Description *string
	AltText     *string
}

// setDescription writes only the description columns. Probe state
// (status, codecs, duration, etc.) is never touched here, and the
// indexer's upsertMedia never touches these columns — so a reprobe
// preserves any prose written by the agent.
//
// Returns sql.ErrNoRows when the file_id has no media row yet
// (call media_reindex first, or wait for the indexer to pick up
// the upload). Callers turn that into a 404 / found:false.
func setDescription(db *sql.DB, projectID, fileID string, f DescriptionFields) error {
	// Build a partial UPDATE; sqlite happily accepts an empty SET
	// list when nothing is provided, so guard against that.
	sets := []string{}
	args := []any{}
	if f.Title != nil {
		sets = append(sets, "title = ?")
		args = append(args, *f.Title)
	}
	if f.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, *f.Description)
	}
	if f.AltText != nil {
		sets = append(sets, "alt_text = ?")
		args = append(args, *f.AltText)
	}
	if len(sets) == 0 {
		return nil // nothing to do; not an error
	}
	// Bump updated_at so panels sorting by recently-described work.
	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().UTC().Format(time.RFC3339))

	args = append(args, projectID, fileID)
	q := "UPDATE media SET " + strings.Join(sets, ", ") + " WHERE project_id=? AND file_id=?"
	res, err := db.Exec(q, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func getMedia(db *sql.DB, projectID, fileID string) (*MediaRow, error) {
	row := db.QueryRow(`
		SELECT m.file_id, m.project_id, m.source_sha256, m.format_name, m.duration_ms, m.bitrate,
			m.has_video, m.has_audio, m.is_image,
			m.width, m.height, m.fps, m.video_codec,
			m.channels, m.sample_rate, m.audio_codec,
			m.probe_status, m.probe_error, m.probe_at, m.raw_probe,
			m.title, m.description, m.alt_text,
			COALESCE(t.status, ''),
			m.created_at, m.updated_at
		FROM media m
		LEFT JOIN transcripts t
		  ON t.file_id = m.file_id AND t.project_id = m.project_id
		WHERE m.project_id=? AND m.file_id=?`, projectID, fileID)
	m, err := scanMedia(row)
	if err != nil {
		return nil, err
	}
	m.Derivations, _ = listDerivations(db, projectID, fileID)
	return m, nil
}

func scanMedia(row interface{ Scan(...any) error }) (*MediaRow, error) {
	var (
		m                              MediaRow
		duration, bitrate              sql.NullInt64
		width, height, channels, srate sql.NullInt64
		fps                            sql.NullFloat64
		formatName, vcodec, acodec     sql.NullString
		probeAt, createdAt, updatedAt  sql.NullString
		probeError                     sql.NullString
		hasVideo, hasAudio, isImage    int
		rawProbe                       string
		title, description, altText    string
		transcriptStatus               string
	)
	err := row.Scan(
		&m.FileID, &m.ProjectID, &m.SourceSHA256,
		&formatName, &duration, &bitrate,
		&hasVideo, &hasAudio, &isImage,
		&width, &height, &fps, &vcodec,
		&channels, &srate, &acodec,
		&m.ProbeStatus, &probeError, &probeAt, &rawProbe,
		&title, &description, &altText,
		&transcriptStatus,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	m.HasVideo = hasVideo == 1
	m.HasAudio = hasAudio == 1
	m.IsImage = isImage == 1
	m.FormatName = formatName.String
	m.DurationMs = duration.Int64
	m.Bitrate = bitrate.Int64
	m.Width = int(width.Int64)
	m.Height = int(height.Int64)
	m.FPS = fps.Float64
	m.VideoCodec = vcodec.String
	m.Channels = int(channels.Int64)
	m.SampleRate = int(srate.Int64)
	m.AudioCodec = acodec.String
	m.ProbeError = probeError.String
	m.ProbeAt = probeAt.String
	m.Title = title
	m.Description = description
	m.AltText = altText
	m.TranscriptStatus = transcriptStatus
	m.CreatedAt = createdAt.String
	m.UpdatedAt = updatedAt.String
	if rawProbe != "" {
		m.RawProbe = json.RawMessage(rawProbe)
	}
	return &m, nil
}

// SearchFilters drive media_search and the GET /media handler.
type SearchFilters struct {
	DurationMinMs int64
	DurationMaxMs int64
	HasVideo      *bool
	HasAudio      *bool
	IsImage       *bool
	WidthMin      int
	WidthMax      int
	VideoCodec    string
	AudioCodec    string
	Limit         int
	OrderBy       string // duration_ms | created_at | updated_at
}

// searchMedia returns rows matching f. Joins derivations once at the
// end so each row has its thumbnail/waveform pointers.
func searchMedia(db *sql.DB, projectID string, f SearchFilters) ([]MediaRow, error) {
	clauses := []string{"project_id = ?", "probe_status = 'ok'"}
	args := []any{projectID}

	if f.DurationMinMs > 0 {
		clauses = append(clauses, "duration_ms >= ?")
		args = append(args, f.DurationMinMs)
	}
	if f.DurationMaxMs > 0 {
		clauses = append(clauses, "duration_ms <= ?")
		args = append(args, f.DurationMaxMs)
	}
	if f.HasVideo != nil {
		clauses = append(clauses, "has_video = ?")
		args = append(args, boolInt(*f.HasVideo))
	}
	if f.HasAudio != nil {
		clauses = append(clauses, "has_audio = ?")
		args = append(args, boolInt(*f.HasAudio))
	}
	if f.IsImage != nil {
		clauses = append(clauses, "is_image = ?")
		args = append(args, boolInt(*f.IsImage))
	}
	if f.WidthMin > 0 {
		clauses = append(clauses, "width >= ?")
		args = append(args, f.WidthMin)
	}
	if f.WidthMax > 0 {
		clauses = append(clauses, "width <= ?")
		args = append(args, f.WidthMax)
	}
	if f.VideoCodec != "" {
		clauses = append(clauses, "video_codec = ?")
		args = append(args, f.VideoCodec)
	}
	if f.AudioCodec != "" {
		clauses = append(clauses, "audio_codec = ?")
		args = append(args, f.AudioCodec)
	}

	order := "created_at DESC"
	switch f.OrderBy {
	case "duration_ms":
		order = "duration_ms DESC"
	case "updated_at":
		order = "updated_at DESC"
	case "created_at":
		order = "created_at DESC"
	}

	limit := 50
	if f.Limit > 0 && f.Limit <= 500 {
		limit = f.Limit
	}

	// Project-scope every clause to the m. alias since we now join.
	for i, c := range clauses {
		clauses[i] = strings.ReplaceAll(c, "project_id =", "m.project_id =")
	}
	query := `SELECT m.file_id, m.project_id, m.source_sha256, m.format_name, m.duration_ms, m.bitrate,
		m.has_video, m.has_audio, m.is_image,
		m.width, m.height, m.fps, m.video_codec,
		m.channels, m.sample_rate, m.audio_codec,
		m.probe_status, m.probe_error, m.probe_at, m.raw_probe,
		m.title, m.description, m.alt_text,
		COALESCE(t.status, ''),
		m.created_at, m.updated_at
	FROM media m
	LEFT JOIN transcripts t
	  ON t.file_id = m.file_id AND t.project_id = m.project_id
	WHERE ` + strings.Join(clauses, " AND ") + " ORDER BY m." + order + " LIMIT ?"
	args = append(args, limit)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MediaRow{}
	ids := []string{}
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
		ids = append(ids, m.FileID)
	}
	if len(ids) == 0 {
		return out, nil
	}
	derivByFile, err := listDerivationsByFiles(db, projectID, ids)
	if err == nil {
		for i := range out {
			out[i].Derivations = derivByFile[out[i].FileID]
		}
	}
	return out, nil
}

func listDerivations(db *sql.DB, projectID, fileID string) ([]DerivationRow, error) {
	rows, err := db.Query(
		`SELECT id, file_id, kind, storage_file_id, width, height, status, error, generated_at
		FROM derivations WHERE project_id=? AND file_id=?`, projectID, fileID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DerivationRow
	for rows.Next() {
		var d DerivationRow
		var w, h sql.NullInt64
		var gen sql.NullString
		if err := rows.Scan(&d.ID, &d.FileID, &d.Kind, &d.StorageFileID, &w, &h, &d.Status, &d.Error, &gen); err != nil {
			return nil, err
		}
		d.Width = int(w.Int64)
		d.Height = int(h.Int64)
		d.GeneratedAt = gen.String
		out = append(out, d)
	}
	return out, nil
}

func listDerivationsByFiles(db *sql.DB, projectID string, fileIDs []string) (map[string][]DerivationRow, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(fileIDs))
	placeholders = strings.TrimSuffix(placeholders, ",")
	args := []any{projectID}
	for _, id := range fileIDs {
		args = append(args, id)
	}
	rows, err := db.Query(
		`SELECT id, file_id, kind, storage_file_id, width, height, status, error, generated_at
		FROM derivations WHERE project_id=? AND file_id IN (`+placeholders+`)`, args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]DerivationRow{}
	for rows.Next() {
		var d DerivationRow
		var w, h sql.NullInt64
		var gen sql.NullString
		if err := rows.Scan(&d.ID, &d.FileID, &d.Kind, &d.StorageFileID, &w, &h, &d.Status, &d.Error, &gen); err != nil {
			return nil, err
		}
		d.Width = int(w.Int64)
		d.Height = int(h.Int64)
		d.GeneratedAt = gen.String
		out[d.FileID] = append(out[d.FileID], d)
	}
	return out, nil
}

// indexerCandidates returns file IDs from storage that need probing —
// missing media row, or marked pending/failed (with an exponential
// retry gate so we don't flap).
func indexerCandidates(db *sql.DB, projectID string, all []StorageFile, limit int) []StorageFile {
	if len(all) == 0 {
		return nil
	}
	known := map[string]struct {
		status string
		sha    string
	}{}
	rows, err := db.Query(
		`SELECT file_id, probe_status, source_sha256 FROM media WHERE project_id=?`, projectID,
	)
	if err == nil {
		for rows.Next() {
			var id, st, sh string
			if err := rows.Scan(&id, &st, &sh); err == nil {
				known[id] = struct {
					status string
					sha    string
				}{st, sh}
			}
		}
		rows.Close()
	}
	out := []StorageFile{}
	for _, f := range all {
		fid := fmt.Sprintf("%d", f.ID)
		k, ok := known[fid]
		if !ok {
			out = append(out, f)
			continue
		}
		switch k.status {
		case "ok":
			// Re-probe only if storage's sha has changed.
			if f.SHA256 != k.sha && f.SHA256 != "" {
				out = append(out, f)
			}
		case "pending", "failed":
			out = append(out, f)
		case "skipped_size":
			// Leave alone — only forced reindex retries skipped rows.
		}
		if len(out) >= limit {
			break
		}
	}
	return out
}

// boolInt / nullables — helpers because SQLite + database/sql typing
// gets noisy when half the columns can be NULL.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
func nullableFloat(v float64) any {
	if v == 0 {
		return nil
	}
	return v
}
func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nilOrErr — collapses sql.ErrNoRows so callers can treat "not found"
// as a normal nil-result rather than wrapping every call.
func notFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
