package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type searchCursor struct {
	Date      string `json:"date"`
	CreatedAt string `json:"created_at,omitempty"`
	ID        string `json:"id"`
}

type searchPage[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type searchUse struct {
	PublicationID  string `json:"publication_id"`
	Destination    string `json:"destination"`
	AccountRef     string `json:"account_ref"`
	Status         string `json:"status"`
	ExternalPostID string `json:"external_post_id"`
	ExternalURL    string `json:"external_url"`
	ActualAt       string `json:"actual_at"`
}

type assetSearchHit struct {
	Asset
	BrandID      string      `json:"brand_id"`
	SessionTitle string      `json:"session_title"`
	SessionDate  string      `json:"session_date"`
	AttachedAt   string      `json:"attached_at"`
	IsDerivative bool        `json:"is_derivative"`
	Uses         []searchUse `json:"uses"`
	MatchReason  string      `json:"match_reason"`
}

type sessionSearchHit struct {
	Session
	BrandName  string `json:"brand_name"`
	AssetCount int    `json:"asset_count"`
}

type releaseSearchHit struct {
	Release
	BrandName   string `json:"brand_name"`
	TargetCount int    `json:"target_count"`
	SortDate    string `json:"sort_date"`
}

type searchOptions struct {
	ProjectID, EntityType, Query, BrandID, SessionID, DateFrom, DateTo       string
	Kind, Lineage, Sort, ReviewStatus, Destination, AccountRef, Availability string
	Limit                                                                    int
}

func parseSearchOptions(pid string, args map[string]any) (searchOptions, error) {
	o := searchOptions{ProjectID: pid, EntityType: str(args, "entity_type"), Query: strings.TrimSpace(str(args, "query")), BrandID: str(args, "brand_id"), SessionID: str(args, "session_id"), DateFrom: str(args, "date_from"), DateTo: str(args, "date_to"), Kind: str(args, "kind"), Lineage: str(args, "lineage"), Sort: str(args, "sort"), ReviewStatus: str(args, "review_status"), Destination: str(args, "destination"), AccountRef: str(args, "account_ref"), Availability: str(args, "availability"), Limit: 30}
	if o.EntityType == "" {
		o.EntityType = "all"
	}
	if o.Availability == "" {
		o.Availability = "any"
	}
	if o.Sort == "" {
		o.Sort = "session_newest"
	}
	if !oneOf(o.EntityType, "all", "assets", "sessions", "releases") {
		return o, errors.New("entity_type must be all, assets, sessions, or releases")
	}
	if !oneOf(o.Availability, "any", "never_used", "not_published", "ready_to_publish", "scheduled", "published", "failed") {
		return o, errors.New("invalid availability filter")
	}
	if o.Lineage != "" && !oneOf(o.Lineage, "source", "derivative") {
		return o, errors.New("lineage must be source or derivative")
	}
	if !oneOf(o.Sort, "session_newest", "asset_newest") {
		return o, errors.New("invalid sort")
	}
	if o.ReviewStatus != "" && !oneOf(o.ReviewStatus, "pending", "approved", "rejected") {
		return o, errors.New("invalid review_status")
	}
	if o.Availability != "any" && o.Availability != "never_used" && o.Destination == "" {
		return o, errors.New("availability filter requires destination")
	}
	if o.AccountRef != "" && o.Destination == "" {
		return o, errors.New("account_ref requires destination")
	}
	for _, date := range []string{o.DateFrom, o.DateTo} {
		if date != "" {
			if _, err := time.Parse("2006-01-02", date); err != nil {
				return o, errors.New("date_from and date_to must be YYYY-MM-DD")
			}
		}
	}
	if o.DateFrom != "" && o.DateTo != "" && o.DateFrom > o.DateTo {
		return o, errors.New("date_from must not follow date_to")
	}
	if v, ok := args["limit"]; ok {
		var n int
		switch x := v.(type) {
		case float64:
			n = int(x)
			if float64(n) != x {
				return o, errors.New("limit must be an integer")
			}
		case int:
			n = x
		case int64:
			n = int(x)
		case string:
			var err error
			n, err = strconv.Atoi(x)
			if err != nil {
				return o, errors.New("limit must be an integer")
			}
		default:
			return o, errors.New("limit must be an integer")
		}
		if n < 1 || n > 100 {
			return o, errors.New("limit must be between 1 and 100")
		}
		o.Limit = n
	}
	return o, nil
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func likePattern(query string) string {
	query = strings.ReplaceAll(query, `\`, `\\`)
	query = strings.ReplaceAll(query, `%`, `\%`)
	query = strings.ReplaceAll(query, `_`, `\_`)
	return "%" + query + "%"
}

func decodeSearchCursor(value string) (searchCursor, error) {
	if value == "" {
		return searchCursor{}, nil
	}
	var c searchCursor
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || json.Unmarshal(b, &c) != nil || c.ID == "" || (c.Date != "" && len(c.Date) != 10) {
		return c, errors.New("invalid search cursor")
	}
	if c.Date != "" && !validSessionDate(c.Date) {
		return c, errors.New("invalid search cursor")
	}
	return c, nil
}

func encodeSearchCursor(date, createdAt, id string) string {
	b, _ := json.Marshal(searchCursor{Date: date, CreatedAt: createdAt, ID: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func searchCursorArg(args map[string]any, entity string) string {
	if value := str(args, entity+"_cursor"); value != "" {
		return value
	}
	if cursors, ok := args["cursors"].(map[string]any); ok {
		if value, ok := cursors[entity].(string); ok {
			return value
		}
	}
	return ""
}

func (a *App) search(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	o, err := parseSearchOptions(pid, args)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"assets": searchPage[assetSearchHit]{Items: []assetSearchHit{}}, "sessions": searchPage[sessionSearchHit]{Items: []sessionSearchHit{}}}
	if o.EntityType == "all" || o.EntityType == "assets" {
		cursor, err := decodeSearchCursor(searchCursorArg(args, "assets"))
		if err != nil {
			return nil, err
		}
		page, err := a.searchAssets(ctx.AppDB(), o, cursor)
		if err != nil {
			return nil, err
		}
		out["assets"] = page
	}
	// Asset-only filters do not silently change the meaning of a session or release result.
	assetOnly := o.Kind != "" || o.Lineage != "" || o.ReviewStatus != "" || o.Availability != "any" || o.SessionID != ""
	if assetOnly && (o.EntityType == "sessions" || o.EntityType == "releases") {
		return nil, errors.New("file filters require entity_type assets or all")
	}
	if !assetOnly && o.Destination == "" && (o.EntityType == "all" || o.EntityType == "sessions") {
		cursor, err := decodeSearchCursor(searchCursorArg(args, "sessions"))
		if err != nil {
			return nil, err
		}
		page, err := a.searchSessions(ctx.AppDB(), o, cursor)
		if err != nil {
			return nil, err
		}
		out["sessions"] = page
	}
	// Explicit legacy queries remain readable during migration; ordinary search
	// no longer surfaces the retired release workflow.
	if !assetOnly && o.EntityType == "releases" {
		cursor, err := decodeSearchCursor(searchCursorArg(args, "releases"))
		if err != nil {
			return nil, err
		}
		page, err := a.searchReleases(ctx.AppDB(), o, cursor)
		if err != nil {
			return nil, err
		}
		out["releases"] = page
	}
	return out, nil
}

func (a *App) searchAssets(db *sql.DB, o searchOptions, cursor searchCursor) (searchPage[assetSearchHit], error) {
	page := searchPage[assetSearchHit]{Items: []assetSearchHit{}}
	q := `SELECT a.id,a.session_id,a.storage_install_id,a.storage_file_id,a.name,a.kind,a.content_type,a.sha256,a.size_bytes,a.review_status,a.media_status,a.media_rating,s.brand_id,s.title,s.session_date,a.created_at,EXISTS(SELECT 1 FROM asset_sources src WHERE src.project_id=a.project_id AND src.child_asset_id=a.id)
		FROM assets a JOIN sessions s ON s.id=a.session_id AND s.project_id=a.project_id WHERE a.project_id=?`
	values := []any{o.ProjectID}
	if o.BrandID != "" {
		q += ` AND s.brand_id=?`
		values = append(values, o.BrandID)
	}
	if o.SessionID != "" {
		q += ` AND a.session_id=?`
		values = append(values, o.SessionID)
	}
	if o.Query != "" {
		q += ` AND (a.name LIKE ? ESCAPE '\' OR s.title LIKE ? ESCAPE '\' OR s.notes LIKE ? ESCAPE '\' OR a.storage_file_id=?)`
		pattern := likePattern(o.Query)
		values = append(values, pattern, pattern, pattern, o.Query)
	}
	if o.DateFrom != "" {
		q += ` AND s.session_date>=?`
		values = append(values, o.DateFrom)
	}
	if o.DateTo != "" {
		q += ` AND s.session_date<>'' AND s.session_date<=?`
		values = append(values, o.DateTo)
	}
	if o.Kind != "" {
		q += ` AND a.kind=?`
		values = append(values, o.Kind)
	}
	if o.Lineage == "derivative" {
		q += ` AND EXISTS(SELECT 1 FROM asset_sources src WHERE src.project_id=a.project_id AND src.child_asset_id=a.id)`
	}
	if o.Lineage == "source" {
		q += ` AND NOT EXISTS(SELECT 1 FROM asset_sources src WHERE src.project_id=a.project_id AND src.child_asset_id=a.id)`
	}
	if o.ReviewStatus != "" {
		q += ` AND a.review_status=?`
		values = append(values, o.ReviewStatus)
	}
	sortDate := "s.session_date"
	if o.Sort == "asset_newest" {
		sortDate = "substr(a.created_at,1,10)"
	}
	if cursor.ID != "" {
		if cursor.CreatedAt == "" {
			return page, errors.New("invalid asset cursor")
		}
		q += ` AND (` + sortDate + `<? OR (` + sortDate + `=? AND (a.created_at<? OR (a.created_at=? AND a.id<?))))`
		values = append(values, cursor.Date, cursor.Date, cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
	}
	target := `SELECT 1 FROM post_assets pa JOIN posts p ON p.id=pa.post_id AND p.project_id=pa.project_id WHERE pa.project_id=a.project_id AND pa.asset_id=a.id`
	matchingTarget := target
	matchingValues := []any{}
	if o.Destination != "" {
		matchingTarget += ` AND LOWER(p.destination) LIKE LOWER(?)`
		matchingValues = append(matchingValues, o.Destination+"%")
	}
	if o.AccountRef != "" {
		matchingTarget += ` AND p.account_ref=?`
		matchingValues = append(matchingValues, o.AccountRef)
	}
	wasPublished := `p.status='verified_published' OR EXISTS (SELECT 1 FROM post_events pe WHERE pe.project_id=p.project_id AND pe.post_id=p.id AND pe.status='verified_published')`
	switch o.Availability {
	case "never_used":
		q += ` AND NOT EXISTS (` + target + `)`
	case "not_published":
		q += ` AND NOT EXISTS (` + matchingTarget + ` AND (` + wasPublished + `))`
		values = append(values, matchingValues...)
	case "ready_to_publish":
		q += ` AND a.review_status='approved' AND NOT EXISTS (` + matchingTarget + ` AND (p.status NOT IN ('failed','removed') OR (` + wasPublished + `)))`
		values = append(values, matchingValues...)
	case "scheduled", "failed":
		q += ` AND EXISTS (` + matchingTarget + ` AND p.status=?)`
		values = append(values, append(matchingValues, o.Availability)...)
	case "published":
		q += ` AND EXISTS (` + matchingTarget + ` AND (` + wasPublished + `))`
		values = append(values, matchingValues...)
	case "any":
		if o.Destination != "" {
			q += ` AND EXISTS (` + matchingTarget + `)`
			values = append(values, matchingValues...)
		}
	}
	q += ` ORDER BY ` + sortDate + ` DESC,a.created_at DESC,a.id DESC LIMIT ?`
	values = append(values, o.Limit+1)
	rows, err := db.Query(q, values...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var h assetSearchHit
		if err := rows.Scan(&h.ID, &h.SessionID, &h.StorageInstallID, &h.StorageFileID, &h.Name, &h.Kind, &h.ContentType, &h.SHA256, &h.SizeBytes, &h.ReviewStatus, &h.MediaStatus, &h.MediaRating, &h.BrandID, &h.SessionTitle, &h.SessionDate, &h.AttachedAt, &h.IsDerivative); err != nil {
			return page, err
		}
		h.Uses = []searchUse{}
		page.Items = append(page.Items, h)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > o.Limit {
		page.Items = page.Items[:o.Limit]
		last := page.Items[len(page.Items)-1]
		date := last.SessionDate
		if o.Sort == "asset_newest" {
			date = last.AttachedAt[:10]
		}
		page.NextCursor = encodeSearchCursor(date, last.AttachedAt, last.ID)
	}
	if err := loadSearchUses(db, o.ProjectID, page.Items); err != nil {
		return page, err
	}
	for i := range page.Items {
		h := &page.Items[i]
		switch o.Availability {
		case "ready_to_publish":
			h.MatchReason = "Approved; no active publication for this destination and account"
		case "never_used":
			h.MatchReason = "No platform use recorded"
		case "not_published":
			h.MatchReason = "No verified publication history for this destination and account"
		default:
			h.MatchReason = "Matches Catalog search filters"
		}
	}
	return page, nil
}

func loadSearchUses(db *sql.DB, pid string, hits []assetSearchHit) error {
	if len(hits) == 0 {
		return nil
	}
	placeholders := make([]string, len(hits))
	values := []any{pid}
	byID := map[string]int{}
	for i := range hits {
		placeholders[i] = "?"
		values = append(values, hits[i].ID)
		byID[hits[i].ID] = i
	}
	q := `SELECT pa.asset_id,p.id,p.destination,p.account_ref,p.status,p.external_post_id,p.external_url,p.actual_at
		FROM post_assets pa JOIN posts p ON p.project_id=pa.project_id AND p.id=pa.post_id
		WHERE pa.project_id=? AND pa.asset_id IN (` + strings.Join(placeholders, ",") + `) ORDER BY p.created_at DESC,p.id DESC`
	rows, err := db.Query(q, values...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var assetID string
		var use searchUse
		if err := rows.Scan(&assetID, &use.PublicationID, &use.Destination, &use.AccountRef, &use.Status, &use.ExternalPostID, &use.ExternalURL, &use.ActualAt); err != nil {
			return err
		}
		hits[byID[assetID]].Uses = append(hits[byID[assetID]].Uses, use)
	}
	return rows.Err()
}

func (a *App) searchSessions(db *sql.DB, o searchOptions, cursor searchCursor) (searchPage[sessionSearchHit], error) {
	page := searchPage[sessionSearchHit]{Items: []sessionSearchHit{}}
	q := `SELECT s.id,s.brand_id,s.title,s.session_date,s.status,s.notes,b.name,(SELECT COUNT(*) FROM assets a WHERE a.project_id=s.project_id AND a.session_id=s.id) FROM sessions s JOIN brands b ON b.id=s.brand_id AND b.project_id=s.project_id WHERE s.project_id=?`
	values := []any{o.ProjectID}
	if o.BrandID != "" {
		q += ` AND s.brand_id=?`
		values = append(values, o.BrandID)
	}
	if o.Query != "" {
		q += ` AND (s.title LIKE ? ESCAPE '\' OR s.notes LIKE ? ESCAPE '\' OR b.name LIKE ? ESCAPE '\')`
		pattern := likePattern(o.Query)
		values = append(values, pattern, pattern, pattern)
	}
	if o.DateFrom != "" {
		q += ` AND s.session_date>=?`
		values = append(values, o.DateFrom)
	}
	if o.DateTo != "" {
		q += ` AND s.session_date<>'' AND s.session_date<=?`
		values = append(values, o.DateTo)
	}
	if cursor.ID != "" {
		q += ` AND (s.session_date<? OR (s.session_date=? AND s.id<?))`
		values = append(values, cursor.Date, cursor.Date, cursor.ID)
	}
	q += ` ORDER BY s.session_date DESC,s.id DESC LIMIT ?`
	values = append(values, o.Limit+1)
	rows, err := db.Query(q, values...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var h sessionSearchHit
		if err := rows.Scan(&h.ID, &h.BrandID, &h.Title, &h.Date, &h.Status, &h.Notes, &h.BrandName, &h.AssetCount); err != nil {
			return page, err
		}
		page.Items = append(page.Items, h)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > o.Limit {
		page.Items = page.Items[:o.Limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeSearchCursor(last.Date, "", last.ID)
	}
	return page, nil
}

func (a *App) searchReleases(db *sql.DB, o searchOptions, cursor searchCursor) (searchPage[releaseSearchHit], error) {
	page := searchPage[releaseSearchHit]{Items: []releaseSearchHit{}}
	dateExpr := `substr(CASE WHEN r.planned_at!='' THEN r.planned_at ELSE r.created_at END,1,10)`
	q := `SELECT r.id,r.brand_id,r.title,r.phase,r.audience,r.planned_at,r.approval_status,b.name,(SELECT COUNT(*) FROM release_targets t WHERE t.project_id=r.project_id AND t.release_id=r.id),` + dateExpr + ` FROM releases r JOIN brands b ON b.id=r.brand_id AND b.project_id=r.project_id WHERE r.project_id=?`
	values := []any{o.ProjectID}
	if o.BrandID != "" {
		q += ` AND r.brand_id=?`
		values = append(values, o.BrandID)
	}
	if o.Query != "" {
		q += ` AND (r.title LIKE ? ESCAPE '\' OR r.phase LIKE ? ESCAPE '\' OR r.audience LIKE ? ESCAPE '\' OR b.name LIKE ? ESCAPE '\')`
		pattern := likePattern(o.Query)
		values = append(values, pattern, pattern, pattern, pattern)
	}
	if o.Destination != "" {
		q += ` AND EXISTS (SELECT 1 FROM release_targets t WHERE t.project_id=r.project_id AND t.release_id=r.id AND t.destination=?`
		values = append(values, o.Destination)
		if o.AccountRef != "" {
			q += ` AND t.account_ref=?`
			values = append(values, o.AccountRef)
		}
		q += `)`
	}
	if o.DateFrom != "" {
		q += ` AND ` + dateExpr + `>=?`
		values = append(values, o.DateFrom)
	}
	if o.DateTo != "" {
		q += ` AND ` + dateExpr + `<=?`
		values = append(values, o.DateTo)
	}
	if cursor.ID != "" {
		q += ` AND (` + dateExpr + `<? OR (` + dateExpr + `=? AND r.id<?))`
		values = append(values, cursor.Date, cursor.Date, cursor.ID)
	}
	q += ` ORDER BY ` + dateExpr + ` DESC,r.id DESC LIMIT ?`
	values = append(values, o.Limit+1)
	rows, err := db.Query(q, values...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var h releaseSearchHit
		if err := rows.Scan(&h.ID, &h.BrandID, &h.Title, &h.Phase, &h.Audience, &h.PlannedAt, &h.ApprovalStatus, &h.BrandName, &h.TargetCount, &h.SortDate); err != nil {
			return page, err
		}
		page.Items = append(page.Items, h)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > o.Limit {
		page.Items = page.Items[:o.Limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeSearchCursor(last.SortDate, "", last.ID)
	}
	return page, nil
}
