package main

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ─── brands ─────────────────────────────────────────────────────────
//
// A brand is what the customer sees: its name, contact details, price
// list, policies and job numbers. Config rows (work types, forms, rate
// items, modifiers, policies, packs) carry brand_id: 0 = shared by all
// brands, otherwise owned by that brand. Lookups by key prefer the
// brand's own row, then the shared one. Resources are shared unless
// they list brand_ids. A project with no brands works as before.

type Brand struct {
	ID           int64  `json:"id"`
	Key          string `json:"key"`
	Name         string `json:"name"`
	LegalName    string `json:"legal_name,omitempty"`
	Email        string `json:"email,omitempty"`
	Phone        string `json:"phone,omitempty"`
	Website      string `json:"website,omitempty"`
	LogoFileID   *int64 `json:"logo_file_id,omitempty"`
	NumberPrefix string `json:"number_prefix"`
	Playbook     string `json:"playbook,omitempty"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

const brandCols = `id,key,name,legal_name,email,phone,website,logo_file_id,number_prefix,playbook,status,created_at,updated_at`

// defaultNumberPrefix is used by work without a brand. Brands may not
// take it, so every job number says which brand it belongs to.
const defaultNumberPrefix = "W"

var brandKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
var prefixRe = regexp.MustCompile(`^[A-Z0-9]{1,8}$`)

func scanBrand(s scanner) (*Brand, error) {
	var b Brand
	var logo sql.NullInt64
	if err := s.Scan(&b.ID, &b.Key, &b.Name, &b.LegalName, &b.Email, &b.Phone, &b.Website, &logo, &b.NumberPrefix,
		&b.Playbook, &b.Status, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return nil, err
	}
	if logo.Valid {
		b.LogoFileID = &logo.Int64
	}
	return &b, nil
}

func getBrand(db *sql.DB, pid string, id int64) (*Brand, error) {
	return scanBrand(db.QueryRow(`SELECT `+brandCols+` FROM brands WHERE project_id=? AND id=?`, pid, id))
}

func getBrandByKey(db *sql.DB, pid, key string) (*Brand, error) {
	return scanBrand(db.QueryRow(`SELECT `+brandCols+` FROM brands WHERE project_id=? AND key=?`, pid, strings.ToLower(strings.TrimSpace(key))))
}

func listBrands(db *sql.DB, pid, status string) ([]*Brand, error) {
	q := `SELECT ` + brandCols + ` FROM brands WHERE project_id=?`
	args := []any{pid}
	if status != "" {
		q += ` AND status=?`
		args = append(args, status)
	}
	rows, err := db.Query(q+` ORDER BY name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Brand{}
	for rows.Next() {
		b, err := scanBrand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func upsertBrand(db *sql.DB, pid string, args map[string]any) (*Brand, error) {
	var cur *Brand
	if id := int64Arg(args, "id"); id > 0 {
		b, err := getBrand(db, pid, id)
		if err != nil {
			return nil, notFound(err, "brand")
		}
		cur = b
	} else if k := stringArg(args, "key"); k != "" {
		if b, err := getBrandByKey(db, pid, k); err == nil {
			cur = b
		}
	}
	isNew := cur == nil
	if isNew {
		key := strings.ToLower(strings.TrimSpace(stringArg(args, "key")))
		if !brandKeyRe.MatchString(key) {
			return nil, errors.New("key required: lowercase letters, digits, - or _ (e.g. flowfix)")
		}
		cur = &Brand{Key: key, Status: "active"}
	}
	set := func(field *string, name string) {
		if hasArg(args, name) {
			*field = strings.TrimSpace(stringArg(args, name))
		}
	}
	set(&cur.Name, "name")
	set(&cur.LegalName, "legal_name")
	set(&cur.Email, "email")
	set(&cur.Phone, "phone")
	set(&cur.Website, "website")
	set(&cur.Playbook, "playbook")
	if cur.Name == "" {
		return nil, errors.New("name required")
	}
	if hasArg(args, "logo_file_id") {
		v := int64Arg(args, "logo_file_id")
		if v > 0 {
			cur.LogoFileID = &v
		} else {
			cur.LogoFileID = nil
		}
	}
	if v := stringArg(args, "status"); v != "" {
		if v != "active" && v != "archived" {
			return nil, errors.New("status must be active or archived")
		}
		cur.Status = v
	}
	if hasArg(args, "number_prefix") {
		cur.NumberPrefix = strings.ToUpper(strings.TrimSpace(stringArg(args, "number_prefix")))
	}
	if cur.NumberPrefix == "" {
		cur.NumberPrefix = derivePrefix(cur.Key)
	}
	if !prefixRe.MatchString(cur.NumberPrefix) {
		return nil, errors.New("number_prefix must be 1-8 letters or digits (e.g. FF)")
	}
	if cur.NumberPrefix == defaultNumberPrefix {
		return nil, fmt.Errorf("number_prefix %s is used for work without a brand; pick another", defaultNumberPrefix)
	}
	var clash string
	_ = db.QueryRow(`SELECT key FROM brands WHERE project_id=? AND number_prefix=? AND key<>?`, pid, cur.NumberPrefix, cur.Key).Scan(&clash)
	if clash != "" {
		return nil, fmt.Errorf("number_prefix %s is already used by brand %s", cur.NumberPrefix, clash)
	}
	if !isNew {
		// Changing a prefix would leave a brand's numbers in two series.
		var used int
		_ = db.QueryRow(`SELECT COUNT(*) FROM work WHERE project_id=? AND brand_id=? AND number NOT LIKE ?`, pid, cur.ID, cur.NumberPrefix+"-%").Scan(&used)
		if used > 0 {
			return nil, errors.New("number_prefix cannot change once the brand has work")
		}
	}
	now := nowUTC()
	var logo any
	if cur.LogoFileID != nil {
		logo = *cur.LogoFileID
	}
	if isNew {
		r, err := db.Exec(`INSERT INTO brands(project_id,key,name,legal_name,email,phone,website,logo_file_id,number_prefix,playbook,status,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, pid, cur.Key, cur.Name, cur.LegalName, cur.Email, cur.Phone, cur.Website, logo,
			cur.NumberPrefix, cur.Playbook, cur.Status, now, now)
		if err != nil {
			return nil, err
		}
		id, _ := r.LastInsertId()
		return getBrand(db, pid, id)
	}
	if _, err := db.Exec(`UPDATE brands SET name=?,legal_name=?,email=?,phone=?,website=?,logo_file_id=?,number_prefix=?,playbook=?,status=?,updated_at=?
		WHERE project_id=? AND id=?`, cur.Name, cur.LegalName, cur.Email, cur.Phone, cur.Website, logo, cur.NumberPrefix, cur.Playbook,
		cur.Status, now, pid, cur.ID); err != nil {
		return nil, err
	}
	return getBrand(db, pid, cur.ID)
}

// derivePrefix turns a key into a short job-number prefix:
// "flowfix" → "FLO", "bright-clean" → "BC".
func derivePrefix(key string) string {
	parts := strings.FieldsFunc(strings.ToUpper(key), func(r rune) bool { return r == '-' || r == '_' })
	p := ""
	if len(parts) > 1 {
		for _, s := range parts {
			p += s[:1]
		}
	} else if len(parts) == 1 {
		p = parts[0]
		if len(p) > 3 {
			p = p[:3]
		}
	}
	if len(p) > 8 {
		p = p[:8]
	}
	if p == defaultNumberPrefix {
		p += "B"
	}
	return p
}

// brandRef reads the brand an argument names: "brand" (key) or
// "brand_id". Returns 0 when neither is given. Archived brands are
// refused unless allowArchived (reads, edits).
func brandRef(db *sql.DB, pid string, args map[string]any, allowArchived bool) (int64, error) {
	var b *Brand
	var err error
	switch {
	case stringArg(args, "brand") != "":
		b, err = getBrandByKey(db, pid, stringArg(args, "brand"))
		if err != nil {
			return 0, notFound(err, "brand "+stringArg(args, "brand"))
		}
	case int64Arg(args, "brand_id") > 0:
		b, err = getBrand(db, pid, int64Arg(args, "brand_id"))
		if err != nil {
			return 0, notFound(err, "brand")
		}
	default:
		return 0, nil
	}
	if b.Status == "archived" && !allowArchived {
		return 0, fmt.Errorf("brand %s is archived", b.Key)
	}
	return b.ID, nil
}

// brandIDsArg reads resource brands: "brands" (keys) or "brand_ids".
// ok is false when neither argument is present.
func brandIDsArg(db *sql.DB, pid string, args map[string]any) ([]int64, bool, error) {
	if keys := arrayArg(args, "brands"); keys != nil || hasArg(args, "brands") {
		out := []int64{}
		for _, k := range keys {
			s, _ := k.(string)
			b, err := getBrandByKey(db, pid, s)
			if err != nil {
				return nil, true, notFound(err, "brand "+s)
			}
			out = append(out, b.ID)
		}
		return uniqueIDs(out), true, nil
	}
	if hasArg(args, "brand_ids") {
		ids := int64sArg(args, "brand_ids")
		for _, id := range ids {
			if _, err := getBrand(db, pid, id); err != nil {
				return nil, true, notFound(err, fmt.Sprintf("brand %d", id))
			}
		}
		return uniqueIDs(ids), true, nil
	}
	return nil, false, nil
}

func uniqueIDs(ids []int64) []int64 {
	seen := map[int64]bool{}
	out := []int64{}
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func brandKey(db *sql.DB, pid string, id int64) string {
	if id == 0 {
		return ""
	}
	var k string
	_ = db.QueryRow(`SELECT key FROM brands WHERE project_id=? AND id=?`, pid, id).Scan(&k)
	return k
}

func activeBrandIDs(db *sql.DB, pid string) []int64 {
	rows, err := db.Query(`SELECT id FROM brands WHERE project_id=? AND status='active' ORDER BY id`, pid)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		out = append(out, id)
	}
	return out
}

// BrandError says a work item's brand could not be settled; Options
// lists the brands the caller can choose from.
type BrandError struct {
	Reason  string   `json:"reason"`
	Options []string `json:"options,omitempty"`
}

func (e *BrandError) Error() string {
	if len(e.Options) > 0 {
		return e.Reason + " (brands: " + strings.Join(e.Options, ", ") + ")"
	}
	return e.Reason
}

// resolveWorkBrand settles the brand and work type of new work. The
// first of these wins: the brand asked for, the site's brand, the work
// type's brand, the project's only active brand. Sources that disagree
// are refused rather than guessed.
func resolveWorkBrand(db *sql.DB, pid string, args map[string]any, typeKey string, siteID int64) (int64, *WorkType, error) {
	asked, err := brandRef(db, pid, args, false)
	if err != nil {
		return 0, nil, err
	}
	var siteBrand int64
	if siteID > 0 {
		s, err := getSite(db, pid, siteID)
		if err != nil {
			return 0, nil, notFound(err, "site")
		}
		siteBrand = s.BrandID
	}
	if asked > 0 && siteBrand > 0 && asked != siteBrand {
		return 0, nil, &BrandError{Reason: fmt.Sprintf("site %d belongs to brand %s, not %s", siteID, brandKey(db, pid, siteBrand), brandKey(db, pid, asked))}
	}
	b := asked
	if b == 0 {
		b = siteBrand
	}
	wt, err := findWorkType(db, pid, typeKey, b)
	if err != nil {
		return 0, nil, err
	}
	if wt.BrandID > 0 && b > 0 && wt.BrandID != b {
		return 0, nil, &BrandError{Reason: fmt.Sprintf("work type %s belongs to brand %s", wt.Key, brandKey(db, pid, wt.BrandID))}
	}
	if b == 0 {
		b = wt.BrandID
	}
	if b == 0 && siteID == 0 && wt.Mode != "at_customer" {
		// Premises work without a site goes to the business's only own
		// site (see intakeWork); that site's brand settles the work's.
		var n int
		var only int64
		_ = db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(brand_id),0) FROM sites WHERE project_id=? AND kind='own'`, pid).Scan(&n, &only)
		if n == 1 {
			b = only
		}
	}
	if b == 0 {
		active := activeBrandIDs(db, pid)
		switch len(active) {
		case 0:
		case 1:
			b = active[0]
		default:
			opts := []string{}
			for _, id := range active {
				opts = append(opts, brandKey(db, pid, id))
			}
			return 0, nil, &BrandError{Reason: "several brands could take this work; pass brand", Options: opts}
		}
	}
	if b > 0 {
		if br, err := getBrand(db, pid, b); err == nil && br.Status == "archived" {
			return 0, nil, fmt.Errorf("brand %s is archived", br.Key)
		}
	}
	return b, wt, nil
}
