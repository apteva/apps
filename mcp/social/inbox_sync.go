package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type inboxAccount struct {
	ID             int64
	ProjectID      string
	Platform       string
	ConnID         int64
	ExtID          string
	Name           string
	PageCreds      string
	ProfileID      int64
	PageToken      string
	LastSynced     string
	AuthorProfiles map[string]metaAuthorProfile
}

type inboxSyncOptions struct {
	ProjectID  string
	ProfileID  int64
	AccountIDs []int64
	Limit      int
	Initial    bool
}

type inboxSyncResult struct {
	SocialAccountID int64    `json:"social_account_id"`
	Platform        string   `json:"platform"`
	DisplayName     string   `json:"display_name,omitempty"`
	Status          string   `json:"status"` // ok | unsupported | failed
	NewItems        int      `json:"new_items"`
	Comments        int      `json:"comments,omitempty"`
	DMs             int      `json:"dms,omitempty"`
	Mentions        int      `json:"mentions,omitempty"`
	Reviews         int      `json:"reviews,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
	Error           string   `json:"error,omitempty"`
	LastSyncAt      string   `json:"last_sync_at,omitempty"`
}

type inboxSyncResponse struct {
	Results []inboxSyncResult `json:"results"`
	Count   int               `json:"count"`
}

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name:     "inbox-sync",
			Schedule: "@every 5m",
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				return a.runInboxSyncWorker(ctx, app)
			},
		},
	}
}

func (a *App) runInboxSyncWorker(ctx context.Context, app *sdk.AppCtx) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	accounts, err := loadInboxAccounts(app, inboxSyncOptions{Limit: 25})
	if err != nil {
		return err
	}
	cutoff := time.Now().UTC().Add(-4 * time.Minute)
	for _, acct := range accounts {
		if acct.LastSynced != "" {
			if t, err := parsePlatformTime(acct.LastSynced); err == nil && t.After(cutoff) {
				continue
			}
		}
		_ = a.syncInboxAccount(app, acct, inboxSyncOptions{Limit: 25})
	}
	return nil
}

func (a *App) triggerInitialInboxSync(ctx *sdk.AppCtx, accountID int64) {
	if accountID <= 0 {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		accounts, err := loadInboxAccounts(ctx, inboxSyncOptions{AccountIDs: []int64{accountID}, Limit: 100, Initial: true})
		if err != nil || len(accounts) == 0 {
			return
		}
		_ = a.syncInboxAccount(ctx, accounts[0], inboxSyncOptions{Limit: 100, Initial: true})
	}()
}

func (a *App) syncInbox(ctx *sdk.AppCtx, opts inboxSyncOptions) inboxSyncResponse {
	if opts.Limit <= 0 {
		opts.Limit = 50
	}
	if opts.Initial && opts.Limit < 100 {
		opts.Limit = 100
	}
	if opts.Limit > 200 {
		opts.Limit = 200
	}
	accounts, err := loadInboxAccounts(ctx, opts)
	if err != nil {
		return inboxSyncResponse{Results: []inboxSyncResult{{
			Status: "failed",
			Error:  err.Error(),
		}}, Count: 1}
	}
	results := make([]inboxSyncResult, 0, len(accounts))
	for _, acct := range accounts {
		results = append(results, a.syncInboxAccount(ctx, acct, opts))
	}
	return inboxSyncResponse{Results: results, Count: len(results)}
}

func loadInboxAccounts(ctx *sdk.AppCtx, opts inboxSyncOptions) ([]inboxAccount, error) {
	q := `SELECT a.id, a.project_id, a.platform, a.connection_id,
	             COALESCE(a.external_account_id,''), a.display_name,
	             COALESCE(a.page_credentials,''), COALESCE(a.profile_id,0),
	             COALESCE(MAX(c.last_sync_at),'')
	      FROM social_accounts a
	      LEFT JOIN inbox_cursors c ON c.social_account_id=a.id
	      WHERE a.status='active'`
	args := []any{}
	if opts.ProjectID != "" {
		q += " AND a.project_id=?"
		args = append(args, opts.ProjectID)
	}
	if opts.ProfileID > 0 {
		q += " AND a.profile_id=?"
		args = append(args, opts.ProfileID)
	}
	if len(opts.AccountIDs) > 0 {
		q += " AND a.id IN (" + placeholders(len(opts.AccountIDs)) + ")"
		for _, id := range opts.AccountIDs {
			args = append(args, id)
		}
	}
	q += " GROUP BY a.id ORDER BY a.id"
	rows, err := ctx.AppDB().Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []inboxAccount
	for rows.Next() {
		var a inboxAccount
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Platform, &a.ConnID, &a.ExtID, &a.Name, &a.PageCreds, &a.ProfileID, &a.LastSynced); err != nil {
			return nil, err
		}
		a.PageToken = extractPageToken(a.PageCreds)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (a *App) syncInboxAccount(ctx *sdk.AppCtx, acct inboxAccount, opts inboxSyncOptions) inboxSyncResult {
	res := inboxSyncResult{
		SocialAccountID: acct.ID,
		Platform:        acct.Platform,
		DisplayName:     acct.Name,
		Status:          "ok",
	}
	if acct.PageToken == "" && (acct.Platform == "facebook" || acct.Platform == "instagram") {
		res.Status = "failed"
		res.Error = "page access_token missing — reconnect the account"
		setInboxCursorError(ctx.AppDB(), acct.ID, "all", res.Error)
		return res
	}
	if acct.AuthorProfiles == nil {
		acct.AuthorProfiles = map[string]metaAuthorProfile{}
	}
	switch acct.Platform {
	case "facebook":
		syncFacebookInbox(ctx, acct, opts, &res)
	case "instagram":
		syncInstagramInbox(ctx, acct, opts, &res)
	default:
		res.Status = "unsupported"
		res.Warnings = append(res.Warnings, "inbox sync is not wired for "+acct.Platform)
	}
	res.NewItems = res.Comments + res.DMs + res.Mentions + res.Reviews
	res.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
	if res.Status == "ok" {
		for _, kind := range []string{inboxKindComment, inboxKindDM, inboxKindMention, inboxKindReview} {
			setInboxCursorOK(ctx.AppDB(), acct.ID, kind)
		}
		ctx.Emit("inbox.synced", map[string]any{
			"social_account_id": acct.ID,
			"platform":          acct.Platform,
			"new_items":         res.NewItems,
		})
	} else if res.Error != "" {
		setInboxCursorError(ctx.AppDB(), acct.ID, "all", res.Error)
		ctx.Emit("inbox.sync_failed", map[string]any{
			"social_account_id": acct.ID,
			"platform":          acct.Platform,
			"error":             res.Error,
		})
	}
	return res
}

func setInboxCursorOK(db *sql.DB, accountID int64, kind string) {
	_, _ = db.Exec(
		`INSERT INTO inbox_cursors (social_account_id, kind, cursor, last_sync_at, last_error)
		 VALUES (?, ?, '', CURRENT_TIMESTAMP, NULL)
		 ON CONFLICT(social_account_id, kind) DO UPDATE SET
		   last_sync_at=excluded.last_sync_at, last_error=NULL`,
		accountID, kind,
	)
}

func setInboxCursorError(db *sql.DB, accountID int64, kind, msg string) {
	_, _ = db.Exec(
		`INSERT INTO inbox_cursors (social_account_id, kind, cursor, last_sync_at, last_error)
		 VALUES (?, ?, '', CURRENT_TIMESTAMP, ?)
		 ON CONFLICT(social_account_id, kind) DO UPDATE SET
		   last_sync_at=excluded.last_sync_at, last_error=excluded.last_error`,
		accountID, kind, msg,
	)
}

func (a *App) handleInboxCollection(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	switch r.Method {
	case http.MethodGet:
		args := queryToolArgs(r)
		if v := r.URL.Query().Get("account_id"); v != "" {
			args["social_account_ids"] = []any{v}
		}
		if v := r.URL.Query().Get("kind"); v != "" && v != "all" {
			args["kinds"] = []any{v}
		}
		if v := r.URL.Query().Get("status"); v != "" && v != "all" {
			args["status"] = []any{v}
		}
		out, err := a.toolInboxList(ctx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		if strings.TrimSuffix(r.URL.Path, "/") != "/inbox/sync" {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		a.handleInboxSync(w, r)
	default:
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleInboxSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	opts := inboxSyncOptions{
		ProjectID: strings.TrimSpace(r.URL.Query().Get("project_id")),
		Limit:     50,
	}
	if opts.ProjectID == "" {
		opts.ProjectID = strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID"))
	}
	if v := r.URL.Query().Get("profile_id"); v != "" {
		opts.ProfileID = toInt64Loose(v)
	}
	if v := r.URL.Query().Get("account_id"); v != "" {
		opts.AccountIDs = []int64{toInt64Loose(v)}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			opts.Limit = n
		}
	}
	var body struct {
		AccountIDs []int64 `json:"social_account_ids"`
		Initial    bool    `json:"initial"`
		Limit      int     `json:"limit"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if len(body.AccountIDs) > 0 {
		opts.AccountIDs = body.AccountIDs
	}
	if body.Limit > 0 {
		opts.Limit = body.Limit
	}
	opts.Initial = body.Initial
	writeJSON(w, a.syncInbox(requestCtx(r), opts))
}

func (a *App) handleInboxItem(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	rest := strings.TrimPrefix(r.URL.Path, "/inbox/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		args := queryToolArgs(r)
		args["id"] = id
		args["with_thread"] = true
		out, err := a.toolInboxGet(ctx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
		return
	}
	if len(parts) != 2 || r.Method != http.MethodPost {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body == nil {
		body = map[string]any{}
	}
	for k, v := range queryToolArgs(r) {
		body[k] = v
	}
	body["id"] = id
	var out any
	switch parts[1] {
	case "reply":
		out, err = a.toolInboxReply(ctx, body)
	case "private_reply":
		out, err = a.toolInboxPrivateReply(ctx, body)
	case "read":
		out, err = a.toolInboxMarkRead(ctx, body)
	case "unread":
		out, err = a.toolInboxMarkUnread(ctx, body)
	case "archive":
		out, err = a.toolInboxArchive(ctx, body)
	case "hide":
		out, err = a.toolInboxHide(ctx, body)
	case "unhide":
		out, err = a.toolInboxUnhide(ctx, body)
	case "like":
		out, err = a.toolInboxLike(ctx, body)
	case "delete":
		out, err = a.toolInboxDelete(ctx, body)
	default:
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

func parsePlatformTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("empty time")
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05-0700",
		"2006-01-02T15:04:05+0000",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unsupported time %q", s)
}

func rawJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
