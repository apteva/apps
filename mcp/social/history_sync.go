package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Metadata is observation evidence, never publishing intent. Null means the
// provider did not expose the field. Visibility does not imply membership.
type nativePost struct {
	MetadataUnavailable   bool           `json:"metadata_unavailable,omitempty"`
	ID                    string         `json:"native_post_id"`
	URL                   *string        `json:"url"`
	Title                 *string        `json:"title"`
	Body                  *string        `json:"body"`
	UploadedAt            *string        `json:"uploaded_at"`
	ProviderPublishedAt   *string        `json:"provider_reported_published_at"`
	PublicationTimeSource string         `json:"publication_time_source"`
	PublishedAt           *string        `json:"published_at"`
	ScheduledPublishAt    *string        `json:"scheduled_publish_at,omitempty"`
	Visibility            *string        `json:"visibility"`
	Audience              *string        `json:"audience"`
	ProcessingState       *string        `json:"processing_state"`
	OriginalFilename      *string        `json:"original_filename"`
	Bytes                 *int64         `json:"bytes"`
	DurationSeconds       *float64       `json:"duration_seconds"`
	ObservedAt            string         `json:"observed_at"`
	ProviderPostID        *string        `json:"provider_post_id"`
	MediaURLs             []string       `json:"-"`
	Options               map[string]any `json:"-"`
}
type historyAccount struct {
	destinationAccount
	Project         string
	ProfileID       int64
	PageCredentials string
}

func loadHistoryAccountForRead(ctx *sdk.AppCtx, id int64) (historyAccount, error) {
	out := historyAccount{destinationAccount: destinationAccount{ID: id}, Project: projectScope(ctx)}
	err := ctx.AppDB().QueryRow(`SELECT platform,connection_id,COALESCE(provider_slug,'native'),COALESCE(provider_account_id,''),COALESCE(external_account_id,''),default_destination_id,default_destination_name,account_kind,status,COALESCE(profile_id,0),COALESCE(page_credentials,'') FROM social_accounts WHERE id=? AND project_id=?`, id, out.Project).Scan(&out.Platform, &out.ConnID, &out.Provider, &out.ProviderID, &out.ExternalID, &out.DefaultID, &out.DefaultName, &out.Kind, &out.Status, &out.ProfileID, &out.PageCredentials)
	if err != nil {
		return out, errors.New("Social account not found in this project")
	}
	return out, nil
}
func loadHistoryAccount(ctx *sdk.AppCtx, id int64) (historyAccount, error) {
	out, err := loadHistoryAccountForRead(ctx, id)
	if err != nil {
		return out, err
	}
	if out.Status != "active" {
		return out, errors.New("Social account is not active; reconnect it first")
	}
	return out, nil
}

func (b historyAccount) binding() string {
	raw, _ := json.Marshal([]any{b.Project, b.ID, b.ConnID, b.Platform, b.Provider, b.ProviderID, b.ExternalID, b.Kind})
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

type historyRun struct {
	MetadataUnresolvedIDs   []string               `json:"metadata_unresolved_sample"`
	MetadataUnresolved      int                    `json:"metadata_unresolved"`
	MetadataRetryable       int                    `json:"metadata_retryable"`
	MetadataReadbacks       int                    `json:"metadata_readbacks"`
	CoverageVersion         int                    `json:"coverage_version"`
	ProviderTotal           *int64                 `json:"provider_total"`
	ProviderTotals          []historyProviderTotal `json:"provider_totals"`
	ProviderTotalsChanged   bool                   `json:"provider_totals_changed"`
	Counts                  historyPassCounts      `json:"counts"`
	TraversalExhausted      bool                   `json:"traversal_exhausted"`
	ReconciliationExhausted bool                   `json:"reconciliation_exhausted"`
	ReconciliationFull      bool                   `json:"reconciliation_full"`
	ReconciliationReasons   []string               `json:"reconciliation_reasons"`
	PageSize                int                    `json:"page_size"`
	TraversalPageSize       int                    `json:"traversal_page_size"`
	ReconciliationPageSize  int                    `json:"reconciliation_page_size"`
	LastPageHash            string                 `json:"last_page_hash,omitempty"`
	RepeatedPages           int                    `json:"repeated_pages,omitempty"`
	ID                      string                 `json:"run_id"`
	Mode                    string                 `json:"mode"`
	Status                  string                 `json:"status"`
	Phase                   string                 `json:"phase"`
	Token                   string                 `json:"provider_cursor,omitempty"`
	Resource                string                 `json:"resource,omitempty"`
	Owner                   string                 `json:"owner,omitempty"`
	StartedAt               string                 `json:"started_at"`
	ObservedAt              string                 `json:"observed_at"`
	CompletedAt             *string                `json:"completed_at"`
	Imported                int                    `json:"imported"`
	Updated                 int                    `json:"updated"`
	Unchanged               int                    `json:"unchanged"`
	Scanned                 int                    `json:"scanned"`
	Pages                   int                    `json:"pages"`
	OverlapPages            int                    `json:"overlap_pages"`
	Limitations             []string               `json:"limitations"`
	Scope                   string                 `json:"scope"`
	Incomplete              bool                   `json:"incomplete"`
	Error                   string                 `json:"error,omitempty"`
	CursorHashes            map[string]bool        `json:"cursor_hashes"`
	Revision                int                    `json:"-"`
}
type historyReceipt struct {
	Platform    string         `json:"platform"`
	AccountID   int64          `json:"social_account_id"`
	RunID       string         `json:"run_id"`
	Status      string         `json:"status"`
	ObservedAt  string         `json:"observed_at"`
	CompletedAt *string        `json:"completed_at"`
	Imported    int            `json:"imported"`
	Updated     int            `json:"updated"`
	Unchanged   int            `json:"unchanged"`
	Scanned     int            `json:"scanned"`
	UniquePosts int            `json:"unique_posts"`
	Pages       int            `json:"pages"`
	Coverage    map[string]any `json:"coverage"`
	Cursor      *string        `json:"continuation_cursor"`
	Limitations []string       `json:"limitations"`
	Error       string         `json:"error,omitempty"`
}

var historyLocks sync.Map

func lockHistory(b historyAccount) func() {
	value, _ := historyLocks.LoadOrStore(b.Project+":"+strconv.FormatInt(b.ID, 10), &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}
func historyCursor(run historyRun) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s.%d", run.ID, run.Revision)))
}
func parseHistoryCursor(raw string) (string, int, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", 0, errors.New("invalid history cursor")
	}
	parts := strings.Split(string(b), ".")
	if len(parts) != 2 || len(parts[0]) != 32 {
		return "", 0, errors.New("invalid history cursor")
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 0 {
		return "", 0, errors.New("invalid history cursor")
	}
	return parts[0], n, nil
}
func historySupported(b historyAccount) bool {
	if b.Provider == zernioProviderSlug {
		return true
	}
	if b.Provider != "native" && b.Provider != "" {
		return false
	}
	return importSupportedPlatform(b.Platform) || b.Platform == "linkedin" && strings.HasPrefix(b.ExternalID, "urn:li:")
}
func addHistoryLimitation(run *historyRun, value string) {
	if !containsFold(run.Limitations, value) {
		run.Limitations = append(run.Limitations, value)
	}
}
func saveHistoryRun(ctx *sdk.AppCtx, b historyAccount, run *historyRun) error {
	raw, err := json.Marshal(run)
	if err != nil {
		return err
	}
	res, err := ctx.AppDB().Exec(`UPDATE social_history_runs SET state=?,revision=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND project_id=? AND social_account_id=? AND binding=? AND revision=?`, string(raw), run.Revision, run.ID, b.Project, b.ID, b.binding(), run.Revision-1)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return errors.New("history run binding changed")
	}
	return err
}
func receiptForHistory(ctx *sdk.AppCtx, b historyAccount, run historyRun) historyReceipt {
	var unique int
	_ = ctx.AppDB().QueryRow(`SELECT count(*),COALESCE(sum(imported),0),COALESCE(sum(updated),0),COALESCE(sum(unchanged),0) FROM social_history_seen WHERE run_id=?`, run.ID).Scan(&unique, &run.Imported, &run.Updated, &run.Unchanged)
	out := historyReceipt{Platform: b.Platform, AccountID: b.ID, RunID: run.ID, Status: run.Status, ObservedAt: run.ObservedAt, CompletedAt: run.CompletedAt, Imported: run.Imported, Updated: run.Updated, Unchanged: run.Unchanged, Scanned: run.Scanned, UniquePosts: unique, Pages: run.Pages, Limitations: run.Limitations, Error: run.Error, Coverage: historyCoverage(run)}
	if run.Status == "partial" || run.Status == "failed" {
		cursor := historyCursor(run)
		out.Cursor = &cursor
	}
	return out
}
func (a *App) toolAccountHistorySync(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	id := int64(intArg(args, "social_account_id", 0))
	if id <= 0 {
		return mcpError("social_account_id required"), nil
	}
	b, err := loadHistoryAccount(ctx, id)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	if !historySupported(b) {
		return map[string]any{"social_account_id": id, "status": "unsupported", "coverage": map[string]any{"complete": false}, "limitation": "History listing is not exposed for this channel/provider."}, nil
	}
	mode := toString(args["mode"])
	if mode == "" {
		mode = "recent"
	}
	if mode != "full" && mode != "recent" {
		return mcpError("mode must be full or recent"), nil
	}
	pages := intArg(args, "max_pages", 2)
	if pages < 1 || pages > 5 {
		return mcpError("max_pages must be 1–5"), nil
	}
	pageSize := intArg(args, "page_size", 50)
	if pageSize < 1 || pageSize > 50 {
		return mcpError("page_size must be 1–50"), nil
	}
	unlock := lockHistory(b)
	defer unlock()
	run := historyRun{}
	cursor := toString(args["cursor"])
	if len(cursor) > 200 {
		return mcpError("invalid history cursor"), nil
	}
	if cursor != "" {
		key, revision, e := parseHistoryCursor(cursor)
		if e != nil {
			return mcpError(e.Error()), nil
		}
		var raw, binding string
		var savedRevision int
		e = ctx.AppDB().QueryRow(`SELECT state,binding,revision FROM social_history_runs WHERE id=? AND project_id=? AND social_account_id=?`, key, b.Project, b.ID).Scan(&raw, &binding, &savedRevision)
		if e != nil || binding != b.binding() {
			return mcpError("cursor does not belong to this project/account connection; start a new sync"), nil
		}
		if json.Unmarshal([]byte(raw), &run) != nil {
			return nil, errors.New("invalid stored history state")
		}
		run.Revision = savedRevision
		if run.CoverageVersion != historyCoverageVersion {
			return mcpError("history cursor predates page-boundary/metadata reconciliation; start a new sync"), nil
		}
		if _, explicit := args["page_size"]; explicit && pageSize != run.PageSize {
			return mcpError("cursor page_size differs from original run"), nil
		}
		pageSize = run.PageSize
		traversalSize, reconciliationSize := historyPageSizes(b, pageSize)
		if run.TraversalPageSize != traversalSize || run.ReconciliationPageSize != reconciliationSize {
			return mcpError("history cursor has invalid stored provider page sizes; start a new sync"), nil
		}
		if _, explicit := args["mode"]; explicit && run.Mode != mode {
			return mcpError("cursor mode differs from requested mode"), nil
		}
		// A retried/replayed step returns the latest receipt without advancing again.
		if revision != savedRevision || run.Status == "complete" || run.Status == "limited" {
			return receiptForHistory(ctx, b, run), nil
		}
	} else {
		traversalSize, reconciliationSize := historyPageSizes(b, pageSize)
		run = historyRun{CoverageVersion: historyCoverageVersion, PageSize: pageSize, TraversalPageSize: traversalSize, ReconciliationPageSize: reconciliationSize, ID: randomDeliveryID(), Mode: mode, Status: "partial", Phase: "scan", Scope: "provider_accessible_history", StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Limitations: []string{}, CursorHashes: map[string]bool{}}
		raw, _ := json.Marshal(run)
		_, err = ctx.AppDB().Exec(`INSERT INTO social_history_runs(id,project_id,social_account_id,binding,state) VALUES(?,?,?,?,?)`, run.ID, b.Project, b.ID, b.binding(), string(raw))
		if err != nil {
			return nil, err
		}
	}
	run.Status = "partial"
	run.Error = ""
	deadline := time.Now().Add(25 * time.Second)
	for step := 0; step < pages && time.Now().Before(deadline); step++ {
		if run.Phase == "metadata" {
			e := a.reconcileHistoryMetadata(ctx, b, &run)
			if e != nil {
				run.Status = "failed"
				run.Error = redactDeliverySecrets(e.Error())
			}
			run.Revision++
			if err = saveHistoryRun(ctx, b, &run); err != nil {
				return nil, err
			}
			if run.Status != "partial" {
				break
			}
			continue
		}
		providerPageSize := run.TraversalPageSize
		if run.Phase == "overlap" {
			providerPageSize = run.ReconciliationPageSize
		}
		page, e := a.fetchHistoryPage(ctx, b, &run, providerPageSize)
		if e != nil {
			run.Status = "failed"
			run.Error = redactDeliverySecrets(e.Error())
			run.Revision++
			if err = saveHistoryRun(ctx, b, &run); err != nil {
				return nil, err
			}
			return receiptForHistory(ctx, b, run), nil
		}
		// Verify connection identity again before writing provider observations.
		current, e := loadHistoryAccount(ctx, b.ID)
		if e != nil || current.binding() != b.binding() {
			return mcpError("account connection changed during history sync; start again"), nil
		}
		for _, post := range page.Posts {
			if post.ID == "" {
				continue
			}
			post.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
			result, _, _, e := a.upsertNativeHistory(ctx, b, post, run.ID)
			if e != nil {
				run.Status = "failed"
				run.Error = redactDeliverySecrets(e.Error())
				run.Revision++
				if err = saveHistoryRun(ctx, b, &run); err != nil {
					return nil, err
				}
				return receiptForHistory(ctx, b, run), nil
			}
			switch result {
			case "suppressed":
				run.Incomplete = true
				addHistoryLimitation(&run, "Locally suppressed provider history exists; complete stored history cannot be claimed.")
			case "imported":
				run.Imported++
			case "updated":
				run.Updated++
			default:
				run.Unchanged++
			}
			run.Scanned++
		}
		if err = refreshHistoryMetadataCounts(ctx, &run); err != nil {
			return nil, err
		}
		if run.MetadataUnresolved > 0 {
			requireHistoryReconciliation(&run, "unresolved_native_metadata")
		}
		if err = observeHistoryPage(ctx, &run, page); err != nil {
			return nil, err
		}
		ids := []string{}
		for _, post := range page.Posts {
			ids = append(ids, post.ID)
		}
		signature := uploadURLHash(strings.Join(ids, "\n"))
		if signature == run.LastPageHash && page.Next != "" {
			run.RepeatedPages++
		} else {
			run.RepeatedPages = 0
		}
		run.LastPageHash = signature
		if run.RepeatedPages >= 2 {
			run.Status = "limited"
			run.Incomplete = true
			addHistoryLimitation(&run, "Provider repeated entire pages without advancing content; full coverage cannot be claimed.")
		}
		run.Pages++
		run.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if run.Phase == "overlap" {
			run.OverlapPages++
		}
		if page.Next != "" {
			hash := uploadURLHash(run.Phase + ":" + page.Next)
			if page.Next == run.Token || run.CursorHashes[hash] {
				run.Status = "limited"
				run.Incomplete = true
				addHistoryLimitation(&run, "Provider repeated a pagination cursor; traversal cannot be declared complete.")
			}
			run.CursorHashes[hash] = true
		}
		run.Token = page.Next
		if run.Status != "limited" {
			if run.Mode == "recent" && run.Pages >= pages {
				run.Status = "limited"
				addHistoryLimitation(&run, "Recent bounded sync does not establish full history coverage.")
			} else if run.Phase == "scan" && page.Next == "" {
				run.TraversalExhausted = true
				if run.ProviderTotal != nil && int64(run.Counts.TraversalUnique) != *run.ProviderTotal {
					requireHistoryReconciliation(&run, "provider_count_discrepancy")
				}
				run.Phase = "overlap"
				run.Token = ""
				run.CursorHashes = map[string]bool{}
				run.LastPageHash = ""
				run.RepeatedPages = 0
			} else if run.Phase == "overlap" && (page.Next == "" || (!run.ReconciliationFull && run.OverlapPages >= 2) || run.OverlapPages >= historyReconciliationPageLimit) {
				run.ReconciliationExhausted = page.Next == ""
				if run.ReconciliationFull && page.Next != "" {
					run.Incomplete = true
					addHistoryLimitation(&run, "Reconciliation page budget exhausted before the second traversal completed.")
				}
				finishHistoryCoverage(&run)
			}
		}
		run.Revision++
		if err = saveHistoryRun(ctx, b, &run); err != nil {
			return nil, err
		}
		if run.Status == "complete" || run.Status == "limited" {
			break
		}
	}
	return receiptForHistory(ctx, b, run), nil
}

func mergeLastKnown(oldRaw string, current nativePost) string {
	old := map[string]any{}
	_ = json.Unmarshal([]byte(oldRaw), &old)
	timestamps, _ := old["field_observed_at"].(map[string]any)
	if timestamps == nil {
		timestamps = map[string]any{}
	}
	raw, _ := json.Marshal(current)
	fresh := map[string]any{}
	_ = json.Unmarshal(raw, &fresh)
	for key, value := range fresh {
		if value != nil {
			old[key] = value
			if key != "observed_at" {
				timestamps[key] = current.ObservedAt
			}
		}
	}
	old["field_observed_at"] = timestamps
	raw, _ = json.Marshal(old)
	return string(raw)
}
func (a *App) upsertNativeHistory(ctx *sdk.AppCtx, b historyAccount, n nativePost, runIDs ...string) (string, int64, int64, error) {
	n = normalizeNativeObservation(b, n)
	if n.ID == "" {
		return "", 0, 0, errors.New("native post ID required")
	}
	comparable := n
	comparable.ObservedAt = ""
	raw, _ := json.Marshal(comparable)
	hash := uploadURLHash(string(raw))
	raw, _ = json.Marshal(n)
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return "", 0, 0, err
	}
	defer tx.Rollback()
	var connection int64
	var platform, provider, external, providerID, kind, status string
	err = tx.QueryRow(`SELECT connection_id,platform,COALESCE(provider_slug,'native'),COALESCE(external_account_id,''),COALESCE(provider_account_id,''),account_kind,status FROM social_accounts WHERE id=? AND project_id=?`, b.ID, b.Project).Scan(&connection, &platform, &provider, &external, &providerID, &kind, &status)
	if err != nil || connection != b.ConnID || platform != b.Platform || provider != b.Provider || external != b.ExternalID || providerID != b.ProviderID || kind != b.Kind || status != "active" {
		return "", 0, 0, errors.New("account changed before history write")
	}
	if n.ProviderPostID != nil && b.Provider == zernioProviderSlug {
		var suppressed int
		err := tx.QueryRow(`SELECT count(*) FROM provider_import_tombstones WHERE social_account_id=? AND provider_slug=? AND provider_post_id=?`, b.ID, b.Provider, *n.ProviderPostID).Scan(&suppressed)
		if err != nil {
			return "", 0, 0, err
		}
		if suppressed > 0 {
			return "suppressed", 0, 0, nil
		}
	}

	if n.MetadataUnavailable && len(runIDs) > 0 {
		var resolved int
		if e := tx.QueryRow(`SELECT count(*) FROM social_history_seen WHERE run_id=? AND native_post_id=? AND metadata_missing=0`, runIDs[0], n.ID).Scan(&resolved); e != nil {
			return "", 0, 0, e
		}
		if resolved > 0 {
			return "unchanged", 0, 0, nil
		}
	}

	var postID, targetID int64
	err = tx.QueryRow(`SELECT t.post_id,t.id FROM post_targets t JOIN posts p ON p.id=t.post_id WHERE p.project_id=? AND t.social_account_id=? AND t.platform_post_id=?`, b.Project, b.ID, n.ID).Scan(&postID, &targetID)
	result := "unchanged"
	// A known provider operation may acquire its native ID after Social accepted
	// it. Attach evidence to that exact target instead of creating a second post.
	if errors.Is(err, sql.ErrNoRows) && n.ProviderPostID != nil && b.Provider == zernioProviderSlug {
		var existingNative string
		err = tx.QueryRow(`SELECT t.post_id,t.id,COALESCE(t.platform_post_id,'') FROM post_targets t JOIN posts p ON p.id=t.post_id WHERE p.project_id=? AND t.social_account_id=? AND t.provider_post_id=?`, b.Project, b.ID, *n.ProviderPostID).Scan(&postID, &targetID, &existingNative)
		if err == nil {
			if existingNative != "" && existingNative != n.ID {
				return "", 0, 0, errors.New("provider operation conflicts with the stored native identity")
			}
			if existingNative == "" {
				if _, e := tx.Exec(`UPDATE post_targets SET platform_post_id=? WHERE id=? AND COALESCE(platform_post_id,'')=''`, n.ID, targetID); e != nil {
					return "", 0, 0, e
				}
			}
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		filtered := []string{}
		for _, u := range n.MediaURLs {
			if u != "" {
				filtered = append(filtered, u)
			}
		}
		media, _ := json.Marshal(filtered)
		profile := b.ProfileID
		observedStatus := "published"
		if n.ProcessingState != nil {
			switch strings.ToLower(*n.ProcessingState) {
			case "processing", "uploaded", "failed", "rejected", "terminated":
				observedStatus = "imported"
			}
		}
		if n.URL == nil && n.Title == nil && n.PublishedAt == nil {
			observedStatus = "imported"
		}
		res, e := tx.Exec(`INSERT INTO posts(project_id,profile_id,body,media_storage_ids,external_media_urls,status,imported_at,published_at,source,requested_mode) VALUES(?,?,?,'[]',?,?,CURRENT_TIMESTAMP,?,'history','draft')`, b.Project, profile, valueOrEmpty(n.Body), string(media), observedStatus, nullable(valueOrEmpty(n.PublishedAt)))
		if e != nil {
			return "", 0, 0, e
		}
		postID, _ = res.LastInsertId()
		options := n.Options
		if options == nil {
			options = map[string]any{}
		}
		if n.Title != nil {
			options["title"] = *n.Title
		}
		opt, _ := json.Marshal(options)
		res, e = tx.Exec(`INSERT INTO post_targets(post_id,social_account_id,status,platform_post_id,platform_url,published_at,native_uploaded_at,publication_observed_at,publication_time_source,options,retryable) VALUES(?,?,?,?,?,?,?,?,?,?,0)`, postID, b.ID, observedStatus, n.ID, nullable(valueOrEmpty(n.URL)), nullable(valueOrEmpty(n.PublishedAt)), n.UploadedAt, optionalText(n.ObservedAt), n.PublicationTimeSource, string(opt))
		if e != nil {
			return "", 0, 0, e
		}
		targetID, _ = res.LastInsertId()
		result = "imported"
	} else if err != nil {
		return "", 0, 0, err
	}
	var oldHash, lastKnown string
	e := tx.QueryRow(`SELECT content_hash,last_known_metadata FROM social_native_posts WHERE project_id=? AND social_account_id=? AND native_post_id=?`, b.Project, b.ID, n.ID).Scan(&oldHash, &lastKnown)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return "", 0, 0, e
	}
	if result != "imported" && oldHash != hash {
		result = "updated"
	}
	lastKnown = mergeLastKnown(lastKnown, n)
	_, err = tx.Exec(`INSERT INTO social_native_posts(project_id,social_account_id,native_post_id,post_id,target_id,metadata,last_known_metadata,content_hash,observed_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id,social_account_id,native_post_id) DO UPDATE SET metadata=excluded.metadata,last_known_metadata=excluded.last_known_metadata,content_hash=excluded.content_hash,observed_at=excluded.observed_at`, b.Project, b.ID, n.ID, postID, targetID, string(raw), lastKnown, hash, n.ObservedAt)
	if err != nil {
		return "", 0, 0, err
	}
	if len(runIDs) > 0 {
		var imported, updated, unchanged int
		switch result {
		case "imported":
			imported = 1
		case "updated":
			updated = 1
		default:
			unchanged = 1
		}
		_, err = tx.Exec(`INSERT INTO social_history_seen(run_id,native_post_id,imported,updated,unchanged,metadata_missing,metadata_observed_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(run_id,native_post_id) DO UPDATE SET imported=max(imported,excluded.imported),updated=max(updated,excluded.updated),unchanged=max(unchanged,excluded.unchanged),metadata_missing=excluded.metadata_missing,metadata_observed_at=excluded.metadata_observed_at,metadata_error=CASE WHEN excluded.metadata_missing=0 THEN '' ELSE metadata_error END`, runIDs[0], n.ID, imported, updated, unchanged, n.MetadataUnavailable, n.ObservedAt)
		if err != nil {
			return "", 0, 0, err
		}
	}
	return result, postID, targetID, tx.Commit()
}
func valueOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func optionalText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func historyState(ctx *sdk.AppCtx, b historyAccount) map[string]any {
	out := map[string]any{"complete": false, "current": false, "last_completed_at": nil, "observed_at": nil, "scope": "provider_accessible_history", "supported": historySupported(b), "current_definition": "Completed traversal observed within 15 minutes; provider data may subsequently change."}
	rows, err := ctx.AppDB().Query(`SELECT state FROM social_history_runs WHERE project_id=? AND social_account_id=? AND binding=? ORDER BY rowid DESC LIMIT 50`, b.Project, b.ID, b.binding())
	if err != nil {
		return out
	}
	defer rows.Close()
	first := true
	for rows.Next() {
		var raw string
		_ = rows.Scan(&raw)
		var run historyRun
		if json.Unmarshal([]byte(raw), &run) != nil {
			continue
		}
		if first {
			out["latest_status"] = run.Status
			out["latest_mode"] = run.Mode
			out["latest_error"] = run.Error
			out["limitations"] = run.Limitations
			out["scope"] = run.Scope
			out["observed_at"] = run.ObservedAt
			out["coverage"] = historyCoverage(run)
			out["needs_resync"] = run.CoverageVersion != historyCoverageVersion
			first = false
		}
		if historyCoverage(run)["complete"] == true {
			out["complete"] = true
			out["last_completed_at"] = run.CompletedAt
			out["completed_limitations"] = run.Limitations
			out["completed_scope"] = run.Scope
			out["completed_coverage"] = historyCoverage(run)
			t, e := time.Parse(time.RFC3339Nano, run.ObservedAt)
			out["current"] = b.Status == "active" && e == nil && time.Since(t) <= 15*time.Minute && out["latest_status"] != "failed" && !(out["latest_mode"] == "full" && out["latest_status"] == "limited")
			break
		}
	}
	return out
}
func (a *App) toolAccountHistoryStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	b, err := loadHistoryAccountForRead(ctx, int64(intArg(args, "social_account_id", 0)))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return map[string]any{"social_account_id": b.ID, "history": historyState(ctx, b)}, nil
}
func (a *App) historyTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "account_history_sync", Description: "Read native history for ONE Social account into local history only; never creates publishing jobs, retries posts or changes automatic sync settings. mode=full traverses provider-accessible history across bounded resumable calls; follow continuation_cursor until null and inspect coverage/limitations. max_pages defaults to 2 (max 5). Duplicate/overlapping pages reconcile by project+account+native ID. No integration connection IDs are accepted.", InputSchema: schemaObject(map[string]any{"social_account_id": map[string]any{"type": "integer", "minimum": 1}, "mode": map[string]any{"type": "string", "enum": []string{"recent", "full"}}, "cursor": map[string]any{"type": "string"}, "max_pages": map[string]any{"type": "integer", "minimum": 1, "maximum": 5}, "page_size": map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}, []string{"social_account_id"}), Handler: a.toolAccountHistorySync},
		{Name: "account_history_status", Description: "Read stored history coverage and observation freshness for one Social account without provider requests. Complete means provider-accessible scope, subject to explicit limitations; current means observed within 15 minutes.", Annotations: map[string]any{"readOnlyHint": true}, InputSchema: schemaObject(map[string]any{"social_account_id": map[string]any{"type": "integer", "minimum": 1}}, []string{"social_account_id"}), Handler: a.toolAccountHistoryStatus},
		{Name: "post_inspect", Description: "Read fresh native metadata for a post on one Social account. Does not publish/retry, change managed delivery state, or establish independent public-player verification. Private/unlisted never imply membership. Unknown provider fields remain null.", InputSchema: schemaObject(map[string]any{"social_account_id": map[string]any{"type": "integer", "minimum": 1}, "native_post_id": map[string]any{"type": "string"}}, []string{"social_account_id", "native_post_id"}), Handler: a.toolPostNativeInspect},
	}
}

func postListHistoryStates(ctx *sdk.AppCtx, accountID, profileID int64) map[string]any {
	query := `SELECT id FROM social_accounts WHERE project_id=?`
	args := []any{projectScope(ctx)}
	if accountID > 0 {
		query += ` AND id=?`
		args = append(args, accountID)
	}
	if profileID > 0 {
		query += ` AND profile_id=?`
		args = append(args, profileID)
	}
	rows, err := ctx.AppDB().Query(query, args...)
	out := map[string]any{}
	if err != nil {
		return out
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		b, e := loadHistoryAccountForRead(ctx, id)
		if e != nil {
			out[strconv.FormatInt(id, 10)] = map[string]any{"complete": false, "current": false, "limitation": e.Error()}
		} else {
			out[strconv.FormatInt(id, 10)] = historyState(ctx, b)
		}
	}
	return out
}

func historySnippet(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func historyCapabilities(platform, provider, external string) map[string]any {
	b := historyAccount{destinationAccount: destinationAccount{Platform: platform, Provider: provider, ExternalID: external}}
	listing := historySupported(b)
	inspect := provider == zernioProviderSlug || provider == "native" && (platform == "youtube" || platform == "pinterest" || platform == "facebook" || platform == "instagram" || platform == "tiktok" || platform == "twitter")
	if platform == "linkedin" && provider == "native" && !strings.HasPrefix(external, "urn:li:") {
		listing = false
	}
	return map[string]any{"listing": listing, "inspection": inspect, "full_sync": "provider_accessible_history_with_explicit_coverage", "resumable": listing, "tool": "account_history_sync", "inspection_tool": "post_inspect"}
}
