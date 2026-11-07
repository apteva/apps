package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// A post owns one destination outcome shared by one or more assets.
type Publication struct {
	PublicationAudience
	RecordedAudiences    []PublicationAudience `json:"recorded_audiences"`
	HistoricallyVerified bool                  `json:"historically_verified"`
	VerifiedAudiences    []PublicationAudience `json:"verified_audiences"`
	Revision             int64                 `json:"revision"`
	ObservedAt           string                `json:"observed_at"`
	CreatedAt            string                `json:"created_at"`
	ID                   string                `json:"id"`
	AssetID              string                `json:"asset_id,omitempty"`
	BrandID              string                `json:"brand_id"`
	Title                string                `json:"title"`
	AssetIDs             []string              `json:"asset_ids"`
	Destination          string                `json:"destination"`
	AccountRef           string                `json:"account_ref"`
	Audience             string                `json:"audience"`
	Status               string                `json:"status"`
	PlannedAt            string                `json:"planned_at"`
	ActualAt             string                `json:"actual_at"`
	ExternalPostID       string                `json:"external_post_id"`
	ExternalURL          string                `json:"external_url"`
	EvidenceSource       string                `json:"evidence_source"`
	FailureDetails       string                `json:"failure_details"`
	LegacyTargetID       string                `json:"legacy_target_id"`
}

const postColumns = `id,brand_id,title,destination,account_ref,audience,status,planned_at,actual_at,external_post_id,external_url,evidence_source,failure_details,legacy_target_id,audience_type,access_policy,group_scope,group_ids_json,group_labels_json,publisher_ref_json,observed_at,revision,created_at`

func scanPost(rows *sql.Rows, prefix ...any) (Publication, error) {
	var p Publication
	var ids, labels, publisher string
	err := rows.Scan(append(prefix, &p.ID, &p.BrandID, &p.Title, &p.Destination, &p.AccountRef, &p.Audience, &p.Status, &p.PlannedAt, &p.ActualAt, &p.ExternalPostID, &p.ExternalURL, &p.EvidenceSource, &p.FailureDetails, &p.LegacyTargetID, &p.AudienceType, &p.AccessPolicy, &p.GroupScope, &ids, &labels, &publisher, &p.ObservedAt, &p.Revision, &p.CreatedAt)...)
	p.AssetIDs = []string{}
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(ids), &p.GroupIDs); err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(labels), &p.GroupLabels); err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(publisher), &p.PublisherRef); err != nil {
		return p, err
	}
	return p, err
}

func loadPostAssets(db publicationDB, pid string, p *Publication) error {
	rows, err := db.Query(`SELECT asset_id FROM post_assets WHERE project_id=? AND post_id=? ORDER BY position`, pid, p.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	p.AssetIDs = []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		p.AssetIDs = append(p.AssetIDs, id)
	}
	return rows.Err()
}

func postByID(db publicationDB, pid, id string) (*Publication, error) {
	rows, err := db.Query(`SELECT `+postColumns+` FROM posts WHERE project_id=? AND id=?`, pid, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, errors.New("post not found")
	}
	p, err := scanPost(rows)
	if err != nil {
		return nil, err
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = loadPostAssets(db, pid, &p); err != nil {
		return nil, err
	}
	return &p, loadVerifiedAudiences(db, pid, []*Publication{&p})
}

func publicationsForAsset(db *sql.DB, pid, assetID string) ([]Publication, error) {
	rows, err := db.Query(`SELECT `+postColumns+` FROM posts WHERE project_id=? AND id IN
		(SELECT post_id FROM post_assets WHERE project_id=? AND asset_id=?) ORDER BY created_at DESC,id DESC`, pid, pid, assetID)
	if err != nil {
		return nil, err
	}
	out := []Publication{}
	for rows.Next() {
		p, e := scanPost(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		p.AssetID = assetID
		out = append(out, p)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}

	refs := []*Publication{}
	for i := range out {
		refs = append(refs, &out[i])
	}
	if err = loadPostMemberships(db, pid, refs); err != nil {
		return nil, err
	}
	return out, loadVerifiedAudiences(db, pid, refs)
}

func (a *App) assetPublicationsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "asset_id"); err != nil {
		return nil, err
	}
	if _, err = assetByID(ctx.AppDB(), pid, str(args, "asset_id")); err != nil {
		return nil, err
	}
	result, err := a.postsList(ctx, args)
	if err != nil {
		return nil, err
	}
	out := result.(map[string]any)
	out["publications"] = out["posts"]
	delete(out, "posts")
	return out, nil
}

func (a *App) postsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := lifecycleScope(args)
	if err != nil {
		return nil, err
	}
	f, err := parsePublicationFilter(args)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + postColumns + ` FROM posts p WHERE p.project_id=?`
	archivedMember := `EXISTS (SELECT 1 FROM post_assets pa JOIN assets a ON a.id=pa.asset_id AND a.project_id=pa.project_id JOIN sessions s ON s.id=a.session_id AND s.project_id=a.project_id WHERE pa.project_id=p.project_id AND pa.post_id=p.id AND (a.lifecycle='archived' OR s.lifecycle='archived'))`
	if scope == "active" {
		q += " AND NOT " + archivedMember
	}
	if scope == "archived" {
		q += " AND " + archivedMember
	}
	values := []any{pid}
	if id := str(args, "session_id"); id != "" {
		if _, err := sessionByID(ctx.AppDB(), pid, id); err != nil {
			return nil, err
		}
		q += ` AND EXISTS (SELECT 1 FROM post_assets pa JOIN assets a ON a.id=pa.asset_id AND a.project_id=pa.project_id WHERE pa.project_id=p.project_id AND pa.post_id=p.id AND a.session_id=?)`
		values = append(values, id)
	}
	if id := str(args, "asset_id"); id != "" {
		if _, err := assetByID(ctx.AppDB(), pid, id); err != nil {
			return nil, err
		}
		q += ` AND EXISTS (SELECT 1 FROM post_assets pa WHERE pa.project_id=p.project_id AND pa.post_id=p.id AND pa.asset_id=?)`
		values = append(values, id)
	}
	if id := str(args, "brand_id"); id != "" {
		q += ` AND p.brand_id=?`
		values = append(values, id)
	}
	predicate, bindings := f.predicate("p")
	q += predicate
	values = append(values, bindings...)
	pageLimit := int64(200)
	if raw, ok := args["limit"]; ok {
		pageLimit, err = sourceInteger(raw, 200)
		if err != nil || pageLimit < 1 || pageLimit > 200 {
			return nil, errors.New("limit must be 1–200")
		}
	}
	if cursor := str(args, "cursor"); cursor != "" {
		c, e := decodeSearchCursor(cursor)
		if e != nil || c.CreatedAt == "" {
			return nil, errors.New("invalid post cursor")
		}
		q += ` AND (p.created_at<? OR (p.created_at=? AND p.id<?))`
		values = append(values, c.CreatedAt, c.CreatedAt, c.ID)
	}
	q += ` ORDER BY p.created_at DESC,p.id DESC LIMIT ?`
	values = append(values, pageLimit+1)
	rows, err := ctx.AppDB().Query(q, values...)
	if err != nil {
		return nil, err
	}
	out := []Publication{}
	for rows.Next() {
		p, e := scanPost(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, p)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}

	refs := []*Publication{}
	for i := range out {
		refs = append(refs, &out[i])
	}
	if err = loadPostMemberships(ctx.AppDB(), pid, refs); err != nil {
		return nil, err
	}
	if err = loadVerifiedAudiences(ctx.AppDB(), pid, refs); err != nil {
		return nil, err
	}
	next := ""
	if len(out) > int(pageLimit) {
		out = out[:pageLimit]
		last := out[len(out)-1]
		next = encodeSearchCursor("", last.CreatedAt, last.ID)
	}
	return map[string]any{"posts": out, "next_cursor": next}, nil
}

func (a *App) postsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "id"); err != nil {
		return nil, err
	}
	p, err := postByID(ctx.AppDB(), pid, str(args, "id"))
	if err != nil {
		return nil, err
	}
	history, err := publicationHistory(ctx.AppDB(), pid, p.ID)
	return map[string]any{"post": p, "history": history}, err
}

func assetIDsArg(v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	var ids []string
	switch x := v.(type) {
	case []string:
		ids = x
	case []any:
		for _, raw := range x {
			id, ok := raw.(string)
			if !ok {
				return nil, errors.New("asset_ids must contain strings")
			}
			ids = append(ids, id)
		}
	default:
		return nil, errors.New("asset_ids must be an array")
	}
	if len(ids) == 0 {
		return nil, errors.New("a post needs at least one asset")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || seen[id] {
			return nil, errors.New("asset_ids must be distinct nonempty IDs")
		}
		seen[id] = true
	}
	return ids, nil
}

func validatePostAssets(db publicationDB, pid, brandID string, ids []string) error {
	if len(ids) == 0 {
		return errors.New("a post needs at least one asset")
	}
	for _, id := range ids {
		var assetBrand string
		err := db.QueryRow(`SELECT s.brand_id FROM assets a JOIN sessions s ON s.id=a.session_id AND s.project_id=a.project_id WHERE a.project_id=? AND a.id=?`, pid, id).Scan(&assetBrand)
		if err != nil {
			return fmt.Errorf("asset %s not found", id)
		}
		if assetBrand != brandID {
			return errors.New("all post assets must belong to the same brand")
		}
	}
	return nil
}

func applyPostField(args map[string]any, key string, current *string) {
	if _, ok := args[key]; ok {
		*current = str(args, key)
	}
}

// postsRecord creates or updates a Catalog post and its ordered membership.
// Every edit appends an audit snapshot. It never publishes externally.
func (a *App) postsRecord(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "status"); err != nil {
		return nil, err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id := str(args, "post_id")
	reconciled := false
	// Acquire the SQLite writer lock before identity lookup, guards and validation.
	if _, err = tx.Exec(`UPDATE posts SET revision=revision WHERE project_id=? AND id=?`, pid, id); err != nil {
		return nil, err
	}
	destination := strings.ToLower(str(args, "destination"))
	account, external := str(args, "account_ref"), str(args, "external_post_id")
	if id == "" && destination != "" && account != "" && external != "" {
		rows, e := tx.Query(`SELECT id FROM posts WHERE project_id=? AND LOWER(destination)=? AND account_ref=? AND external_post_id=?`, pid, destination, account, external)
		if e != nil {
			return nil, e
		}
		matches := []string{}
		for rows.Next() {
			var found string
			if e = rows.Scan(&found); e != nil {
				rows.Close()
				return nil, e
			}
			matches = append(matches, found)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		if len(matches) > 1 {
			return nil, errors.New("external post identity has legacy duplicates; review existing records before reconciliation")
		}
		if len(matches) == 1 {
			id = matches[0]
			reconciled = true
		}
	}
	creating := id == ""
	var p, previous Publication
	if creating {
		if destination == "" {
			return nil, errors.New("destination required")
		}
		id = newID()
		p = Publication{ID: id, Destination: destination, Revision: 1, CreatedAt: now()}
	} else {
		old, e := postByID(tx, pid, id)
		if e != nil {
			return nil, e
		}
		p = *old
		previous = *old
		if destination != "" && destination != strings.ToLower(p.Destination) {
			return nil, errors.New("post destination is immutable")
		}
	}
	for key, field := range map[string]*string{"title": &p.Title, "account_ref": &p.AccountRef, "audience": &p.Audience, "status": &p.Status, "planned_at": &p.PlannedAt, "actual_at": &p.ActualAt, "observed_at": &p.ObservedAt, "external_post_id": &p.ExternalPostID, "external_url": &p.ExternalURL, "evidence_source": &p.EvidenceSource, "failure_details": &p.FailureDetails} {
		applyPostField(args, key, field)
	}
	if err = applyPublicationAudience(args, &p); err != nil {
		return nil, err
	}
	if !oneOf(p.Status, "planned", "scheduled", "submitted", "provider_reported_published", "verified_published", "failed", "removed", "unknown") {
		return nil, errors.New("invalid post status")
	}
	if p.ExternalURL != "" {
		u, e := url.Parse(p.ExternalURL)
		if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, errors.New("external_url must be an absolute HTTP(S) URL")
		}
	}
	if p.Status == "verified_published" && p.ExternalPostID == "" && p.ExternalURL == "" {
		return nil, errors.New("verified publication requires a post URL or ID")
	}
	if p.Status != "planned" && strings.TrimSpace(p.EvidenceSource) == "" {
		return nil, errors.New("observed status requires evidence_source")
	}
	for _, v := range []string{p.PlannedAt, p.ActualAt, p.ObservedAt} {
		if v != "" {
			if _, e := time.Parse(time.RFC3339, v); e != nil {
				return nil, errors.New("post times must be RFC3339")
			}
		}
	}
	ids := p.AssetIDs
	if raw, ok := args["asset_ids"]; ok {
		ids, err = assetIDsArg(raw)
		if err != nil {
			return nil, err
		}
	}
	if len(ids) > 100 {
		return nil, errors.New("at most 100 post assets")
	}
	if creating {
		if len(ids) == 0 {
			return nil, errors.New("asset_ids required")
		}
		if err = tx.QueryRow(`SELECT s.brand_id FROM assets a JOIN sessions s ON s.id=a.session_id AND s.project_id=a.project_id WHERE a.project_id=? AND a.id=?`, pid, ids[0]).Scan(&p.BrandID); err != nil {
			return nil, errors.New("first asset not found")
		}
	}
	if err = validatePostAssets(tx, pid, p.BrandID, ids); err != nil {
		return nil, err
	}
	if oneOf(p.Status, "planned", "scheduled", "submitted") {
		for _, assetID := range ids {
			var state, parent, role string
			if err = tx.QueryRow(`SELECT a.lifecycle,s.lifecycle,a.role FROM assets a JOIN sessions s ON s.id=a.session_id AND s.project_id=a.project_id WHERE a.project_id=? AND a.id=?`, pid, assetID).Scan(&state, &parent, &role); err != nil {
				return nil, err
			}
			if state != "active" || parent != "active" || role == "intermediate" {
				return nil, errors.New("archived or intermediate asset cannot be selected for new publication")
			}
		}
	}
	p.AssetIDs = append([]string{}, ids...)
	if !creating {
		var verified int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM posts p WHERE p.project_id=? AND p.id=? AND (p.status='verified_published' OR EXISTS(SELECT 1 FROM post_events e WHERE e.project_id=p.project_id AND e.post_id=p.id AND e.status='verified_published'))`, pid, id).Scan(&verified); err != nil {
			return nil, err
		}
		if verified > 0 && (!sameAssetSet(ids, previous.AssetIDs) || (previous.AccountRef != "" && p.AccountRef != previous.AccountRef) || (previous.ExternalPostID != "" && p.ExternalPostID != previous.ExternalPostID)) {
			return nil, errors.New("assets and external identity on a historically verified post cannot be changed")
		}
		// Repeated imports with the exact saved outcome are a truthful no-op even
		// when the caller's expected revision predates the successful first attempt.
		if reflect.DeepEqual(p, previous) {
			return map[string]any{"post_id": id, "publication_id": id, "status": p.Status, "revision": p.Revision, "post": p, "unchanged": true, "reconciled": reconciled}, nil
		}
		audienceChanged := !reflect.DeepEqual(p.PublicationAudience, previous.PublicationAudience) || p.Audience != previous.Audience
		identityChanged := (previous.AccountRef != "" && p.AccountRef != previous.AccountRef) || (previous.ExternalPostID != "" && p.ExternalPostID != previous.ExternalPostID)
		identityFilled := verified > 0 && (p.AccountRef != previous.AccountRef || p.ExternalPostID != previous.ExternalPostID)
		if (audienceChanged || identityChanged || identityFilled) && args["expected_revision"] == nil {
			return nil, errors.New("audience or external identity changes require expected_revision")
		}
		if raw, ok := args["expected_revision"]; ok {
			expected, e := sourceInteger(raw, 0)
			if e != nil || expected != previous.Revision {
				return nil, errors.New("post revision conflict; reload the post")
			}
		}
		if (audienceChanged || identityFilled) && p.Status == "verified_published" && str(args, "evidence_source") == "" {
			return nil, errors.New("verified audience changes require explicit new evidence_source")
		}
		p.Revision++
	}
	p.RecordedAudiences = appendAudience(p.RecordedAudiences, p.PublicationAudience)
	if p.Status == "verified_published" {
		p.HistoricallyVerified = true
		known := false
		for _, saved := range p.VerifiedAudiences {
			if reflect.DeepEqual(saved, p.PublicationAudience) {
				known = true
			}
		}
		if !known {
			p.VerifiedAudiences = append([]PublicationAudience{p.PublicationAudience}, p.VerifiedAudiences...)
		}
	}
	if p.Status != "planned" && str(args, "observed_at") == "" {
		p.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	// Bind exactly one scoped external identity; old duplicate rows remain intact.
	if p.AccountRef != "" && p.ExternalPostID != "" {
		var other string
		e := tx.QueryRow(`SELECT id FROM posts WHERE project_id=? AND LOWER(destination)=? AND account_ref=? AND external_post_id=? AND id<>? LIMIT 1`, pid, strings.ToLower(p.Destination), p.AccountRef, p.ExternalPostID, id).Scan(&other)
		if e == nil {
			return nil, errors.New("external post identity already belongs to another record")
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
	}
	if creating {
		_, err = tx.Exec(`INSERT INTO posts(id,project_id,brand_id,title,destination,account_ref,audience,status,planned_at,actual_at,external_post_id,external_url,evidence_source,failure_details,audience_type,access_policy,group_scope,group_ids_json,group_labels_json,publisher_ref_json,observed_at,revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, pid, p.BrandID, p.Title, p.Destination, p.AccountRef, p.Audience, p.Status, p.PlannedAt, p.ActualAt, p.ExternalPostID, p.ExternalURL, p.EvidenceSource, p.FailureDetails, p.AudienceType, p.AccessPolicy, p.GroupScope, jsonText(p.GroupIDs), jsonText(p.GroupLabels), jsonText(p.PublisherRef), p.ObservedAt, p.Revision)
	} else {
		var res sql.Result
		res, err = tx.Exec(`UPDATE posts SET title=?,account_ref=?,audience=?,status=?,planned_at=?,actual_at=?,external_post_id=?,external_url=?,evidence_source=?,failure_details=?,audience_type=?,access_policy=?,group_scope=?,group_ids_json=?,group_labels_json=?,publisher_ref_json=?,observed_at=?,revision=?,updated_at=? WHERE project_id=? AND id=? AND revision=?`, p.Title, p.AccountRef, p.Audience, p.Status, p.PlannedAt, p.ActualAt, p.ExternalPostID, p.ExternalURL, p.EvidenceSource, p.FailureDetails, p.AudienceType, p.AccessPolicy, p.GroupScope, jsonText(p.GroupIDs), jsonText(p.GroupLabels), jsonText(p.PublisherRef), p.ObservedAt, p.Revision, now(), pid, id, previous.Revision)
		if err == nil {
			n, e := res.RowsAffected()
			if e != nil || n != 1 {
				return nil, errors.New("post revision conflict")
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`DELETE FROM post_external_identities WHERE project_id=? AND post_id=?`, pid, id); err != nil {
		return nil, err
	}
	if p.AccountRef != "" && p.ExternalPostID != "" {
		if _, err = tx.Exec(`INSERT INTO post_external_identities(project_id,destination,account_ref,external_post_id,post_id) VALUES(?,?,?,?,?)`, pid, strings.ToLower(p.Destination), p.AccountRef, p.ExternalPostID, id); err != nil {
			return nil, err
		}
	}
	if !creating {
		if _, err = tx.Exec(`DELETE FROM post_assets WHERE project_id=? AND post_id=?`, pid, id); err != nil {
			return nil, err
		}
	}
	for i, assetID := range ids {
		if _, err = tx.Exec(`INSERT INTO post_assets(project_id,post_id,asset_id,position) VALUES(?,?,?,?)`, pid, id, assetID, i); err != nil {
			return nil, err
		}
	}
	_, err = tx.Exec(`INSERT INTO post_events(id,project_id,post_id,status,external_post_id,external_url,actual_at,evidence_source,failure_details,asset_ids_json,observed_at,audience,audience_type,access_policy,group_scope,group_ids_json,group_labels_json,publisher_ref_json,revision,before_json,after_json,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, newID(), pid, id, p.Status, p.ExternalPostID, p.ExternalURL, p.ActualAt, p.EvidenceSource, p.FailureDetails, jsonText(ids), p.ObservedAt, p.Audience, p.AudienceType, p.AccessPolicy, p.GroupScope, jsonText(p.GroupIDs), jsonText(p.GroupLabels), jsonText(p.PublisherRef), p.Revision, jsonText(previous), jsonText(p), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	ctx.EmitWithProject("content-catalog.publication.observed", pid, map[string]any{"post_id": id, "asset_ids": ids, "status": p.Status})
	return map[string]any{"post_id": id, "publication_id": id, "status": p.Status, "revision": p.Revision, "post": p, "unchanged": false, "reconciled": reconciled}, nil
}

func sameAssetSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, id := range a {
		set[id] = true
	}
	for _, id := range b {
		if !set[id] {
			return false
		}
	}
	return true
}

// Existing single-asset callers create one-asset posts. Updating an existing
// post ID changes its shared status, including all attached assets.
func (a *App) assetPublicationRecord(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "asset_id", "status"); err != nil {
		return nil, err
	}
	assetID := str(args, "asset_id")
	if _, err = assetByID(ctx.AppDB(), pid, assetID); err != nil {
		return nil, err
	}
	copyArgs := map[string]any{}
	for key, value := range args {
		copyArgs[key] = value
	}
	if id := str(args, "publication_id"); id != "" {
		p, e := postByID(ctx.AppDB(), pid, id)
		if e != nil {
			return nil, e
		}
		found := false
		for _, member := range p.AssetIDs {
			if member == assetID {
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("asset is not part of that post")
		}
		copyArgs["post_id"] = id
	} else {
		copyArgs["asset_ids"] = []string{assetID}
	}
	return a.postsRecord(ctx, copyArgs)
}
