package main

// SQLite reads/writes for the shows + episodes tables. App-sdk v0.19+
// enforces foreign-key + WAL pragmas, so DELETE on a show cascades to
// its episodes without an explicit pragma here.

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var errNotFound = errors.New("not found")

// ─── types ─────────────────────────────────────────────────────────

type Show struct {
	ID          int64  `json:"id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Author      string `json:"author"`
	OwnerEmail  string `json:"owner_email"`
	Language    string `json:"language"`
	Category    string `json:"category"`
	Explicit    bool   `json:"explicit"`
	Link        string `json:"link"`
	PodcastType string `json:"podcast_type"`
	ImageFileID string `json:"image_file_id"`
	Copyright   string `json:"copyright"`
	Hostname    string `json:"hostname"`
	ProjectID   string `json:"project_id"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type Episode struct {
	ID               int64   `json:"id"`
	ShowID           int64   `json:"show_id"`
	GUID             string  `json:"guid"`
	Title            string  `json:"title"`
	Description      string  `json:"description"`
	SeasonNumber     *int64  `json:"season_number,omitempty"`
	EpisodeNumber    *int64  `json:"episode_number,omitempty"`
	EpisodeType      string  `json:"episode_type"`
	Status           string  `json:"status"`
	AudioFileID      string  `json:"audio_file_id"`
	AudioURL         string  `json:"audio_url"`
	AudioBytes       int64   `json:"audio_bytes"`
	DurationSeconds  int64   `json:"duration_seconds"`
	MimeType         string  `json:"mime_type"`
	ImageFileID      string  `json:"image_file_id"`
	TranscriptFileID string  `json:"transcript_file_id"`
	PublishAt        *string `json:"publish_at,omitempty"`
	PublishedAt      *string `json:"published_at,omitempty"`
	Downloads        int64   `json:"downloads"`
	LastDownloadAt   *string `json:"last_download_at,omitempty"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

// ─── shows ─────────────────────────────────────────────────────────

const showCols = `id, slug, title, description, author, owner_email, language,
	category, explicit, link, podcast_type, image_file_id, copyright,
	hostname, project_id, created_at, updated_at`

func scanShow(row interface{ Scan(...any) error }) (*Show, error) {
	var s Show
	err := row.Scan(&s.ID, &s.Slug, &s.Title, &s.Description, &s.Author,
		&s.OwnerEmail, &s.Language, &s.Category, &s.Explicit, &s.Link,
		&s.PodcastType, &s.ImageFileID, &s.Copyright, &s.Hostname,
		&s.ProjectID, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func dbInsertShow(db *sql.DB, args map[string]any, projectID string) (*Show, error) {
	title := strings.TrimSpace(strArg(args, "title"))
	if title == "" {
		return nil, errors.New("title required")
	}
	slug := strings.TrimSpace(strArg(args, "slug"))
	if slug == "" {
		slug = slugify(title)
	}
	language := strArg(args, "language")
	if language == "" {
		language = "en"
	}
	podcastType := strArg(args, "podcast_type")
	if podcastType == "" {
		podcastType = "episodic"
	}
	res, err := db.Exec(`INSERT INTO shows
		(slug, title, description, author, owner_email, language, category,
		 explicit, link, podcast_type, image_file_id, copyright, hostname, project_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		slug, title, strArg(args, "description"), strArg(args, "author"),
		strArg(args, "owner_email"), language, strArg(args, "category"),
		boolArg(args, "explicit"), strArg(args, "link"), podcastType,
		strArg(args, "image_file_id"), strArg(args, "copyright"),
		strings.TrimSpace(strArg(args, "hostname")), projectID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("a show with slug %q already exists in this scope", slug)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetShow(db, id)
}

func dbUpdateShow(db *sql.DB, id int64, args map[string]any) (*Show, error) {
	sets, vals := []string{}, []any{}
	str := func(key, col string) {
		if _, ok := args[key]; ok {
			sets = append(sets, col+"=?")
			vals = append(vals, strArg(args, key))
		}
	}
	str("title", "title")
	str("description", "description")
	str("author", "author")
	str("owner_email", "owner_email")
	str("language", "language")
	str("category", "category")
	str("link", "link")
	str("podcast_type", "podcast_type")
	str("image_file_id", "image_file_id")
	str("copyright", "copyright")
	str("slug", "slug")
	str("hostname", "hostname")
	if _, ok := args["explicit"]; ok {
		sets = append(sets, "explicit=?")
		vals = append(vals, boolArg(args, "explicit"))
	}
	if len(sets) == 0 {
		return dbGetShow(db, id)
	}
	sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, id)
	_, err := db.Exec("UPDATE shows SET "+strings.Join(sets, ", ")+" WHERE id=?", vals...)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, errors.New("that slug is already taken in this scope")
		}
		return nil, err
	}
	return dbGetShow(db, id)
}

func dbGetShow(db *sql.DB, id int64) (*Show, error) {
	return scanShow(db.QueryRow("SELECT "+showCols+" FROM shows WHERE id=?", id))
}

func dbGetShowBySlug(db *sql.DB, slug, projectID string) (*Show, error) {
	return scanShow(db.QueryRow("SELECT "+showCols+" FROM shows WHERE slug=? AND project_id=?", slug, projectID))
}

func dbListShows(db *sql.DB, projectID string, limit, offset int) ([]Show, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Query("SELECT "+showCols+" FROM shows WHERE project_id=? ORDER BY title LIMIT ? OFFSET ?",
		projectID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Show{}
	for rows.Next() {
		s, err := scanShow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func dbDeleteShow(db *sql.DB, id int64) error {
	res, err := db.Exec("DELETE FROM shows WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

// ─── episodes ──────────────────────────────────────────────────────

const epCols = `id, show_id, guid, title, description, season_number,
	episode_number, episode_type, status, audio_file_id, audio_url,
	audio_bytes, duration_seconds, mime_type, image_file_id,
	transcript_file_id, publish_at, published_at, downloads,
	last_download_at, created_at, updated_at`

func scanEpisode(row interface{ Scan(...any) error }) (*Episode, error) {
	var e Episode
	var season, episode sql.NullInt64
	var publishAt, publishedAt, lastDL sql.NullString
	err := row.Scan(&e.ID, &e.ShowID, &e.GUID, &e.Title, &e.Description,
		&season, &episode, &e.EpisodeType, &e.Status, &e.AudioFileID,
		&e.AudioURL, &e.AudioBytes, &e.DurationSeconds, &e.MimeType,
		&e.ImageFileID, &e.TranscriptFileID, &publishAt, &publishedAt,
		&e.Downloads, &lastDL, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if season.Valid {
		e.SeasonNumber = &season.Int64
	}
	if episode.Valid {
		e.EpisodeNumber = &episode.Int64
	}
	if publishAt.Valid {
		e.PublishAt = &publishAt.String
	}
	if publishedAt.Valid {
		e.PublishedAt = &publishedAt.String
	}
	if lastDL.Valid {
		e.LastDownloadAt = &lastDL.String
	}
	return &e, nil
}

func dbInsertEpisode(db *sql.DB, args map[string]any) (*Episode, error) {
	showID := int64Arg(args, "show_id")
	if showID == 0 {
		return nil, errors.New("show_id required")
	}
	if _, err := dbGetShow(db, showID); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, fmt.Errorf("show %d does not exist", showID)
		}
		return nil, err
	}
	title := strings.TrimSpace(strArg(args, "title"))
	if title == "" {
		return nil, errors.New("title required")
	}
	guid := strings.TrimSpace(strArg(args, "guid"))
	if guid == "" {
		guid = "apteva-podcast-" + uuid.NewString()
	}
	epType := strArg(args, "episode_type")
	if epType == "" {
		epType = "full"
	}
	res, err := db.Exec(`INSERT INTO episodes
		(show_id, guid, title, description, season_number, episode_number,
		 episode_type, audio_file_id, image_file_id)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		showID, guid, title, strArg(args, "description"),
		nullableInt(args, "season_number"), nullableInt(args, "episode_number"),
		epType, strArg(args, "audio_file_id"), strArg(args, "image_file_id"))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("an episode with guid %q already exists", guid)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetEpisode(db, id)
}

func dbUpdateEpisode(db *sql.DB, id int64, args map[string]any) (*Episode, error) {
	sets, vals := []string{}, []any{}
	str := func(key, col string) {
		if _, ok := args[key]; ok {
			sets = append(sets, col+"=?")
			vals = append(vals, strArg(args, key))
		}
	}
	str("title", "title")
	str("description", "description")
	str("episode_type", "episode_type")
	str("image_file_id", "image_file_id")
	for _, key := range []string{"season_number", "episode_number"} {
		if _, ok := args[key]; ok {
			sets = append(sets, key+"=?")
			vals = append(vals, nullableInt(args, key))
		}
	}
	if len(sets) == 0 {
		return dbGetEpisode(db, id)
	}
	sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, id)
	if _, err := db.Exec("UPDATE episodes SET "+strings.Join(sets, ", ")+" WHERE id=?", vals...); err != nil {
		return nil, err
	}
	return dbGetEpisode(db, id)
}

func dbGetEpisode(db *sql.DB, id int64) (*Episode, error) {
	return scanEpisode(db.QueryRow("SELECT "+epCols+" FROM episodes WHERE id=?", id))
}

func dbGetEpisodeByGUID(db *sql.DB, guid string) (*Episode, error) {
	return scanEpisode(db.QueryRow("SELECT "+epCols+" FROM episodes WHERE guid=?", guid))
}

func dbListEpisodes(db *sql.DB, showID int64, status string, limit, offset int) ([]Episode, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	where, conds, vals := "", []string{}, []any{}
	if showID != 0 {
		conds = append(conds, "show_id=?")
		vals = append(vals, showID)
	}
	if status != "" {
		conds = append(conds, "status=?")
		vals = append(vals, status)
	}
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	vals = append(vals, limit, offset)
	rows, err := db.Query("SELECT "+epCols+" FROM episodes "+where+
		" ORDER BY COALESCE(published_at, created_at) DESC LIMIT ? OFFSET ?", vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Episode{}
	for rows.Next() {
		e, err := scanEpisode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// dbListPublishedEpisodes returns the feed-visible episodes for a show,
// newest first.
func dbListPublishedEpisodes(db *sql.DB, showID int64) ([]Episode, error) {
	rows, err := db.Query("SELECT "+epCols+" FROM episodes WHERE show_id=? AND status='published'"+
		" ORDER BY COALESCE(published_at, created_at) DESC", showID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Episode{}
	for rows.Next() {
		e, err := scanEpisode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// dbListDueScheduled returns scheduled episodes whose publish_at has
// passed — the scheduled-publish worker's input.
func dbListDueScheduled(db *sql.DB) ([]Episode, error) {
	rows, err := db.Query("SELECT " + epCols + " FROM episodes" +
		" WHERE status='scheduled' AND publish_at IS NOT NULL AND publish_at <= CURRENT_TIMESTAMP")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Episode{}
	for rows.Next() {
		e, err := scanEpisode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func dbDeleteEpisode(db *sql.DB, id int64) error {
	res, err := db.Exec("DELETE FROM episodes WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

// dbSetEpisodeAudio caches the storage + media probe result onto the
// episode so feed generation is a pure read.
func dbSetEpisodeAudio(db *sql.DB, id int64, fileID, url string, bytes, durationSec int64, mime string) error {
	_, err := db.Exec(`UPDATE episodes SET audio_file_id=?, audio_url=?,
		audio_bytes=?, duration_seconds=?, mime_type=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?`, fileID, url, bytes, durationSec, mime, id)
	return err
}

// dbSetEpisodeStatus moves an episode through the lifecycle. publishAt
// is set for 'scheduled'; publishedAt is stamped for 'published'.
func dbSetEpisodeStatus(db *sql.DB, id int64, status string, publishAt, publishedAt *string) error {
	res, err := db.Exec(`UPDATE episodes SET status=?, publish_at=?, published_at=?,
		updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, publishAt, publishedAt, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

func dbBumpDownload(db *sql.DB, id int64) error {
	_, err := db.Exec(`UPDATE episodes SET downloads=downloads+1,
		last_download_at=CURRENT_TIMESTAMP WHERE id=?`, id)
	return err
}

// ─── helpers ───────────────────────────────────────────────────────

var slugStrip = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugStrip.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "show-" + uuid.NewString()[:8]
	}
	if len(s) > 80 {
		s = strings.Trim(s[:80], "-")
	}
	return s
}

// sqliteTime renders a Go time the way CURRENT_TIMESTAMP stores it, so
// scheduled-publish comparisons stay string-comparable.
func sqliteTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05")
}
