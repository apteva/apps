package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ─── sites ──────────────────────────────────────────────────────────

type Site struct {
	ID           int64                 `json:"id"`
	Kind         string                `json:"kind"`
	Name         string                `json:"name"`
	CRMContactID *int64                `json:"crm_contact_id,omitempty"`
	Address      string                `json:"address"`
	Lat          *float64              `json:"lat"`
	Lng          *float64              `json:"lng"`
	Timezone     string                `json:"timezone"`
	Hours        map[string][][]string `json:"hours"`
	Access       json.RawMessage       `json:"access"`
	Custom       json.RawMessage       `json:"custom"`
	CreatedAt    string                `json:"created_at"`
	UpdatedAt    string                `json:"updated_at"`
}

const siteCols = `id,kind,name,crm_contact_id,address,lat,lng,timezone,hours_json,access_json,custom_json,created_at,updated_at`

func scanSite(s scanner) (*Site, error) {
	var x Site
	var crm sql.NullInt64
	var lat, lng sql.NullFloat64
	var hours, access, custom string
	if err := s.Scan(&x.ID, &x.Kind, &x.Name, &crm, &x.Address, &lat, &lng, &x.Timezone, &hours, &access, &custom, &x.CreatedAt, &x.UpdatedAt); err != nil {
		return nil, err
	}
	if crm.Valid {
		x.CRMContactID = &crm.Int64
	}
	if lat.Valid && lng.Valid {
		x.Lat, x.Lng = &lat.Float64, &lng.Float64
	}
	decodeInto(hours, &x.Hours)
	x.Access, x.Custom = json.RawMessage(access), json.RawMessage(custom)
	return &x, nil
}

func getSite(db *sql.DB, pid string, id int64) (*Site, error) {
	return scanSite(db.QueryRow(`SELECT `+siteCols+` FROM sites WHERE project_id=? AND id=?`, pid, id))
}

var weekdayKeys = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func validateHours(h map[string][][]string) error {
	for day, spans := range h {
		if !contains(weekdayKeys, day) {
			return fmt.Errorf("hours: unknown day %q (use mon..sun)", day)
		}
		for _, s := range spans {
			if len(s) != 2 {
				return fmt.Errorf("hours %s: each span is [\"HH:MM\",\"HH:MM\"]", day)
			}
			a, errA := hhmm(s[0])
			b, errB := hhmm(s[1])
			if errA != nil || errB != nil || b <= a {
				return fmt.Errorf("hours %s: invalid span %v", day, s)
			}
		}
	}
	return nil
}

func hhmm(s string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || h < 0 || h > 24 || m < 0 || m > 59 {
		return 0, fmt.Errorf("bad time %q", s)
	}
	return h*60 + m, nil
}

func upsertSite(db *sql.DB, pid string, args map[string]any) (*Site, error) {
	id := int64Arg(args, "id")
	var cur *Site
	if id > 0 {
		c, err := getSite(db, pid, id)
		if err != nil {
			return nil, notFound(err, "site")
		}
		cur = c
	} else {
		cur = &Site{Kind: "own", Timezone: "UTC", Hours: map[string][][]string{}, Access: json.RawMessage("{}"), Custom: json.RawMessage("{}")}
	}
	if v := stringArg(args, "kind"); v != "" {
		if v != "own" && v != "customer" {
			return nil, errors.New("kind must be own or customer")
		}
		cur.Kind = v
	}
	if v := stringArg(args, "name"); v != "" {
		cur.Name = v
	}
	if cur.Name == "" {
		cur.Name = stringArg(args, "address")
	}
	if cur.Name == "" {
		return nil, errors.New("name or address required")
	}
	if hasArg(args, "address") {
		cur.Address = stringArg(args, "address")
	}
	if hasArg(args, "crm_contact_id") {
		v := int64Arg(args, "crm_contact_id")
		cur.CRMContactID = &v
	}
	if hasArg(args, "lat") && hasArg(args, "lng") {
		cur.Lat, cur.Lng = floatPtrArg(args, "lat"), floatPtrArg(args, "lng")
	}
	if v := stringArg(args, "timezone"); v != "" {
		if _, err := time.LoadLocation(v); err != nil {
			return nil, fmt.Errorf("unknown timezone %q", v)
		}
		cur.Timezone = v
	}
	if h := objectArg(args, "hours"); h != nil {
		var hours map[string][][]string
		if err := remarshal(h, &hours); err != nil {
			return nil, fmt.Errorf("hours: %w", err)
		}
		if err := validateHours(hours); err != nil {
			return nil, err
		}
		cur.Hours = hours
	}
	if a := objectArg(args, "access"); a != nil {
		cur.Access = json.RawMessage(mustJSON(a))
	}
	if c := objectArg(args, "custom"); c != nil {
		cur.Custom = json.RawMessage(mustJSON(c))
	}
	now := nowUTC()
	var crm any
	if cur.CRMContactID != nil {
		crm = *cur.CRMContactID
	}
	if id > 0 {
		_, err := db.Exec(`UPDATE sites SET kind=?,name=?,crm_contact_id=?,address=?,lat=?,lng=?,timezone=?,hours_json=?,access_json=?,custom_json=?,updated_at=?
			WHERE project_id=? AND id=?`, cur.Kind, cur.Name, crm, cur.Address, nullFloat(cur.Lat), nullFloat(cur.Lng), cur.Timezone,
			jsonOr(cur.Hours, "{}"), string(cur.Access), string(cur.Custom), now, pid, id)
		if err != nil {
			return nil, err
		}
		return getSite(db, pid, id)
	}
	r, err := db.Exec(`INSERT INTO sites(project_id,kind,name,crm_contact_id,address,lat,lng,timezone,hours_json,access_json,custom_json,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, pid, cur.Kind, cur.Name, crm, cur.Address, nullFloat(cur.Lat), nullFloat(cur.Lng),
		cur.Timezone, jsonOr(cur.Hours, "{}"), string(cur.Access), string(cur.Custom), now, now)
	if err != nil {
		return nil, err
	}
	nid, _ := r.LastInsertId()
	return getSite(db, pid, nid)
}

func listSites(db *sql.DB, pid, kind, q string) ([]*Site, error) {
	query := `SELECT ` + siteCols + ` FROM sites WHERE project_id=?`
	args := []any{pid}
	if kind != "" {
		query += ` AND kind=?`
		args = append(args, kind)
	}
	if q != "" {
		query += ` AND (lower(name) LIKE ? OR lower(address) LIKE ?)`
		like := "%" + strings.ToLower(q) + "%"
		args = append(args, like, like)
	}
	rows, err := db.Query(query+` ORDER BY kind, name LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Site{}
	for rows.Next() {
		s, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ─── resources ──────────────────────────────────────────────────────

type Resource struct {
	ID            int64           `json:"id"`
	Kind          string          `json:"kind"`
	Name          string          `json:"name"`
	SiteID        *int64          `json:"site_id"`
	Zone          string          `json:"zone"`
	Capacity      int             `json:"capacity"`
	Quals         []string        `json:"quals"`
	CRMContactID  *int64          `json:"crm_contact_id,omitempty"`
	GigsWorkerID  *int64          `json:"gigs_worker_id,omitempty"`
	HomeLat       *float64        `json:"home_lat"`
	HomeLng       *float64        `json:"home_lng"`
	CostRateCents int64           `json:"cost_rate_cents"`
	NeedsShift    bool            `json:"needs_shift"`
	Active        bool            `json:"active"`
	Custom        json.RawMessage `json:"custom"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
}

const resourceCols = `id,kind,name,site_id,zone,capacity,quals_json,crm_contact_id,gigs_worker_id,home_lat,home_lng,cost_rate_cents,needs_shift,active,custom_json,created_at,updated_at`

func scanResource(s scanner) (*Resource, error) {
	var r Resource
	var site, crm, gigs sql.NullInt64
	var hlat, hlng sql.NullFloat64
	var quals, custom string
	var shift, active int
	if err := s.Scan(&r.ID, &r.Kind, &r.Name, &site, &r.Zone, &r.Capacity, &quals, &crm, &gigs, &hlat, &hlng,
		&r.CostRateCents, &shift, &active, &custom, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	if site.Valid {
		r.SiteID = &site.Int64
	}
	if crm.Valid {
		r.CRMContactID = &crm.Int64
	}
	if gigs.Valid {
		r.GigsWorkerID = &gigs.Int64
	}
	if hlat.Valid && hlng.Valid {
		r.HomeLat, r.HomeLng = &hlat.Float64, &hlng.Float64
	}
	r.Quals = []string{}
	decodeInto(quals, &r.Quals)
	r.NeedsShift, r.Active = shift == 1, active == 1
	r.Custom = json.RawMessage(custom)
	return &r, nil
}

func getResource(db *sql.DB, pid string, id int64) (*Resource, error) {
	return scanResource(db.QueryRow(`SELECT `+resourceCols+` FROM resources WHERE project_id=? AND id=?`, pid, id))
}

func upsertResource(db *sql.DB, pid string, args map[string]any) (*Resource, error) {
	id := int64Arg(args, "id")
	var r *Resource
	if id > 0 {
		c, err := getResource(db, pid, id)
		if err != nil {
			return nil, notFound(err, "resource")
		}
		r = c
	} else {
		r = &Resource{Capacity: 1, Quals: []string{}, Active: true, Custom: json.RawMessage("{}")}
		// People only work inside their shifts by default.
		r.NeedsShift = stringArg(args, "kind") == "person" || stringArg(args, "kind") == "crew"
	}
	if v := stringArg(args, "kind"); v != "" {
		if !validResourceKinds[v] {
			return nil, errors.New("kind must be person, crew, space, equipment or vehicle")
		}
		r.Kind = v
	}
	if r.Kind == "" {
		return nil, errors.New("kind required")
	}
	if v := stringArg(args, "name"); v != "" {
		r.Name = v
	}
	if r.Name == "" {
		return nil, errors.New("name required")
	}
	if hasArg(args, "site_id") {
		v := int64Arg(args, "site_id")
		if v > 0 {
			if _, err := getSite(db, pid, v); err != nil {
				return nil, notFound(err, "site")
			}
			r.SiteID = &v
		} else {
			r.SiteID = nil
		}
	}
	if hasArg(args, "zone") {
		r.Zone = stringArg(args, "zone")
	}
	if hasArg(args, "capacity") {
		r.Capacity = intArg(args, "capacity", 1)
		if r.Capacity < 1 {
			return nil, errors.New("capacity must be >= 1")
		}
	}
	if q := arrayArg(args, "quals"); q != nil {
		r.Quals = []string{}
		for _, x := range q {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				r.Quals = append(r.Quals, strings.TrimSpace(s))
			}
		}
	}
	if hasArg(args, "crm_contact_id") {
		v := int64Arg(args, "crm_contact_id")
		r.CRMContactID = &v
	}
	if hasArg(args, "gigs_worker_id") {
		v := int64Arg(args, "gigs_worker_id")
		r.GigsWorkerID = &v
	}
	if hasArg(args, "home_lat") && hasArg(args, "home_lng") {
		r.HomeLat, r.HomeLng = floatPtrArg(args, "home_lat"), floatPtrArg(args, "home_lng")
	}
	if hasArg(args, "cost_rate_cents") {
		r.CostRateCents = int64Arg(args, "cost_rate_cents")
	}
	if hasArg(args, "needs_shift") {
		r.NeedsShift = boolArg(args, "needs_shift", r.NeedsShift)
	}
	if hasArg(args, "active") {
		r.Active = boolArg(args, "active", true)
	}
	if c := objectArg(args, "custom"); c != nil {
		r.Custom = json.RawMessage(mustJSON(c))
	}
	ptr := func(p *int64) any {
		if p == nil {
			return nil
		}
		return *p
	}
	now := nowUTC()
	if id > 0 {
		_, err := db.Exec(`UPDATE resources SET kind=?,name=?,site_id=?,zone=?,capacity=?,quals_json=?,crm_contact_id=?,gigs_worker_id=?,
			home_lat=?,home_lng=?,cost_rate_cents=?,needs_shift=?,active=?,custom_json=?,updated_at=? WHERE project_id=? AND id=?`,
			r.Kind, r.Name, ptr(r.SiteID), r.Zone, r.Capacity, mustJSON(r.Quals), ptr(r.CRMContactID), ptr(r.GigsWorkerID),
			nullFloat(r.HomeLat), nullFloat(r.HomeLng), r.CostRateCents, boolInt(r.NeedsShift), boolInt(r.Active), string(r.Custom), now, pid, id)
		if err != nil {
			return nil, err
		}
		return getResource(db, pid, id)
	}
	res, err := db.Exec(`INSERT INTO resources(project_id,kind,name,site_id,zone,capacity,quals_json,crm_contact_id,gigs_worker_id,
		home_lat,home_lng,cost_rate_cents,needs_shift,active,custom_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		pid, r.Kind, r.Name, ptr(r.SiteID), r.Zone, r.Capacity, mustJSON(r.Quals), ptr(r.CRMContactID), ptr(r.GigsWorkerID),
		nullFloat(r.HomeLat), nullFloat(r.HomeLng), r.CostRateCents, boolInt(r.NeedsShift), boolInt(r.Active), string(r.Custom), now, now)
	if err != nil {
		return nil, err
	}
	nid, _ := res.LastInsertId()
	return getResource(db, pid, nid)
}

func listResources(db *sql.DB, pid, kind string, siteID int64, activeOnly bool) ([]*Resource, error) {
	query := `SELECT ` + resourceCols + ` FROM resources WHERE project_id=?`
	args := []any{pid}
	if kind != "" {
		query += ` AND kind=?`
		args = append(args, kind)
	}
	if siteID > 0 {
		query += ` AND site_id=?`
		args = append(args, siteID)
	}
	if activeOnly {
		query += ` AND active=1`
	}
	rows, err := db.Query(query+` ORDER BY kind, name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Resource{}
	for rows.Next() {
		r, err := scanResource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─── resource blocks (shifts, time off, blocked) ────────────────────

type Block struct {
	ID         int64  `json:"id"`
	ResourceID int64  `json:"resource_id"`
	Kind       string `json:"kind"`
	StartsAt   string `json:"starts_at"`
	EndsAt     string `json:"ends_at"`
	Note       string `json:"note"`
}

func addBlocks(db *sql.DB, pid string, resourceID int64, raw []any, replaceFrom, replaceTo string) ([]Block, error) {
	if _, err := getResource(db, pid, resourceID); err != nil {
		return nil, notFound(err, "resource")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if replaceFrom != "" && replaceTo != "" {
		from, err1 := parseTime(replaceFrom)
		to, err2 := parseTime(replaceTo)
		if err1 != nil || err2 != nil {
			return nil, errors.New("replace_from and replace_to must be valid times")
		}
		if _, err := tx.Exec(`DELETE FROM resource_blocks WHERE resource_id=? AND starts_at>=? AND starts_at<?`, resourceID, fmtTime(from), fmtTime(to)); err != nil {
			return nil, err
		}
	}
	out := []Block{}
	now := nowUTC()
	for i, x := range raw {
		m, ok := x.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("blocks[%d] must be an object", i)
		}
		kind := stringArg(m, "kind")
		if kind == "" {
			kind = "shift"
		}
		if kind != "shift" && kind != "off" && kind != "blocked" {
			return nil, fmt.Errorf("blocks[%d].kind must be shift, off or blocked", i)
		}
		s, err1 := parseTime(stringArg(m, "starts_at"))
		e, err2 := parseTime(stringArg(m, "ends_at"))
		if err1 != nil || err2 != nil || !e.After(s) {
			return nil, fmt.Errorf("blocks[%d]: starts_at and ends_at must be valid and ordered", i)
		}
		r, err := tx.Exec(`INSERT INTO resource_blocks(project_id,resource_id,kind,starts_at,ends_at,note,created_at) VALUES(?,?,?,?,?,?,?)`,
			pid, resourceID, kind, fmtTime(s), fmtTime(e), stringArg(m, "note"), now)
		if err != nil {
			return nil, err
		}
		id, _ := r.LastInsertId()
		out = append(out, Block{ID: id, ResourceID: resourceID, Kind: kind, StartsAt: fmtTime(s), EndsAt: fmtTime(e), Note: stringArg(m, "note")})
	}
	return out, tx.Commit()
}

func listBlocks(db *sql.DB, resourceID int64, from, to string) ([]Block, error) {
	rows, err := db.Query(`SELECT id,resource_id,kind,starts_at,ends_at,note FROM resource_blocks
		WHERE resource_id=? AND ends_at>? AND starts_at<? ORDER BY starts_at`, resourceID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Block{}
	for rows.Next() {
		var b Block
		if err := rows.Scan(&b.ID, &b.ResourceID, &b.Kind, &b.StartsAt, &b.EndsAt, &b.Note); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ─── service objects ────────────────────────────────────────────────

type Object struct {
	ID             int64           `json:"id"`
	SiteID         int64           `json:"site_id"`
	Kind           string          `json:"kind"`
	Name           string          `json:"name"`
	Attributes     json.RawMessage `json:"attributes"`
	InstalledOn    string          `json:"installed_on,omitempty"`
	WarrantyUntil  string          `json:"warranty_until,omitempty"`
	LastServicedAt string          `json:"last_serviced_at,omitempty"`
	CreatedAt      string          `json:"created_at"`
	UpdatedAt      string          `json:"updated_at"`
}

const objectCols = `id,site_id,kind,name,attributes_json,installed_on,warranty_until,last_serviced_at,created_at,updated_at`

func scanObject(s scanner) (*Object, error) {
	var o Object
	var attrs string
	if err := s.Scan(&o.ID, &o.SiteID, &o.Kind, &o.Name, &attrs, &o.InstalledOn, &o.WarrantyUntil, &o.LastServicedAt, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	o.Attributes = json.RawMessage(attrs)
	return &o, nil
}

func getObject(db *sql.DB, pid string, id int64) (*Object, error) {
	return scanObject(db.QueryRow(`SELECT `+objectCols+` FROM objects WHERE project_id=? AND id=?`, pid, id))
}

func upsertObject(db *sql.DB, pid string, args map[string]any) (*Object, error) {
	id := int64Arg(args, "id")
	var o *Object
	if id > 0 {
		c, err := getObject(db, pid, id)
		if err != nil {
			return nil, notFound(err, "object")
		}
		o = c
	} else {
		o = &Object{Attributes: json.RawMessage("{}")}
		o.SiteID = int64Arg(args, "site_id")
		if _, err := getSite(db, pid, o.SiteID); err != nil {
			return nil, notFound(err, "site")
		}
	}
	for k, dst := range map[string]*string{"kind": &o.Kind, "name": &o.Name, "installed_on": &o.InstalledOn,
		"warranty_until": &o.WarrantyUntil, "last_serviced_at": &o.LastServicedAt} {
		if hasArg(args, k) {
			*dst = stringArg(args, k)
		}
	}
	if o.Kind == "" || o.Name == "" {
		return nil, errors.New("kind and name required")
	}
	if a := objectArg(args, "attributes"); a != nil {
		o.Attributes = json.RawMessage(mustJSON(a))
	}
	now := nowUTC()
	if id > 0 {
		_, err := db.Exec(`UPDATE objects SET kind=?,name=?,attributes_json=?,installed_on=?,warranty_until=?,last_serviced_at=?,updated_at=?
			WHERE project_id=? AND id=?`, o.Kind, o.Name, string(o.Attributes), o.InstalledOn, o.WarrantyUntil, o.LastServicedAt, now, pid, id)
		if err != nil {
			return nil, err
		}
		return getObject(db, pid, id)
	}
	r, err := db.Exec(`INSERT INTO objects(project_id,site_id,kind,name,attributes_json,installed_on,warranty_until,last_serviced_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, pid, o.SiteID, o.Kind, o.Name, string(o.Attributes), o.InstalledOn, o.WarrantyUntil, o.LastServicedAt, now, now)
	if err != nil {
		return nil, err
	}
	nid, _ := r.LastInsertId()
	return getObject(db, pid, nid)
}

func listObjects(db *sql.DB, pid string, siteID int64, kind string) ([]*Object, error) {
	query := `SELECT ` + objectCols + ` FROM objects WHERE project_id=?`
	args := []any{pid}
	if siteID > 0 {
		query += ` AND site_id=?`
		args = append(args, siteID)
	}
	if kind != "" {
		query += ` AND kind=?`
		args = append(args, kind)
	}
	rows, err := db.Query(query+` ORDER BY site_id, kind, name LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Object{}
	for rows.Next() {
		o, err := scanObject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
