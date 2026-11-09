package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

var reconnectIdentityPatterns = map[string]*regexp.Regexp{
	"youtube":   regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`),
	"facebook":  regexp.MustCompile(`^[0-9]{1,32}$`),
	"instagram": regexp.MustCompile(`^[0-9]{1,32}$`),
	"twitter":   regexp.MustCompile(`^[0-9]{1,32}$`),
	"tiktok":    regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`),
	"pinterest": regexp.MustCompile(`^[0-9]{1,32}$`),
}

func supportsAccountReconnect(platform, provider, status string, connectionID int64) bool {
	return provider == "native" && status != "disconnected" && connectionID > 0 && reconnectIdentityPatterns[platform] != nil
}

// A connection can authorize several Social accounts, especially Facebook
// Pages and their linked Instagram accounts. Check every live binding.
func reconnectAccounts(ctx *sdk.AppCtx, account audienceAccount) ([]audienceAccount, error) {
	rows, err := ctx.AppDB().Query(`SELECT id,platform,COALESCE(external_account_id,''),status,
		COALESCE(provider_slug,'native') FROM social_accounts
		WHERE project_id=? AND connection_id=? AND status!='disconnected' ORDER BY id`, account.Project, account.ConnectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []audienceAccount
	for rows.Next() {
		sibling := audienceAccount{Project: account.Project, ConnectionID: account.ConnectionID}
		if err := rows.Scan(&sibling.ID, &sibling.Platform, &sibling.ExternalID, &sibling.Status, &sibling.Provider); err != nil {
			return nil, err
		}
		if !supportsAccountReconnect(sibling.Platform, sibling.Provider, sibling.Status, sibling.ConnectionID) ||
			platforms[sibling.Platform].IntegrationSlug != platforms[account.Platform].IntegrationSlug {
			return nil, errors.New("the connection has an account that Social cannot safely verify")
		}
		accounts = append(accounts, sibling)
	}
	if len(accounts) == 0 {
		return nil, errors.New("the connection has no active Social accounts")
	}
	return accounts, rows.Err()
}

func reconnectIdentity(ctx *sdk.AppCtx, account audienceAccount) (sdk.OAuthReconnectIdentity, error) {
	pattern := reconnectIdentityPatterns[account.Platform]
	id := account.ExternalID
	if account.Platform == "pinterest" {
		// Older Pinterest rows may store the default board ID here.
		id = ""
	}
	var historyOwner string
	err := ctx.AppDB().QueryRow(`SELECT native_owner_id FROM social_history_owners WHERE project_id=? AND social_account_id=? AND platform=?`, account.Project, account.ID, account.Platform).Scan(&historyOwner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return sdk.OAuthReconnectIdentity{}, err
	}
	if pattern.MatchString(historyOwner) {
		if pattern.MatchString(id) && id != historyOwner {
			return sdk.OAuthReconnectIdentity{}, errors.New("the saved account identities disagree")
		}
		id = historyOwner
	}
	if !pattern.MatchString(id) {
		id, err = readExistingReconnectIdentity(ctx, account)
		if err != nil || !pattern.MatchString(id) {
			return sdk.OAuthReconnectIdentity{}, errors.New("the original account could not be identified")
		}
	}
	return sdk.OAuthReconnectIdentity{Kind: account.Platform, ID: id}, nil
}

func readExistingReconnectIdentity(ctx *sdk.AppCtx, account audienceAccount) (string, error) {
	if account.Platform == "youtube" {
		return youtubeChannelIdentity(ctx, account)
	}
	def := platforms[account.Platform]
	if def.ProfileTool == "" {
		return "", errors.New("no original account identity is available")
	}
	out, err := ctx.PlatformAPI().ExecuteIntegrationTool(account.ConnectionID, def.ProfileTool, def.ProfileToolArgs)
	if err != nil {
		return "", err
	}
	if out == nil || !out.Success {
		return "", upstreamError(out)
	}
	return reconnectIdentityFromProfile(out.Data, account.Platform), nil
}

func reconnectIdentityFromProfile(raw []byte, platform string) string {
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil {
		return ""
	}
	switch platform {
	case "youtube":
		items, _ := data["items"].([]any)
		if len(items) == 1 {
			item, _ := items[0].(map[string]any)
			return historyString(item, "id")
		}
	case "twitter":
		return historyString(historyMap(data, "data"), "id")
	case "tiktok":
		return historyString(historyMap(historyMap(data, "data"), "user"), "open_id")
	case "pinterest":
		return historyString(data, "id")
	}
	return ""
}

func rememberReconnectIdentity(db interface {
	Exec(string, ...any) (sql.Result, error)
}, accountID int64, project, platform, identity string) error {
	pattern := reconnectIdentityPatterns[platform]
	if pattern == nil || !pattern.MatchString(identity) {
		return nil
	}
	_, err := db.Exec(`INSERT OR IGNORE INTO social_history_owners(project_id,social_account_id,platform,native_owner_id)
		VALUES (?,?,?,?)`, project, accountID, platform, identity)
	return err
}

type socialReconnectResult struct {
	SocialAccountID int64  `json:"social_account_id"`
	ConnectionID    int64  `json:"connection_id"`
	ID              string `json:"reconnect_id"`
	Status          string `json:"status"`
	AuthorizeURL    string `json:"authorize_url,omitempty"`
	ExpiresAt       string `json:"expires_at,omitempty"`
	Coverage        string `json:"inbox_coverage,omitempty"`
	Error           string `json:"error,omitempty"`
	CommentsRead    bool   `json:"comments_read"`
}

func (a *App) toolAccountReconnect(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	id := toInt64Loose(args["social_account_id"])
	account, err := loadAudienceAccount(ctx, ctx.CurrentProject(), id)
	if err != nil || !supportsAccountReconnect(account.Platform, account.Provider, account.Status, account.ConnectionID) {
		return mcpError("Select an existing native OAuth account in this project. Provider-managed accounts reconnect through their provider."), nil
	}
	accounts, err := reconnectAccounts(ctx, account)
	if err != nil {
		return mcpError("Reconnect stopped: " + redactDeliverySecrets(err.Error())), nil
	}
	expected := make([]sdk.OAuthReconnectIdentity, 0, len(accounts))
	seen := map[sdk.OAuthReconnectIdentity]bool{}
	for _, sibling := range accounts {
		identity, err := reconnectIdentity(ctx, sibling)
		if err != nil {
			return mcpError(fmt.Sprintf("Reconnect stopped for Social account %d: %s", sibling.ID, redactDeliverySecrets(err.Error()))), nil
		}
		if !seen[identity] {
			seen[identity] = true
			expected = append(expected, identity)
		}
	}
	if platforms[account.Platform].ListPagesTool == "" && len(expected) != 1 {
		return mcpError("The accounts sharing this connection have different identities. Reconnect stopped."), nil
	}
	out, err := sdk.StartOAuthReconnect(ctx.PlatformAPI(), sdk.OAuthReconnectRequest{
		ConnectionID: account.ConnectionID, ExpectedIdentities: expected, ProjectID: account.Project,
	})
	if err != nil {
		if strings.Contains(err.Error(), "POST /oauth/start only") || strings.Contains(err.Error(), "does not support verified OAuth reconnect") {
			return mcpError("Update the Apteva server to use verified reconnect from Social."), nil
		}
		return mcpError("Could not start verified reconnect: " + redactDeliverySecrets(err.Error())), nil
	}
	if out == nil || out.ConnectionID != account.ConnectionID || out.ID == "" || out.AuthorizeURL == "" {
		return mcpError("The platform did not return a reconnect request for this connection"), nil
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO social_oauth_reconnects(id,project_id,social_account_id,connection_id) VALUES(?,?,?,?)`,
		out.ID, account.Project, id, account.ConnectionID); err != nil {
		_, _ = sdk.OAuthReconnectStatus(ctx.PlatformAPI(), sdk.OAuthReconnectQuery{ID: out.ID, ProjectID: account.Project, Cancel: true})
		return nil, err
	}
	_, _ = ctx.AppDB().Exec(`DELETE FROM social_oauth_reconnects WHERE created_at < datetime('now','-1 day')`)
	return socialReconnectResult{SocialAccountID: id, ConnectionID: out.ConnectionID, ID: out.ID,
		Status: out.Status, AuthorizeURL: out.AuthorizeURL, ExpiresAt: out.ExpiresAt}, nil
}

func (a *App) toolAccountReconnectStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	id := toInt64Loose(args["social_account_id"])
	requestID := strOption(args, "reconnect_id")
	if id <= 0 || requestID == "" {
		return mcpError("social_account_id and reconnect_id are required"), nil
	}
	unlock := lockSocialResource(ctx, id, "oauth_reconnect")
	defer unlock()
	result := socialReconnectResult{ID: requestID, SocialAccountID: id}
	var completed string
	err := ctx.AppDB().QueryRow(`SELECT r.connection_id,r.result FROM social_oauth_reconnects r
		JOIN social_accounts a ON a.id=r.social_account_id AND a.project_id=r.project_id AND a.connection_id=r.connection_id
		WHERE r.id=? AND r.social_account_id=? AND r.project_id=? AND a.status!='disconnected'`, requestID, id, ctx.CurrentProject()).
		Scan(&result.ConnectionID, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return mcpError("Reconnect request not found for this account"), nil
	}
	if err != nil {
		return nil, err
	}
	if completed != "" {
		err = json.Unmarshal([]byte(completed), &result)
		return result, err
	}
	cancel, _ := args["cancel"].(bool)
	remote, err := sdk.OAuthReconnectStatus(ctx.PlatformAPI(), sdk.OAuthReconnectQuery{ID: requestID, ProjectID: ctx.CurrentProject(), Cancel: cancel})
	if err != nil {
		return mcpError("Could not check reconnect: " + redactDeliverySecrets(err.Error())), nil
	}
	if remote == nil || remote.ID != requestID || remote.ConnectionID != result.ConnectionID {
		return mcpError("The platform returned a different reconnect request"), nil
	}
	result.Status, result.ExpiresAt = remote.Status, remote.ExpiresAt
	if remote.Status != "authorized" {
		return result, nil
	}

	account, err := loadAudienceAccount(ctx, ctx.CurrentProject(), id)
	if err != nil {
		return nil, err
	}
	if err := a.verifyReconnectedAccount(ctx, account, &result); err != nil {
		return nil, err
	}
	// Leave failed verification retryable with the same receipt.
	if strings.HasSuffix(result.Status, "_unverified") {
		result.Error = redactDeliverySecrets(result.Error)
		return result, nil
	}
	result.Error = redactDeliverySecrets(result.Error)
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	_, err = ctx.AppDB().Exec(`UPDATE social_oauth_reconnects SET result=? WHERE id=? AND project_id=?`, string(encoded), requestID, ctx.CurrentProject())
	return result, err
}

func (a *App) verifyReconnectedAccount(ctx *sdk.AppCtx, account audienceAccount, result *socialReconnectResult) error {
	accounts, err := reconnectAccounts(ctx, account)
	if err != nil {
		return err
	}
	for _, sibling := range accounts {
		if platforms[sibling.Platform].ListPagesTool == "" {
			continue
		}
		check := a.checkAccount(ctx, sibling.Project, sibling.ID)
		if check.Status != "ok" || check.Details["page_token_refreshed"] != true {
			result.Status = "account_unverified"
			result.Error = "The new Page credentials could not be verified for every linked destination."
			return nil
		}
	}
	if _, err := ctx.AppDB().Exec(`UPDATE social_accounts SET status='active'
		WHERE project_id=? AND connection_id=? AND status='needs_reauth'`, account.Project, account.ConnectionID); err != nil {
		return err
	}
	if !inboxHasSupport(account.Platform) {
		result.Status = "account_connected"
		return nil
	}

	// The explicit sync bypasses the scheduled interval. Keep comment
	// cursors and quota backoff while clearing the old permission pause.
	if _, err := ctx.AppDB().Exec(`DELETE FROM inbox_cursors WHERE social_account_id=? AND kind='poll_backoff'
		AND (lower(COALESCE(last_error,'')) LIKE '%permission_required%' OR lower(COALESCE(last_error,'')) LIKE '%access_token_scope_insufficient%')`, account.ID); err != nil {
		return err
	}
	if account.Platform == "twitter" {
		if _, err := ctx.AppDB().Exec(`UPDATE inbox_cursors SET last_error=NULL WHERE social_account_id=? AND kind=? AND last_error LIKE ?`,
			account.ID, twitterDMCursorKind, twitterDMPermissionPrefix+"%"); err != nil {
			return err
		}
	}
	sync, err := a.toolInboxSync(ctx, map[string]any{"social_account_ids": []any{account.ID}})
	if err != nil {
		return err
	}
	result.Status = "inbox_unverified"
	if account.Platform == "youtube" {
		result.Status = "comments_unverified"
	}
	if payload, ok := sync.(map[string]any); ok {
		if rows, ok := payload["results"].([]inboxSyncResult); ok && len(rows) == 1 {
			row := rows[0]
			result.Coverage, result.Error = row.Coverage, row.Error
			if warnings := inboxReportWarnings(row.Report); len(warnings) > 0 {
				result.Error = strings.Join(warnings, ". ")
			}
			if account.Platform == "youtube" {
				if report, ok := row.Report.(*youtubeSyncReport); ok && report.CommentsRead {
					result.Status, result.CommentsRead = "comments_connected", true
				}
			} else if row.Status == "ok" && row.Coverage == coverageComplete {
				result.Status = "inbox_connected"
			}
		}
	}
	if strings.HasSuffix(result.Status, "_unverified") && result.Error == "" {
		result.Error = "The provider authorized the account. The inbox read could not be verified."
	}
	return nil
}

func (a *App) handleAccountReconnect(w http.ResponseWriter, r *http.Request, accountID int64, status bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	args := projectArgsFromRequest(r)
	args["social_account_id"] = accountID
	handler := a.toolAccountReconnect
	if status {
		var body struct {
			ID     string `json:"reconnect_id"`
			Cancel bool   `json:"cancel"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body); err != nil {
			http.Error(w, "invalid reconnect status request", http.StatusBadRequest)
			return
		}
		args["reconnect_id"], args["cancel"] = body.ID, body.Cancel
		handler = a.toolAccountReconnectStatus
	}
	out, err := handler(globalCtx.WithUserSession(r), args)
	if err != nil {
		http.Error(w, "could not complete Social reconnect", http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}
