package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Status values a place can have. "closed" is for places that shut
// down (kept so ratings/visits aren't lost).
var validStatuses = map[string]bool{"to_try": true, "visited": true, "favorite": true, "closed": true}

// Suggested kinds. Kind is free-form; these are what the panel offers
// and what lookups map onto.
var suggestedKinds = []string{
	"restaurant", "cafe", "bar", "bakery", "hotel", "museum", "attraction",
	"viewpoint", "beach", "park", "hike", "shop", "market", "venue", "other",
}

type Place struct {
	ID            int64           `json:"id"`
	ProjectID     string          `json:"project_id,omitempty"`
	Name          string          `json:"name"`
	Kind          string          `json:"kind"`
	Status        string          `json:"status"`
	Lat           *float64        `json:"lat"`
	Lng           *float64        `json:"lng"`
	Address       string          `json:"address"`
	City          string          `json:"city"`
	Country       string          `json:"country"`
	GooglePlaceID string          `json:"google_place_id,omitempty"`
	OSMID         string          `json:"osm_id,omitempty"`
	GoogleMapsURL string          `json:"google_maps_url,omitempty"`
	Website       string          `json:"website,omitempty"`
	Phone         string          `json:"phone,omitempty"`
	PriceLevel    *int            `json:"price_level"`
	GoogleRating  *float64        `json:"google_rating"`
	MyRating      *int            `json:"my_rating"`
	SourceURL     string          `json:"source_url,omitempty"`
	Notes         string          `json:"notes"`
	Tags          json.RawMessage `json:"tags"`
	Metadata      json.RawMessage `json:"metadata"`
	VisitCount    int             `json:"visit_count"`
	LastVisitedAt string          `json:"last_visited_at,omitempty"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
	DistanceM     *float64        `json:"distance_m,omitempty"`
	ListIDs       []int64         `json:"list_ids"`
}

type Visit struct {
	ID        int64           `json:"id"`
	PlaceID   int64           `json:"place_id"`
	VisitedOn string          `json:"visited_on"`
	WithWhom  string          `json:"with_whom"`
	Dishes    json.RawMessage `json:"dishes"`
	Spend     *float64        `json:"spend"`
	Currency  string          `json:"currency"`
	Rating    *int            `json:"rating"`
	Notes     string          `json:"notes"`
	Details   json.RawMessage `json:"details"`
	CreatedAt string          `json:"created_at"`
}

type List struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
	PlaceCount  int    `json:"place_count"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type Settings struct {
	ProjectID          string   `json:"project_id"`
	PlacesConnectionID int64    `json:"places_connection_id"`
	HomeLat            *float64 `json:"home_lat"`
	HomeLng            *float64 `json:"home_lng"`
	HomeLabel          string   `json:"home_label"`
}

// ─── places ──────────────────────────────────────────────────────

const placeCols = `id, project_id, name, kind, status, lat, lng, address, city, country,
	google_place_id, osm_id, google_maps_url, website, phone, price_level, google_rating,
	my_rating, source_url, notes, tags_json, metadata_json, visit_count,
	COALESCE(last_visited_at, ''), created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanPlace(s scanner) (*Place, error) {
	var p Place
	var lat, lng, grating sql.NullFloat64
	var price, mine sql.NullInt64
	var tags, meta string
	err := s.Scan(&p.ID, &p.ProjectID, &p.Name, &p.Kind, &p.Status, &lat, &lng, &p.Address,
		&p.City, &p.Country, &p.GooglePlaceID, &p.OSMID, &p.GoogleMapsURL, &p.Website, &p.Phone,
		&price, &grating, &mine, &p.SourceURL, &p.Notes, &tags, &meta, &p.VisitCount,
		&p.LastVisitedAt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	p.Lat = nullFloat(lat)
	p.Lng = nullFloat(lng)
	p.GoogleRating = nullFloat(grating)
	p.PriceLevel = nullInt(price)
	p.MyRating = nullInt(mine)
	p.Tags = json.RawMessage(defaultJSON(tags, "[]"))
	p.Metadata = json.RawMessage(defaultJSON(meta, "{}"))
	p.ListIDs = []int64{}
	return &p, nil
}

func getPlace(db *sql.DB, projectID string, id int64) (*Place, error) {
	p, err := scanPlace(db.QueryRow(`SELECT `+placeCols+` FROM places WHERE id = ? AND project_id = ?`, id, projectID))
	if err != nil {
		return nil, err
	}
	if err := attachListIDs(db, []*Place{p}); err != nil {
		return nil, err
	}
	return p, nil
}

func findPlaceByGoogleID(db *sql.DB, projectID, gid string) (*Place, error) {
	if gid == "" {
		return nil, nil
	}
	p, err := scanPlace(db.QueryRow(`SELECT `+placeCols+` FROM places WHERE project_id = ? AND google_place_id = ?`, projectID, gid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// findNearDuplicate returns an existing place with the same name
// (case-insensitive) within 150 m, or with the same name and no
// coordinates on either side in the same city.
func findNearDuplicate(db *sql.DB, projectID, name string, lat, lng *float64, city string) (*Place, error) {
	rows, err := db.Query(`SELECT `+placeCols+` FROM places WHERE project_id = ? AND lower(name) = lower(?)`, projectID, strings.TrimSpace(name))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanPlace(rows)
		if err != nil {
			return nil, err
		}
		if lat != nil && lng != nil && p.Lat != nil && p.Lng != nil {
			if haversine(*lat, *lng, *p.Lat, *p.Lng) <= 150 {
				return p, nil
			}
			continue
		}
		if strings.EqualFold(strings.TrimSpace(p.City), strings.TrimSpace(city)) {
			return p, nil
		}
	}
	return nil, rows.Err()
}

type placeInput struct {
	Name          string
	Kind          string
	Status        string
	Lat, Lng      *float64
	Address       string
	City          string
	Country       string
	GooglePlaceID string
	OSMID         string
	GoogleMapsURL string
	Website       string
	Phone         string
	PriceLevel    *int
	GoogleRating  *float64
	MyRating      *int
	SourceURL     string
	Notes         string
	TagsJSON      string
	MetadataJSON  string
}

func insertPlace(db *sql.DB, projectID string, in placeInput) (*Place, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, errors.New("name required")
	}
	status := in.Status
	if status == "" {
		status = "to_try"
	}
	if !validStatuses[status] {
		return nil, fmt.Errorf("invalid status %q (to_try, visited, favorite, closed)", status)
	}
	if err := validateRating(in.MyRating); err != nil {
		return nil, err
	}
	now := nowUTC()
	res, err := db.Exec(`INSERT INTO places
		(project_id, name, kind, status, lat, lng, address, city, country, google_place_id, osm_id,
		 google_maps_url, website, phone, price_level, google_rating, my_rating, source_url, notes,
		 tags_json, metadata_json, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		projectID, strings.TrimSpace(in.Name), cleanToken(in.Kind, "other"), status, in.Lat, in.Lng,
		in.Address, in.City, in.Country, in.GooglePlaceID, in.OSMID, in.GoogleMapsURL, in.Website,
		in.Phone, in.PriceLevel, in.GoogleRating, in.MyRating, in.SourceURL, in.Notes,
		defaultJSON(in.TagsJSON, "[]"), defaultJSON(in.MetadataJSON, "{}"), now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getPlace(db, projectID, id)
}

// updatePlace applies a column → value patch. Keys must be column
// names from allowedPatchCols.
var allowedPatchCols = map[string]bool{
	"name": true, "kind": true, "status": true, "lat": true, "lng": true, "address": true,
	"city": true, "country": true, "google_place_id": true, "osm_id": true, "google_maps_url": true,
	"website": true, "phone": true, "price_level": true, "google_rating": true, "my_rating": true,
	"source_url": true, "notes": true, "tags_json": true, "metadata_json": true,
}

func updatePlace(db *sql.DB, projectID string, id int64, patch map[string]any) (*Place, error) {
	if len(patch) == 0 {
		return getPlace(db, projectID, id)
	}
	keys := make([]string, 0, len(patch))
	for k := range patch {
		if !allowedPatchCols[k] {
			return nil, fmt.Errorf("cannot update %q", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sets := []string{}
	args := []any{}
	for _, k := range keys {
		sets = append(sets, k+" = ?")
		args = append(args, patch[k])
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, nowUTC(), id, projectID)
	res, err := db.Exec(`UPDATE places SET `+strings.Join(sets, ", ")+` WHERE id = ? AND project_id = ?`, args...)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, sql.ErrNoRows
	}
	return getPlace(db, projectID, id)
}

func deletePlace(db *sql.DB, projectID string, id int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM places WHERE id = ? AND project_id = ?`, id, projectID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.Exec(`DELETE FROM visits WHERE place_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM list_places WHERE place_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

type placeFilter struct {
	Q              string
	Kind           string
	Status         string // "", a status, "active" (everything but closed) or "all"
	Tag            string
	City           string
	ListID         int64
	NearLat        *float64
	NearLng        *float64
	RadiusM        float64
	MaxPrice       *int
	MinMyRating    *int
	NotVisitedDays int
	Sort           string // recent | rating | distance | name
	Limit          int
}

func listPlaces(db *sql.DB, projectID string, f placeFilter) ([]*Place, error) {
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	where := []string{"p.project_id = ?"}
	args := []any{projectID}
	switch f.Status {
	case "", "active":
		where = append(where, "p.status <> 'closed'")
	case "all":
	default:
		where = append(where, "p.status = ?")
		args = append(args, f.Status)
	}
	if f.Kind != "" {
		where = append(where, "p.kind = ?")
		args = append(args, f.Kind)
	}
	if f.Tag != "" {
		where = append(where, "EXISTS (SELECT 1 FROM json_each(p.tags_json) WHERE lower(value) = lower(?))")
		args = append(args, f.Tag)
	}
	if f.City != "" {
		where = append(where, "lower(p.city) = lower(?)")
		args = append(args, f.City)
	}
	if f.ListID > 0 {
		where = append(where, "EXISTS (SELECT 1 FROM list_places lp WHERE lp.place_id = p.id AND lp.list_id = ?)")
		args = append(args, f.ListID)
	}
	if f.MaxPrice != nil {
		where = append(where, "(p.price_level IS NULL OR p.price_level <= ?)")
		args = append(args, *f.MaxPrice)
	}
	if f.MinMyRating != nil {
		where = append(where, "p.my_rating >= ?")
		args = append(args, *f.MinMyRating)
	}
	if f.NotVisitedDays > 0 {
		cutoff := time.Now().UTC().AddDate(0, 0, -f.NotVisitedDays).Format("2006-01-02")
		where = append(where, "(p.last_visited_at IS NULL OR p.last_visited_at < ?)")
		args = append(args, cutoff)
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		like := "%" + escapeLike(q) + "%"
		where = append(where, `(p.name LIKE ? ESCAPE '\' OR p.notes LIKE ? ESCAPE '\' OR p.address LIKE ? ESCAPE '\'
			OR p.city LIKE ? ESCAPE '\' OR p.tags_json LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like, like, like)
	}
	geo := f.NearLat != nil && f.NearLng != nil
	if geo && f.RadiusM > 0 {
		// Bounding box prefilter; exact distance is checked below.
		dLat := f.RadiusM / 111320.0
		dLng := f.RadiusM / (111320.0 * math.Max(math.Cos(*f.NearLat*math.Pi/180), 0.01))
		where = append(where, "p.lat BETWEEN ? AND ? AND p.lng BETWEEN ? AND ?")
		args = append(args, *f.NearLat-dLat, *f.NearLat+dLat, *f.NearLng-dLng, *f.NearLng+dLng)
	}
	order := "p.updated_at DESC, p.id DESC"
	switch f.Sort {
	case "rating":
		order = "p.my_rating IS NULL, p.my_rating DESC, p.google_rating DESC, p.updated_at DESC"
	case "name":
		order = "p.name COLLATE NOCASE ASC"
	}
	sqlLimit := limit
	if geo {
		sqlLimit = 5000 // distance sort/filter happens in Go
	}
	args = append(args, sqlLimit)
	rows, err := db.Query(`SELECT `+placeCols+` FROM places p WHERE `+strings.Join(where, " AND ")+
		` ORDER BY `+order+` LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Place{}
	for rows.Next() {
		p, err := scanPlace(rows)
		if err != nil {
			return nil, err
		}
		if geo && p.Lat != nil && p.Lng != nil {
			d := haversine(*f.NearLat, *f.NearLng, *p.Lat, *p.Lng)
			if f.RadiusM > 0 && d > f.RadiusM {
				continue
			}
			p.DistanceM = &d
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if geo && (f.Sort == "distance" || f.Sort == "") {
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].DistanceM == nil {
				return false
			}
			if out[j].DistanceM == nil {
				return true
			}
			return *out[i].DistanceM < *out[j].DistanceM
		})
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, attachListIDs(db, out)
}

func attachListIDs(db *sql.DB, places []*Place) error {
	if len(places) == 0 {
		return nil
	}
	byID := map[int64]*Place{}
	ids := make([]any, 0, len(places))
	for _, p := range places {
		byID[p.ID] = p
		ids = append(ids, p.ID)
	}
	rows, err := db.Query(`SELECT place_id, list_id FROM list_places WHERE place_id IN (`+placeholders(len(ids))+`) ORDER BY list_id`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pid, lid int64
		if err := rows.Scan(&pid, &lid); err != nil {
			return err
		}
		if p := byID[pid]; p != nil {
			p.ListIDs = append(p.ListIDs, lid)
		}
	}
	return rows.Err()
}

// ─── visits ──────────────────────────────────────────────────────

type visitInput struct {
	VisitedOn   string
	WithWhom    string
	DishesJSON  string
	Spend       *float64
	Currency    string
	Rating      *int
	Notes       string
	DetailsJSON string
}

// logVisit records a visit, bumps the place's counters, promotes a
// to_try place to visited, and copies the rating onto my_rating.
func logVisit(db *sql.DB, projectID string, placeID int64, in visitInput) (*Visit, *Place, error) {
	if err := validateRating(in.Rating); err != nil {
		return nil, nil, err
	}
	day := strings.TrimSpace(in.VisitedOn)
	if day == "" {
		day = time.Now().UTC().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return nil, nil, fmt.Errorf("visited_on must be YYYY-MM-DD")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM places WHERE id = ? AND project_id = ?`, placeID, projectID).Scan(&exists); err != nil {
		return nil, nil, err
	}
	now := nowUTC()
	res, err := tx.Exec(`INSERT INTO visits (project_id, place_id, visited_on, with_whom, dishes_json, spend, currency, rating, notes, details_json, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, projectID, placeID, day, in.WithWhom, defaultJSON(in.DishesJSON, "[]"),
		in.Spend, strings.ToUpper(strings.TrimSpace(in.Currency)), in.Rating, in.Notes, defaultJSON(in.DetailsJSON, "{}"), now)
	if err != nil {
		return nil, nil, err
	}
	vid, _ := res.LastInsertId()
	if _, err := tx.Exec(`UPDATE places SET
			visit_count = (SELECT COUNT(*) FROM visits WHERE place_id = ?),
			last_visited_at = (SELECT MAX(visited_on) FROM visits WHERE place_id = ?),
			status = CASE WHEN status = 'to_try' THEN 'visited' ELSE status END,
			my_rating = COALESCE(?, my_rating),
			updated_at = ?
		WHERE id = ?`, placeID, placeID, in.Rating, now, placeID); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	visits, err := listVisits(db, projectID, placeID, vid)
	if err != nil || len(visits) == 0 {
		return nil, nil, err
	}
	p, err := getPlace(db, projectID, placeID)
	return visits[0], p, err
}

// listVisits returns a place's visits newest first. onlyID > 0 narrows to one row.
func listVisits(db *sql.DB, projectID string, placeID, onlyID int64) ([]*Visit, error) {
	q := `SELECT id, place_id, visited_on, with_whom, dishes_json, spend, currency, rating, notes, details_json, created_at
		FROM visits WHERE project_id = ? AND place_id = ?`
	args := []any{projectID, placeID}
	if onlyID > 0 {
		q += ` AND id = ?`
		args = append(args, onlyID)
	}
	rows, err := db.Query(q+` ORDER BY visited_on DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Visit{}
	for rows.Next() {
		var v Visit
		var dishes, details string
		var spend sql.NullFloat64
		var rating sql.NullInt64
		if err := rows.Scan(&v.ID, &v.PlaceID, &v.VisitedOn, &v.WithWhom, &dishes, &spend, &v.Currency, &rating, &v.Notes, &details, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.Dishes = json.RawMessage(defaultJSON(dishes, "[]"))
		v.Details = json.RawMessage(defaultJSON(details, "{}"))
		v.Spend = nullFloat(spend)
		v.Rating = nullInt(rating)
		out = append(out, &v)
	}
	return out, rows.Err()
}

func deleteVisit(db *sql.DB, projectID string, visitID int64) (*Place, error) {
	var placeID int64
	if err := db.QueryRow(`SELECT place_id FROM visits WHERE id = ? AND project_id = ?`, visitID, projectID).Scan(&placeID); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`DELETE FROM visits WHERE id = ?`, visitID); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`UPDATE places SET
			visit_count = (SELECT COUNT(*) FROM visits WHERE place_id = ?),
			last_visited_at = (SELECT MAX(visited_on) FROM visits WHERE place_id = ?),
			updated_at = ? WHERE id = ?`, placeID, placeID, nowUTC(), placeID); err != nil {
		return nil, err
	}
	return getPlace(db, projectID, placeID)
}

// ─── lists ───────────────────────────────────────────────────────

func listLists(db *sql.DB, projectID string) ([]*List, error) {
	rows, err := db.Query(`SELECT l.id, l.name, l.description, l.color, l.created_at, l.updated_at,
			(SELECT COUNT(*) FROM list_places lp WHERE lp.list_id = l.id)
		FROM lists l WHERE l.project_id = ? ORDER BY l.name COLLATE NOCASE`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*List{}
	for rows.Next() {
		var l List
		if err := rows.Scan(&l.ID, &l.Name, &l.Description, &l.Color, &l.CreatedAt, &l.UpdatedAt, &l.PlaceCount); err != nil {
			return nil, err
		}
		out = append(out, &l)
	}
	return out, rows.Err()
}

func getList(db *sql.DB, projectID string, id int64) (*List, error) {
	lists, err := listLists(db, projectID)
	if err != nil {
		return nil, err
	}
	for _, l := range lists {
		if l.ID == id {
			return l, nil
		}
	}
	return nil, sql.ErrNoRows
}

// ensureList returns the list with that name, creating it if needed.
func ensureList(db *sql.DB, projectID, name, description, color string) (*List, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false, errors.New("list name required")
	}
	var id int64
	err := db.QueryRow(`SELECT id FROM lists WHERE project_id = ? AND name = ? COLLATE NOCASE`, projectID, name).Scan(&id)
	if err == nil {
		l, err := getList(db, projectID, id)
		return l, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	now := nowUTC()
	res, err := db.Exec(`INSERT INTO lists (project_id, name, description, color, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
		projectID, name, description, color, now, now)
	if err != nil {
		return nil, false, err
	}
	id, _ = res.LastInsertId()
	l, err := getList(db, projectID, id)
	return l, true, err
}

func updateList(db *sql.DB, projectID string, id int64, name, description, color *string) (*List, error) {
	sets, args := []string{}, []any{}
	if name != nil {
		if strings.TrimSpace(*name) == "" {
			return nil, errors.New("list name cannot be empty")
		}
		sets, args = append(sets, "name = ?"), append(args, strings.TrimSpace(*name))
	}
	if description != nil {
		sets, args = append(sets, "description = ?"), append(args, *description)
	}
	if color != nil {
		sets, args = append(sets, "color = ?"), append(args, *color)
	}
	sets, args = append(sets, "updated_at = ?"), append(args, nowUTC(), id, projectID)
	res, err := db.Exec(`UPDATE lists SET `+strings.Join(sets, ", ")+` WHERE id = ? AND project_id = ?`, args...)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, sql.ErrNoRows
	}
	return getList(db, projectID, id)
}

func deleteList(db *sql.DB, projectID string, id int64) error {
	res, err := db.Exec(`DELETE FROM lists WHERE id = ? AND project_id = ?`, id, projectID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	_, err = db.Exec(`DELETE FROM list_places WHERE list_id = ?`, id)
	return err
}

func addToList(db *sql.DB, projectID string, listID int64, placeIDs []int64) (int, error) {
	if _, err := getList(db, projectID, listID); err != nil {
		return 0, err
	}
	added := 0
	now := nowUTC()
	for _, pid := range placeIDs {
		var ok int
		if err := db.QueryRow(`SELECT 1 FROM places WHERE id = ? AND project_id = ?`, pid, projectID).Scan(&ok); err != nil {
			return added, fmt.Errorf("place %d: %w", pid, err)
		}
		res, err := db.Exec(`INSERT OR IGNORE INTO list_places (list_id, place_id, added_at) VALUES (?,?,?)`, listID, pid, now)
		if err != nil {
			return added, err
		}
		n, _ := res.RowsAffected()
		added += int(n)
	}
	_, _ = db.Exec(`UPDATE lists SET updated_at = ? WHERE id = ?`, now, listID)
	return added, nil
}

func removeFromList(db *sql.DB, projectID string, listID int64, placeIDs []int64) (int, error) {
	if _, err := getList(db, projectID, listID); err != nil {
		return 0, err
	}
	removed := 0
	for _, pid := range placeIDs {
		res, err := db.Exec(`DELETE FROM list_places WHERE list_id = ? AND place_id = ?`, listID, pid)
		if err != nil {
			return removed, err
		}
		n, _ := res.RowsAffected()
		removed += int(n)
	}
	return removed, nil
}

// ─── settings ────────────────────────────────────────────────────

func loadSettings(db *sql.DB, projectID string) (Settings, error) {
	s := Settings{ProjectID: projectID}
	var conn sql.NullInt64
	var lat, lng sql.NullFloat64
	err := db.QueryRow(`SELECT places_connection_id, home_lat, home_lng, home_label FROM settings WHERE project_id = ?`, projectID).
		Scan(&conn, &lat, &lng, &s.HomeLabel)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.PlacesConnectionID = conn.Int64
	s.HomeLat = nullFloat(lat)
	s.HomeLng = nullFloat(lng)
	return s, nil
}

func saveSettings(db *sql.DB, s Settings) error {
	var conn any
	if s.PlacesConnectionID > 0 {
		conn = s.PlacesConnectionID
	}
	_, err := db.Exec(`INSERT INTO settings (project_id, places_connection_id, home_lat, home_lng, home_label, updated_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(project_id) DO UPDATE SET places_connection_id = excluded.places_connection_id,
			home_lat = excluded.home_lat, home_lng = excluded.home_lng, home_label = excluded.home_label,
			updated_at = excluded.updated_at`,
		s.ProjectID, conn, s.HomeLat, s.HomeLng, s.HomeLabel, nowUTC())
	return err
}

// ─── small helpers ───────────────────────────────────────────────

func validateRating(r *int) error {
	if r != nil && (*r < 1 || *r > 10) {
		return errors.New("rating must be between 1 and 10")
	}
	return nil
}

func haversine(lat1, lng1, lat2, lng2 float64) float64 {
	const R = 6371000.0
	toRad := math.Pi / 180
	dLat := (lat2 - lat1) * toRad
	dLng := (lng2 - lng1) * toRad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*toRad)*math.Cos(lat2*toRad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * R * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func nullFloat(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}

func nullInt(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	i := int(v.Int64)
	return &i
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func cleanToken(s, fallback string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return fallback
	}
	return strings.ReplaceAll(s, " ", "_")
}

func defaultJSON(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }
