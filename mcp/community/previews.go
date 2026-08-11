package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// PublicLessonPreviewSummary is the safe, cacheable preview index embedded in
// public products. Lesson bodies and signed video URLs are fetched only when a
// visitor explicitly opens a preview.
type PublicLessonPreviewSummary struct {
	ID                   string `json:"id"`
	Title                string `json:"title"`
	SectionTitle         string `json:"section_title"`
	VideoDurationSeconds *int64 `json:"video_duration_seconds,omitempty"`
}

type PublicLessonPreviewVideo struct {
	URL             string `json:"url"`
	ExpiresAt       int64  `json:"expires_at"`
	DurationSeconds *int64 `json:"duration_seconds,omitempty"`
}

// PublicLessonPreview deliberately contains no resources, quizzes,
// assignments, comments, progress, member data, or raw Storage identifiers.
type PublicLessonPreview struct {
	ID           string                    `json:"id"`
	Title        string                    `json:"title"`
	Body         string                    `json:"body"`
	SectionTitle string                    `json:"section_title"`
	Course       PublicCourseIdentity      `json:"course"`
	Video        *PublicLessonPreviewVideo `json:"video,omitempty"`
}

type PublicCourseIdentity struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func (a *App) httpPortalLessonPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/portal/previews/"), "/")
	if id == "" || strings.Contains(id, "/") {
		writeErr(w, http.StatusBadRequest, "preview lesson id is required")
		return
	}
	community, err := publicPortalCommunity(r)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	preview, videoStorageKey, duration, err := loadPublicLessonPreview(globalCtx.AppDB(), community.ID, id)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	if videoStorageKey != "" {
		preview.Video = mintPublicLessonPreviewVideo(globalCtx, videoStorageKey, duration)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"preview": preview})
}

func loadPublicLessonPreviewSummaries(db *sql.DB, spaceID string) ([]PublicLessonPreviewSummary, error) {
	rows, err := db.Query(`SELECT l.id,l.title,s.title,l.video_duration_seconds
		FROM lessons l JOIN sections s ON s.id=l.section_id
		WHERE s.space_id=? AND l.preview_enabled=1 AND l.published_at IS NOT NULL
		ORDER BY s.position,l.position,l.id`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PublicLessonPreviewSummary{}
	for rows.Next() {
		var item PublicLessonPreviewSummary
		var duration sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Title, &item.SectionTitle, &duration); err != nil {
			return nil, err
		}
		if duration.Valid {
			value := duration.Int64
			item.VideoDurationSeconds = &value
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func loadPublicLessonPreview(db *sql.DB, communityID, lessonID string) (PublicLessonPreview, string, *int64, error) {
	var preview PublicLessonPreview
	var videoStorageKey sql.NullString
	var duration sql.NullInt64
	err := db.QueryRow(`SELECT l.id,l.title,l.body,s.title,sp.slug,sp.name,
		l.video_storage_key,l.video_duration_seconds
		FROM lessons l
		JOIN sections s ON s.id=l.section_id
		JOIN spaces sp ON sp.id=s.space_id
		WHERE l.id=? AND sp.community_id=? AND sp.kind='course'
		AND sp.archived_at IS NULL AND l.preview_enabled=1 AND l.published_at IS NOT NULL`,
		lessonID, communityID,
	).Scan(&preview.ID, &preview.Title, &preview.Body, &preview.SectionTitle,
		&preview.Course.Slug, &preview.Course.Name, &videoStorageKey, &duration)
	if errors.Is(err, sql.ErrNoRows) {
		return preview, "", nil, fmt.Errorf("preview lesson not found")
	}
	if err != nil {
		return preview, "", nil, err
	}
	var durationValue *int64
	if duration.Valid {
		value := duration.Int64
		durationValue = &value
	}
	return preview, videoStorageKey.String, durationValue, nil
}

func mintPublicLessonPreviewVideo(ctx *sdk.AppCtx, storageKey string, duration *int64) *PublicLessonPreviewVideo {
	fileID, err := strconv.ParseInt(strings.TrimSpace(storageKey), 10, 64)
	if err != nil || fileID <= 0 {
		return nil
	}
	var out struct {
		URL       string `json:"url"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := callAppResult(ctx, "storage", "files_get_url", map[string]any{
		"id": fileID, "ttl_seconds": 3600, "disposition": "inline", "delivery": "apteva",
	}, &out); err != nil || strings.TrimSpace(out.URL) == "" {
		return nil
	}
	return &PublicLessonPreviewVideo{URL: out.URL, ExpiresAt: out.ExpiresAt, DurationSeconds: duration}
}
