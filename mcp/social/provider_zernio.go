package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const zernioProviderSlug = "zernio"

type providerAccountImportResult struct {
	Status          string               `json:"status"`
	Provider        string               `json:"provider"`
	DryRun          bool                 `json:"dry_run"`
	Imported        int                  `json:"imported"`
	SkippedExisting int                  `json:"skipped_existing"`
	Failed          int                  `json:"failed"`
	Accounts        []providerAccountRow `json:"accounts"`
	Error           string               `json:"error,omitempty"`
}

type providerAccountRow struct {
	ID                int64          `json:"id,omitempty"`
	ProviderAccountID string         `json:"provider_account_id"`
	ProviderProfileID string         `json:"provider_profile_id,omitempty"`
	ProviderProfile   string         `json:"provider_profile,omitempty"`
	Platform          string         `json:"platform"`
	DisplayName       string         `json:"display_name"`
	AvatarURL         string         `json:"avatar_url,omitempty"`
	Status            string         `json:"status"`
	Reason            string         `json:"reason,omitempty"`
	Capabilities      map[string]any `json:"capabilities,omitempty"`
}

type zernioAccount struct {
	AccountID string
	ProfileID string
	Profile   string
	Platform  string
	Name      string
	Avatar    string
	Status    string
	Raw       map[string]any
}

type zernioProfile struct {
	ID               string   `json:"provider_profile_id"`
	Name             string   `json:"name"`
	Description      string   `json:"description,omitempty"`
	Color            string   `json:"color,omitempty"`
	IsDefault        bool     `json:"is_default,omitempty"`
	AccountUsernames []string `json:"account_usernames,omitempty"`
}

type providerProfileListResult struct {
	Status   string          `json:"status"`
	Provider string          `json:"provider"`
	Profiles []zernioProfile `json:"profiles"`
	Error    string          `json:"error,omitempty"`
}

type zernioProviderPlatform struct {
	Platform    string
	DisplayName string
}

func zernioProviderPlatforms() []zernioProviderPlatform {
	return []zernioProviderPlatform{
		{Platform: "twitter", DisplayName: "X (Twitter)"},
		{Platform: "instagram", DisplayName: "Instagram"},
		{Platform: "tiktok", DisplayName: "TikTok"},
		{Platform: "linkedin", DisplayName: "LinkedIn"},
		{Platform: "facebook", DisplayName: "Facebook"},
		{Platform: "youtube", DisplayName: "YouTube"},
		{Platform: "threads", DisplayName: "Threads"},
		{Platform: "reddit", DisplayName: "Reddit"},
		{Platform: "pinterest", DisplayName: "Pinterest"},
		{Platform: "bluesky", DisplayName: "Bluesky"},
		{Platform: "telegram", DisplayName: "Telegram"},
		{Platform: "googlebusiness", DisplayName: "Google Business"},
		{Platform: "snapchat", DisplayName: "Snapchat"},
		{Platform: "whatsapp", DisplayName: "WhatsApp"},
		{Platform: "discord", DisplayName: "Discord"},
	}
}

func zernioPlatformAllowed(platform string) bool {
	platform = normalizeZernioPlatform(platform)
	for _, p := range zernioProviderPlatforms() {
		if p.Platform == platform {
			return true
		}
	}
	return false
}

func (a *App) toolAccountImportProvider(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	provider := strings.ToLower(strings.TrimSpace(toString(args["provider"])))
	if provider == "" {
		provider = zernioProviderSlug
	}
	switch provider {
	case zernioProviderSlug:
		return a.importZernioAccounts(ctx, projectScope(ctx, args), args), nil
	default:
		return mcpError("unsupported provider " + provider), nil
	}
}

func (a *App) handleProviderAccountsImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	args := projectArgsFromRequest(r)
	if r.Body != nil {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		for k, v := range body {
			args[k] = v
		}
	}
	out, err := a.toolAccountImportProvider(globalCtx, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleProviderProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	args := projectArgsFromRequest(r)
	out := providerProfileListResult{Status: "ok", Provider: zernioProviderSlug}
	connID, err := zernioConnectionID(globalCtx, projectScope(globalCtx, args))
	if err != nil {
		out.Status = "failed"
		out.Error = err.Error()
		writeJSON(w, out)
		return
	}
	profiles, err := a.listZernioProfiles(globalCtx, connID, args)
	if err != nil {
		out.Status = "failed"
		out.Error = err.Error()
		writeJSON(w, out)
		return
	}
	out.Profiles = profiles
	writeJSON(w, out)
}

func (a *App) importZernioAccounts(ctx *sdk.AppCtx, pid string, args map[string]any) providerAccountImportResult {
	out := providerAccountImportResult{
		Status:   "ok",
		Provider: zernioProviderSlug,
		DryRun:   boolArg(args, "dry_run", false),
	}
	profileID := resolveProfileArg(ctx, pid, args)
	if profileID < 0 {
		out.Status = "failed"
		out.Error = fmt.Sprintf("profile %q not found in this project", args["profile"])
		return out
	}
	if profileID == 0 {
		profileID = projectDefaultProfileID(ctx, pid)
	}
	connID, err := zernioConnectionID(ctx, pid)
	if err != nil {
		out.Status = "failed"
		out.Error = err.Error()
		return out
	}
	accounts, err := a.listZernioAccounts(ctx, connID, args)
	if err != nil {
		out.Status = "failed"
		out.Error = err.Error()
		return out
	}
	accountIDs := stringSliceArg(args, "account_ids")
	if len(accountIDs) == 0 {
		accountIDs = stringSliceArg(args, "provider_account_ids")
	}
	accountFilter := stringSet(accountIDs)
	platformFilter := stringSet(stringSliceArg(args, "platforms"))
	for _, za := range accounts {
		row := providerAccountRow{
			ProviderAccountID: za.AccountID,
			ProviderProfileID: za.ProfileID,
			ProviderProfile:   za.Profile,
			Platform:          za.Platform,
			DisplayName:       za.Name,
			AvatarURL:         za.Avatar,
			Status:            "ready",
			Capabilities:      zernioCapabilities(za.Platform),
		}
		if za.AccountID == "" || za.Platform == "" {
			row.Status = "failed"
			row.Reason = "provider account missing account id or platform"
			out.Failed++
			out.Accounts = append(out.Accounts, row)
			continue
		}
		if len(accountFilter) > 0 && !accountFilter[za.AccountID] {
			continue
		}
		if len(platformFilter) > 0 && !platformFilter[za.Platform] {
			continue
		}
		if out.DryRun {
			out.Accounts = append(out.Accounts, row)
			continue
		}
		inserted, err := a.upsertZernioSocialAccount(ctx, pid, connID, profileID, za)
		if err != nil {
			row.Status = "failed"
			row.Reason = err.Error()
			out.Failed++
			out.Accounts = append(out.Accounts, row)
			continue
		}
		row.ID = inserted.ID
		if inserted.Status == "skipped_existing" {
			row.Status = "skipped_existing"
			out.SkippedExisting++
		} else {
			row.Status = "ok"
			out.Imported++
		}
		out.Accounts = append(out.Accounts, row)
	}
	return out
}

func (a *App) upsertZernioSocialAccount(ctx *sdk.AppCtx, pid string, connID int64, profileID int64, za zernioAccount) (providerAccountRow, error) {
	row := providerAccountRow{
		ProviderAccountID: za.AccountID,
		ProviderProfileID: za.ProfileID,
		ProviderProfile:   za.Profile,
		Platform:          za.Platform,
		DisplayName:       nonEmpty(za.Name, za.Platform),
		AvatarURL:         za.Avatar,
		Status:            "ok",
		Capabilities:      zernioCapabilities(za.Platform),
	}
	if za.AccountID == "" || za.Platform == "" {
		row.Status = "failed"
		return row, errors.New("provider account missing account id or platform")
	}
	rawJSON, _ := json.Marshal(za.Raw)
	capsJSON, _ := json.Marshal(row.Capabilities)
	res, err := ctx.AppDB().Exec(
		`INSERT OR IGNORE INTO social_accounts
		   (project_id, platform, connection_id, external_account_id,
		    display_name, avatar_url, status, profile_id,
		    provider_slug, provider_account_id, provider_profile_id,
		    provider_data, capabilities)
		 VALUES (?, ?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, ?)`,
		pid, za.Platform, connID, za.AccountID,
		row.DisplayName, nullable(za.Avatar), profileID,
		zernioProviderSlug, za.AccountID, za.ProfileID,
		string(rawJSON), string(capsJSON),
	)
	if err != nil {
		row.Status = "failed"
		return row, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		row.Status = "skipped_existing"
		_ = ctx.AppDB().QueryRow(
			`SELECT id FROM social_accounts
			  WHERE project_id=? AND provider_slug=? AND provider_account_id=?`,
			pid, zernioProviderSlug, za.AccountID,
		).Scan(&row.ID)
		return row, nil
	}
	row.ID, _ = res.LastInsertId()
	return row, nil
}

func (a *App) startZernioAccountConnect(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	platform := normalizeZernioPlatform(toString(args["platform"]))
	if !zernioPlatformAllowed(platform) {
		return mcpError(fmt.Sprintf("unsupported Zernio platform %q", platform)), nil
	}
	pid := projectScope(ctx, args)
	profileID := resolveProfileArg(ctx, pid, args)
	if profileID < 0 {
		return mcpError(fmt.Sprintf("profile %q not found in this project — call profile_list to see available slugs", args["profile"])), nil
	}
	if profileID == 0 {
		profileID = projectDefaultProfileID(ctx, pid)
	}
	connID, err := zernioConnectionID(ctx, pid)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	zProfileID, err := a.resolveZernioProfileID(ctx, connID, args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	returnTo := strings.TrimSpace(toString(args["return_to"]))
	if returnTo == "" {
		returnTo = "/api/apps/social/accounts/oauth_done"
	}
	now := time.Now().UTC()
	res, err := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts
		   (project_id, platform, integration_slug, connection_id, status, expires_at, profile_id,
		    provider_slug, provider_profile_id)
		 VALUES (?, ?, ?, ?, 'pending_oauth', ?, ?, ?, ?)`,
		pid, platform, zernioProviderSlug, connID, now.Add(30*time.Minute), profileID,
		zernioProviderSlug, zProfileID,
	)
	if err != nil {
		return nil, fmt.Errorf("create pending Zernio account: %w", err)
	}
	pendingID, _ := res.LastInsertId()
	sep := "?"
	if strings.Contains(returnTo, "?") {
		sep = "&"
	}
	returnURL := fmt.Sprintf("%s%spending=%d&provider=%s", returnTo, sep, pendingID, zernioProviderSlug)
	connectInput := map[string]any{
		"platform":     platform,
		"profileId":    zProfileID,
		"redirect_url": returnURL,
		"headless":     true,
	}
	call, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_connect_url", connectInput)
	if err != nil || call == nil || !call.Success {
		_, _ = ctx.AppDB().Exec(`DELETE FROM pending_accounts WHERE id=?`, pendingID)
		if err != nil {
			return mcpError("Zernio connect URL failed: " + err.Error()), nil
		}
		return mcpError("Zernio connect URL failed: " + upstreamError(call).Error()), nil
	}
	raw := sanitizeRawJSON(call.Data)
	state := firstDeepStringRaw(raw, "state")
	_, _ = ctx.AppDB().Exec(
		`UPDATE pending_accounts SET provider_state=?, provider_data=? WHERE id=?`,
		state, string(raw), pendingID,
	)
	url := firstDeepStringRaw(raw, "url", "authorize_url", "authorizeUrl", "authUrl", "connectUrl")
	if url == "" {
		_, _ = ctx.AppDB().Exec(`DELETE FROM pending_accounts WHERE id=?`, pendingID)
		return mcpError("Zernio did not return an authorize_url"), nil
	}
	return map[string]any{
		"pending_account_id":  pendingID,
		"platform":            platform,
		"provider":            zernioProviderSlug,
		"provider_profile_id": zProfileID,
		"authorize_url":       url,
		"instructions": fmt.Sprintf(
			"Open this URL to connect %s through Zernio. After authorization, call account_list_pending_pages with pending_account_id=%d if a picker is needed, then account_finalize.",
			platform, pendingID,
		),
	}, nil
}

func (a *App) resolveZernioProfileID(ctx *sdk.AppCtx, connID int64, args map[string]any) (string, error) {
	if v := strings.TrimSpace(stringArgAny(args, "provider_profile_id", "zernio_profile_id", "profileId")); v != "" {
		return v, nil
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_profiles", map[string]any{"limit": 1})
	if err != nil {
		return "", err
	}
	if res == nil || !res.Success {
		return "", upstreamError(res)
	}
	items := jsonItems(res.Data, "profiles", "data", "items", "results")
	if len(items) == 0 {
		return "", errors.New("Zernio has no profiles for this API key; create one in Zernio or pass provider_profile_id")
	}
	id := firstString(items[0], "id", "_id", "profileId", "profile_id")
	if id == "" {
		return "", errors.New("Zernio profile response did not include an id")
	}
	return id, nil
}

func (a *App) completeZernioOAuth(ctx *sdk.AppCtx, r *http.Request, row *pendingRow) (int64, bool) {
	if row.connectionID == 0 {
		return 0, false
	}
	q := r.URL.Query()
	state := strings.TrimSpace(q.Get("state"))
	if state == "" {
		state = row.providerState
	}
	input := map[string]any{
		"platform":  row.platform,
		"profileId": row.providerProfileID,
	}
	if code := strings.TrimSpace(q.Get("code")); code != "" {
		input["code"] = code
	}
	if state != "" {
		input["state"] = state
	}
	if len(input) > 3 || input["code"] != nil {
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(row.connectionID, "complete_oauth_callback", input)
		if err != nil {
			ctx.Logger().Warn("zernio oauth callback failed", "pending_id", row.id, "err", err)
			return row.connectionID, false
		}
		if res == nil || !res.Success {
			ctx.Logger().Warn("zernio oauth callback upstream failed", "pending_id", row.id, "err", upstreamError(res))
			return row.connectionID, false
		}
		raw := sanitizeRawJSON(res.Data)
		if state == "" {
			state = firstDeepStringRaw(raw, "state")
		}
		_, _ = ctx.AppDB().Exec(
			`UPDATE pending_accounts SET status='ready', provider_state=?, provider_data=? WHERE id=?`,
			state, string(raw), row.id,
		)
	} else {
		_, _ = ctx.AppDB().Exec(
			`UPDATE pending_accounts SET status='ready', provider_state=? WHERE id=?`,
			state, row.id,
		)
	}
	ctx.Emit("account.oauth_ready", map[string]any{
		"pending_account_id": row.id,
		"connection_id":      row.connectionID,
		"provider":           zernioProviderSlug,
	})
	return row.connectionID, true
}

func (a *App) listZernioPendingPages(ctx *sdk.AppCtx, row *pendingRow) (any, error) {
	if row.connectionID == 0 {
		return mcpError("Zernio connection is missing for this pending account"), nil
	}
	if row.status != "ready" {
		return mcpError("OAuth not yet complete — open the authorize_url first, then re-call this tool"), nil
	}
	data := a.zernioPendingData(ctx, row)
	pages, err := a.zernioSelectionPages(ctx, row, data)
	if err != nil {
		return mcpError("Zernio picker failed: " + err.Error()), nil
	}
	if len(pages) == 0 {
		return map[string]any{
			"pages":           []pageEntry{},
			"requires_picker": false,
			"platform":        row.platform,
			"provider":        zernioProviderSlug,
			"hint":            "No provider selection step is required — call account_finalize with this pending_account_id.",
		}, nil
	}
	return map[string]any{
		"pages":           pages,
		"requires_picker": true,
		"platform":        row.platform,
		"provider":        zernioProviderSlug,
	}, nil
}

func (a *App) finalizeZernioAccount(ctx *sdk.AppCtx, args map[string]any, row *pendingRow) (any, error) {
	if row.connectionID == 0 {
		return mcpError("OAuth not yet complete"), nil
	}
	pid := row.projectID
	if pid == "" {
		pid = projectScope(ctx, args)
	}
	pageID := strings.TrimSpace(toString(args["page_id"]))
	profileID := row.profileID
	if profileID == 0 {
		profileID = projectDefaultProfileID(ctx, pid)
	}
	before, _ := a.listZernioAccounts(ctx, row.connectionID, map[string]any{
		"provider_profile_id": row.providerProfileID,
		"platforms":           []string{row.platform},
	})
	beforeIDs := map[string]bool{}
	for _, za := range before {
		beforeIDs[za.AccountID] = true
	}
	data := a.zernioPendingData(ctx, row)
	var selectRaw json.RawMessage
	if pageID != "" {
		raw, err := a.selectZernioDestination(ctx, row, data, pageID)
		if err != nil {
			return mcpError("Zernio selection failed: " + err.Error()), nil
		}
		selectRaw = raw
		_, _ = ctx.AppDB().Exec(
			`UPDATE pending_accounts SET provider_data=? WHERE id=?`,
			string(raw), row.id,
		)
	}
	accounts, err := a.listZernioAccounts(ctx, row.connectionID, map[string]any{
		"provider_profile_id": row.providerProfileID,
		"platforms":           []string{row.platform},
	})
	if err != nil {
		return mcpError("Zernio account import failed: " + err.Error()), nil
	}
	providerID := firstDeepStringRaw(selectRaw, "accountId", "account_id", "socialAccountId", "social_account_id", "id", "_id")
	var chosen *zernioAccount
	for i := range accounts {
		if providerID != "" && accounts[i].AccountID == providerID {
			chosen = &accounts[i]
			break
		}
	}
	if chosen == nil && pageID != "" && pageID != "__personal" {
		for i := range accounts {
			if accounts[i].AccountID == pageID || firstDeepString(accounts[i].Raw, "pageId", "organizationId", "boardId", "locationId", "phoneNumberId") == pageID {
				chosen = &accounts[i]
				break
			}
		}
	}
	if chosen == nil {
		for i := range accounts {
			if !beforeIDs[accounts[i].AccountID] {
				chosen = &accounts[i]
				break
			}
		}
	}
	if chosen == nil && len(accounts) == 1 {
		chosen = &accounts[0]
	}
	if chosen == nil {
		return mcpError("Zernio connected successfully, but Social could not identify the selected account. Use Import provider to import it explicitly."), nil
	}
	rowOut, err := a.upsertZernioSocialAccount(ctx, pid, row.connectionID, profileID, *chosen)
	if err != nil {
		return nil, err
	}
	_, _ = ctx.AppDB().Exec(`UPDATE pending_accounts SET status='finalized' WHERE id=?`, row.id)
	ctx.Emit("account.added", map[string]any{
		"social_account_id": rowOut.ID,
		"platform":          rowOut.Platform,
		"display_name":      rowOut.DisplayName,
		"provider":          zernioProviderSlug,
	})
	return map[string]any{
		"social_account_id":   rowOut.ID,
		"platform":            rowOut.Platform,
		"display_name":        rowOut.DisplayName,
		"avatar_url":          rowOut.AvatarURL,
		"external_account_id": rowOut.ProviderAccountID,
		"provider":            zernioProviderSlug,
	}, nil
}

func zernioConnectionID(ctx *sdk.AppCtx, pid string) (int64, error) {
	conns, err := ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{
		ProjectID: pid,
		AppSlug:   zernioProviderSlug,
	})
	if err != nil {
		return 0, err
	}
	for _, c := range conns {
		if c.Status == "active" {
			return c.ID, nil
		}
	}
	return 0, errors.New("no active Zernio integration connection found in this project")
}

func (a *App) listZernioAccounts(ctx *sdk.AppCtx, connID int64, args map[string]any) ([]zernioAccount, error) {
	input := map[string]any{}
	if v := strings.TrimSpace(toString(args["provider_profile_id"])); v != "" {
		input["profileId"] = v
	}
	if platforms := stringSliceArg(args, "platforms"); len(platforms) == 1 {
		input["platform"] = platforms[0]
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_accounts", input)
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		return nil, upstreamError(res)
	}
	items := jsonItems(res.Data, "accounts", "data", "items", "results")
	out := make([]zernioAccount, 0, len(items))
	for _, item := range items {
		platform := strings.ToLower(firstString(item, "platform", "provider", "network", "type"))
		platform = normalizeZernioPlatform(platform)
		id := firstString(item, "accountId", "account_id", "id", "_id")
		name := firstString(item, "displayName", "display_name", "name", "username", "handle", "label")
		if name == "" {
			name = id
		}
		profileID, profileName := zernioProfileIdentity(item["profileId"])
		if profileID == "" {
			profileID, profileName = zernioProfileIdentity(item["profile_id"])
		}
		out = append(out, zernioAccount{
			AccountID: id,
			ProfileID: profileID,
			Profile:   profileName,
			Platform:  platform,
			Name:      name,
			Avatar: firstString(item,
				"avatarUrl", "avatar_url",
				"profilePicture", "profile_picture",
				"profilePictureUrl", "profile_picture_url",
				"metadata.profileData.profilePicture",
				"metadata.profileData.profile_picture",
				"metadata.userProfile.profilePicture",
				"profileData.profilePicture",
				"userProfile.profilePicture",
				"picture", "image",
			),
			Status: firstString(item, "status", "state"),
			Raw:    item,
		})
	}
	return out, nil
}

func (a *App) listZernioProfiles(ctx *sdk.AppCtx, connID int64, args map[string]any) ([]zernioProfile, error) {
	input := map[string]any{}
	if v := strings.TrimSpace(toString(args["search"])); v != "" {
		input["search"] = v
	}
	if limit := intArg(args, "limit", 100); limit > 0 {
		input["limit"] = limit
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_profiles", input)
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		return nil, upstreamError(res)
	}
	items := jsonItems(res.Data, "profiles", "data", "items", "results")
	out := make([]zernioProfile, 0, len(items))
	for _, item := range items {
		id, name := zernioProfileIdentity(item)
		if id == "" {
			continue
		}
		out = append(out, zernioProfile{
			ID:               id,
			Name:             nonEmpty(name, id),
			Description:      firstString(item, "description"),
			Color:            firstString(item, "color"),
			IsDefault:        boolFromAny(item["isDefault"]) || boolFromAny(item["is_default"]),
			AccountUsernames: stringSliceAny(item["accountUsernames"], item["account_usernames"]),
		})
	}
	return out, nil
}

func (a *App) zernioPendingData(ctx *sdk.AppCtx, row *pendingRow) map[string]any {
	out := map[string]any{}
	if row.providerData != "" {
		_ = json.Unmarshal([]byte(row.providerData), &out)
	}
	if row.providerState == "" {
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(row.connectionID, "get_pending_oauth_data", map[string]any{
		"state":    row.providerState,
		"platform": row.platform,
	})
	if err != nil || res == nil || !res.Success {
		return out
	}
	var fresh map[string]any
	if json.Unmarshal(res.Data, &fresh) != nil {
		return out
	}
	return mergeMaps(out, fresh)
}

func (a *App) zernioSelectionPages(ctx *sdk.AppCtx, row *pendingRow, data map[string]any) ([]pageEntry, error) {
	base := zernioSelectionBase(row, data)
	switch row.platform {
	case "facebook":
		return a.zernioToolPages(ctx, row.connectionID, "list_facebook_pages", base, "pages", "data", "items", "results")
	case "linkedin":
		pages := []pageEntry{}
		if user := nestedMap(data, "userProfile", "user_profile", "profile"); len(user) > 0 {
			pages = append(pages, pageEntry{
				ID:     "__personal",
				Name:   nonEmpty(firstString(user, "displayName", "display_name", "name", "username"), "Personal LinkedIn profile"),
				Avatar: firstString(user, "profilePicture", "profile_picture", "profilePictureUrl", "profile_picture_url", "avatarUrl"),
			})
		}
		if orgIds := strings.TrimSpace(firstDeepString(data, "orgIds", "organizationIds", "organization_ids")); orgIds != "" && base["tempToken"] != nil {
			orgInput := map[string]any{"tempToken": base["tempToken"], "orgIds": orgIds}
			orgPages, err := a.zernioToolPages(ctx, row.connectionID, "list_linkedin_organizations", orgInput, "organizations", "data", "items", "results")
			if err != nil {
				return nil, err
			}
			pages = append(pages, orgPages...)
		} else {
			pages = append(pages, zernioPagesFromData(data, "organizations", "organizationPages", "orgs")...)
		}
		return pages, nil
	case "pinterest":
		return a.zernioToolPages(ctx, row.connectionID, "list_pinterest_boards", base, "boards", "data", "items", "results")
	case "snapchat":
		return a.zernioToolPages(ctx, row.connectionID, "list_snapchat_profiles", base, "profiles", "publicProfiles", "data", "items", "results")
	case "whatsapp":
		return a.zernioToolPages(ctx, row.connectionID, "list_whatsapp_phone_numbers", base, "phoneNumbers", "phone_numbers", "numbers", "data", "items", "results")
	case "googlebusiness":
		return a.zernioToolPages(ctx, row.connectionID, "list_googlebusiness_locations", base, "locations", "data", "items", "results")
	default:
		return zernioPagesFromData(data, "accounts", "pages", "items", "results"), nil
	}
}

func (a *App) zernioToolPages(ctx *sdk.AppCtx, connID int64, tool string, input map[string]any, keys ...string) ([]pageEntry, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, input)
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		return nil, upstreamError(res)
	}
	return zernioPagesFromRaw(res.Data, keys...), nil
}

func (a *App) selectZernioDestination(ctx *sdk.AppCtx, row *pendingRow, data map[string]any, pageID string) (json.RawMessage, error) {
	base := zernioSelectionBase(row, data)
	input := copyMap(base)
	var tool string
	switch row.platform {
	case "facebook":
		tool = "select_facebook_page"
		input["pageId"] = pageID
		input["userProfile"] = nonEmptyMap(nestedMap(data, "userProfile", "user_profile", "profile"))
	case "linkedin":
		tool = "select_linkedin_organization"
		input["userProfile"] = nonEmptyMap(nestedMap(data, "userProfile", "user_profile", "profile"))
		if pageID == "__personal" {
			input["accountType"] = "personal"
		} else {
			input["accountType"] = "organization"
			if org := findZernioSelection(data, pageID, "organizations", "organizationPages", "orgs"); len(org) > 0 {
				input["selectedOrganization"] = org
			} else {
				input["selectedOrganization"] = map[string]any{"id": pageID}
			}
		}
	case "pinterest":
		tool = "select_pinterest_board"
		input["boardId"] = pageID
		if board := findZernioSelection(data, pageID, "boards"); len(board) > 0 {
			input["boardName"] = firstString(board, "name", "displayName", "display_name", "title")
		}
		input["userProfile"] = nonEmptyMap(nestedMap(data, "userProfile", "user_profile", "profile"))
	case "snapchat":
		tool = "select_snapchat_profile"
		if profile := findZernioSelection(data, pageID, "profiles", "publicProfiles"); len(profile) > 0 {
			input["selectedPublicProfile"] = profile
		} else {
			input["selectedPublicProfile"] = map[string]any{"id": pageID}
		}
		input["userProfile"] = nonEmptyMap(nestedMap(data, "userProfile", "user_profile", "profile"))
	case "whatsapp":
		tool = "select_whatsapp_phone_number"
		number := findZernioSelection(data, pageID, "phoneNumbers", "phone_numbers", "numbers")
		input["phoneNumberId"] = pageID
		input["wabaId"] = firstString(number, "wabaId", "waba_id", "businessAccountId", "business_account_id")
		input["userProfile"] = nonEmptyMap(nestedMap(data, "userProfile", "user_profile", "profile"))
	case "googlebusiness":
		tool = "select_googlebusiness_location"
		location := findZernioSelection(data, pageID, "locations")
		input["locationId"] = pageID
		if accountID := firstString(location, "accountId", "account_id", "accountName", "account_name"); accountID != "" {
			input["accountId"] = accountID
		}
	default:
		return json.RawMessage(`{}`), nil
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(row.connectionID, tool, input)
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		return nil, upstreamError(res)
	}
	return sanitizeRawJSON(res.Data), nil
}

func zernioSelectionBase(row *pendingRow, data map[string]any) map[string]any {
	out := map[string]any{}
	if row.providerProfileID != "" {
		out["profileId"] = row.providerProfileID
	} else if v := firstDeepString(data, "profileId", "profile_id"); v != "" {
		out["profileId"] = v
	}
	if v := firstDeepString(data, "tempToken", "temp_token", "accessToken", "access_token"); v != "" {
		out["tempToken"] = v
	}
	if v := firstDeepString(data, "pendingDataToken", "pending_data_token"); v != "" {
		out["pendingDataToken"] = v
	}
	if v := firstDeepString(data, "connectToken", "connect_token", "X-Connect-Token"); v != "" {
		out["X-Connect-Token"] = v
	}
	return out
}

func (a *App) checkZernioAccount(ctx *sdk.AppCtx, pid string, result accountCheckResult, connID int64, providerAccountID string) accountCheckResult {
	if providerAccountID == "" {
		result.Error = "zernio account has no provider_account_id"
		return result
	}
	result.Details["provider"] = zernioProviderSlug
	result.Details["provider_account_id"] = providerAccountID
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "check_account_health", map[string]any{
		"accountId": providerAccountID,
	})
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if res == nil || !res.Success {
		result.Error = upstreamError(res).Error()
		return result
	}
	result.Status = "ok"
	result.Error = ""
	result.Details["tool"] = "check_account_health"
	var raw map[string]any
	if json.Unmarshal(res.Data, &raw) == nil {
		result.Details["provider_response"] = raw
	}
	return result
}

func (a *App) publishZernio(ctx *sdk.AppCtx, j publishJob) (string, string, error) {
	if j.providerAccountID == "" {
		return "", "", errors.New("zernio-backed account missing provider_account_id")
	}
	input := map[string]any{
		"content":    j.body,
		"publishNow": true,
		"platforms": []any{
			map[string]any{
				"platform":  normalizeZernioPlatform(j.platform),
				"accountId": j.providerAccountID,
			},
		},
	}
	if title := strings.TrimSpace(toString(j.options["title"])); title != "" {
		input["title"] = title
	}
	if visibility := strings.TrimSpace(toString(j.options["visibility"])); visibility != "" {
		input["visibility"] = visibility
	}
	if tags, ok := j.options["tags"]; ok {
		input["tags"] = tags
	}
	if len(j.media) > 0 {
		mediaItems := make([]map[string]any, 0, len(j.media))
		var thumb *mediaItem
		if t, err := a.resolveThumbnailOption(ctx, j.options, j.mediaProjectID); err != nil {
			return "", "", err
		} else {
			thumb = t
		}
		for _, m := range j.media {
			item := map[string]any{
				"url":  m.URL,
				"mime": m.Mime,
			}
			if m.IsVideo() {
				item["type"] = "video"
			} else if m.IsImage() {
				item["type"] = "image"
			}
			if thumb != nil {
				item["thumbnailUrl"] = thumb.URL
			}
			mediaItems = append(mediaItems, item)
		}
		input["mediaItems"] = mediaItems
	}
	if psd := zernioPlatformSpecificData(j.options); len(psd) > 0 {
		platforms := input["platforms"].([]any)
		platform := platforms[0].(map[string]any)
		platform["platformSpecificData"] = psd
	}
	ctx.Logger().Info("publishZernio: calling create_post",
		"platform", j.platform, "provider_account_id", j.providerAccountID, "media_count", len(j.media))
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, "create_post", input)
	if err != nil {
		return "", "", err
	}
	if res == nil || !res.Success {
		return "", "", upstreamError(res)
	}
	providerID, platformID, platformURL := extractZernioPostIdentity(res.Data)
	_, _ = ctx.AppDB().Exec(
		`UPDATE post_targets SET provider_post_id=?, provider_data=? WHERE id=?`,
		nullable(providerID), string(sanitizeRawJSON(res.Data)), j.targetID,
	)
	if platformID == "" {
		platformID = providerID
	}
	return platformID, platformURL, nil
}

func (a *App) importZernioPosts(ctx *sdk.AppCtx, pid string, out importResult, accountID, connID int64, providerAccountID string, profileID int64, limit int) importResult {
	if providerAccountID == "" {
		out.Status = "failed"
		out.Error = "zernio account missing provider_account_id"
		return out
	}
	if profileID == 0 {
		profileID = projectDefaultProfileID(ctx, pid)
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "sync_external_posts", map[string]any{
		"accountId": providerAccountID,
	})
	if err != nil {
		ctx.Logger().Warn("zernio sync_external_posts failed", "account", accountID, "err", err)
	} else if res != nil && !res.Success {
		ctx.Logger().Warn("zernio sync_external_posts upstream failed", "account", accountID, "err", upstreamError(res))
	}
	listArgs := map[string]any{"accountId": providerAccountID}
	if limit > 0 {
		listArgs["limit"] = limit
	}
	list, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_posts", listArgs)
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if list == nil || !list.Success {
		out.Status, out.Error = "failed", upstreamError(list).Error()
		return out
	}
	for _, item := range jsonItems(list.Data, "posts", "data", "items", "results") {
		platformPostID := firstString(item, "platformPostId", "platform_post_id", "externalId", "external_id", "postId", "id", "_id")
		if platformPostID == "" {
			continue
		}
		body := firstString(item, "content", "text", "caption", "description", "title", "body")
		url := firstString(item, "platformUrl", "platform_url", "permalink", "permalinkUrl", "url", "shareUrl")
		publishedAt := firstString(item, "publishedAt", "published_at", "createdAt", "created_at", "scheduledFor")
		imported, err := a.insertImportedPost(ctx, pid, accountID, profileID, body, platformPostID, url, publishedAt, zernioMediaURLs(item))
		if err != nil {
			ctx.Logger().Warn("import: insert zernio post failed", "provider_post_id", platformPostID, "err", err)
			continue
		}
		if imported {
			out.Imported++
		} else {
			out.SkippedExisting++
		}
	}
	out.Status = "ok"
	return out
}

func (a *App) getZernioAccountMetrics(ctx *sdk.AppCtx, out accountMetricsResult, connID int64, providerAccountID string) accountMetricsResult {
	if providerAccountID == "" {
		out.Status = "failed"
		out.Error = "zernio account missing provider_account_id"
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_daily_metrics", map[string]any{
		"accountId": providerAccountID,
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	series := parseZernioMetricSeries(res.Data)
	out.Status = "ok"
	out.Insights = series
	out.Followers = latestZernioInsight(series, "followers", "follower_count", "followers_count")
	out.Views = latestZernioInsight(series, "views", "view_count")
	out.Impressions = latestInsight(series, "impressions")
	out.Reach = latestInsight(series, "reach")
	out.Engagements = latestZernioInsight(series, "engagements", "likes", "comments")
	out.Raw = sanitizeRawJSON(res.Data)
	return out
}

type zernioInboxReport struct {
	Conversations int `json:"conversations"`
	Messages      int `json:"messages"`
	CommentPosts  int `json:"comment_posts"`
	Comments      int `json:"comments"`
}

func (a *App) syncZernioInbox(ctx *sdk.AppCtx, pid string, accountID, connID int64, providerAccountID, platform string) (zernioInboxReport, error) {
	var report zernioInboxReport
	if providerAccountID == "" {
		return report, errors.New("zernio account missing provider_account_id")
	}
	convRes, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_conversations", map[string]any{
		"accountId": providerAccountID,
		"limit":     50,
	})
	if err == nil && convRes != nil && convRes.Success {
		for _, conv := range jsonItems(convRes.Data, "conversations", "data", "items", "results") {
			convID := firstString(conv, "id", "_id", "conversationId", "conversation_id")
			if convID == "" {
				continue
			}
			report.Conversations++
			msgRes, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_conversation_messages", map[string]any{
				"conversationId": convID,
				"limit":          50,
			})
			if err != nil || msgRes == nil || !msgRes.Success {
				continue
			}
			for _, msg := range jsonItems(msgRes.Data, "messages", "data", "items", "results") {
				if a.upsertZernioInboxItem(ctx, pid, accountID, platform, inboxKindDM, convID, msg) {
					report.Messages++
				}
			}
		}
	}
	commentRes, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_commented_posts", map[string]any{
		"accountId": providerAccountID,
		"limit":     50,
	})
	if err == nil && commentRes != nil && commentRes.Success {
		for _, post := range jsonItems(commentRes.Data, "posts", "data", "items", "results") {
			postID := firstString(post, "id", "_id", "postId", "post_id", "platformPostId", "platform_post_id")
			if postID == "" {
				continue
			}
			report.CommentPosts++
			commentsRes, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_post_comments", map[string]any{
				"postId": postID,
				"limit":  100,
			})
			if err != nil || commentsRes == nil || !commentsRes.Success {
				continue
			}
			for _, c := range jsonItems(commentsRes.Data, "comments", "data", "items", "results") {
				if a.upsertZernioInboxItem(ctx, pid, accountID, platform, inboxKindComment, postID, c) {
					report.Comments++
				}
			}
		}
	}
	return report, nil
}

func (a *App) upsertZernioInboxItem(ctx *sdk.AppCtx, pid string, accountID int64, platform, kind, containerID string, item map[string]any) bool {
	extID := firstString(item, "id", "_id", "messageId", "message_id", "commentId", "comment_id")
	if extID == "" {
		return false
	}
	author := nestedMap(item, "author", "from", "sender", "participant")
	occurred := firstString(item, "createdAt", "created_at", "timestamp", "time", "sentAt", "sent_at")
	occurredAt := time.Now().UTC()
	if t, err := time.Parse(time.RFC3339, occurred); err == nil {
		occurredAt = t
	}
	mediaJSON := ""
	if media := zernioMediaURLs(item); len(media) > 0 {
		b, _ := json.Marshal(media)
		mediaJSON = string(b)
	}
	rawJSON, _ := json.Marshal(item)
	_, inserted, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:        pid,
		SocialAccountID:  accountID,
		Platform:         platform,
		Kind:             kind,
		ExternalID:       extID,
		ParentExternalID: firstString(item, "parentId", "parent_id", "parentExternalId", "parent_external_id"),
		ExternalPostID:   containerID,
		AuthorExternalID: firstString(author, "id", "_id", "userId", "user_id"),
		AuthorName:       firstString(author, "name", "displayName", "display_name", "username"),
		AuthorHandle:     firstString(author, "handle", "username"),
		AuthorAvatarURL:  firstString(author, "avatarUrl", "avatar_url", "profilePictureUrl", "profile_picture_url"),
		Body:             firstString(item, "body", "text", "message", "content", "comment"),
		MediaJSON:        mediaJSON,
		Permalink:        firstString(item, "permalink", "permalinkUrl", "url", "shareUrl"),
		OccurredAt:       occurredAt,
		RawJSON:          string(rawJSON),
	})
	if err != nil {
		ctx.Logger().Warn("zernio inbox upsert failed", "account", accountID, "kind", kind, "external_id", extID, "err", err)
		return false
	}
	return inserted
}

func (a *App) zernioInboxReply(ctx *sdk.AppCtx, item *inboxItem, body string) inboxOutcome {
	connID, err := zernioConnForInboxItem(ctx, item)
	if err != nil {
		return inboxOutcome{InboxItemID: item.ID, SocialAccountID: item.SocialAccountID, Platform: item.Platform, Status: "failed", Error: err.Error()}
	}
	var tool string
	input := map[string]any{"message": body}
	switch item.Kind {
	case inboxKindDM:
		tool = "send_inbox_message"
		conversationID := item.ExternalPostID
		if conversationID == "" {
			conversationID = item.ParentExternalID
		}
		if conversationID == "" {
			conversationID = item.ExternalID
		}
		input["conversationId"] = conversationID
	case inboxKindComment, inboxKindMention:
		tool = "reply_to_comment"
		input["postId"] = item.ExternalPostID
		input["commentId"] = item.ExternalID
	default:
		return inboxOutcome{InboxItemID: item.ID, SocialAccountID: item.SocialAccountID, Platform: item.Platform, Status: "unsupported", Reason: "zernio reply unsupported for " + item.Kind}
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, input)
	if err != nil {
		return inboxOutcome{InboxItemID: item.ID, SocialAccountID: item.SocialAccountID, Platform: item.Platform, Status: "failed", Error: err.Error()}
	}
	if res == nil || !res.Success {
		return inboxOutcome{InboxItemID: item.ID, SocialAccountID: item.SocialAccountID, Platform: item.Platform, Status: "failed", Error: upstreamError(res).Error()}
	}
	extID, _, url := extractZernioPostIdentity(res.Data)
	_ = markInboxRepliedByExternalID(ctx.AppDB(), item.SocialAccountID, item.Kind, item.ExternalID)
	return inboxOutcome{InboxItemID: item.ID, SocialAccountID: item.SocialAccountID, Platform: item.Platform, Status: "ok", ExternalID: extID, Permalink: url}
}

func (a *App) zernioCommentModeration(ctx *sdk.AppCtx, item *inboxItem, action string, hide bool) inboxOutcome {
	if item.Kind != inboxKindComment && item.Kind != inboxKindMention {
		return inboxOutcome{InboxItemID: item.ID, SocialAccountID: item.SocialAccountID, Platform: item.Platform, Status: "unsupported", Reason: "zernio moderation requires a comment item"}
	}
	connID, err := zernioConnForInboxItem(ctx, item)
	if err != nil {
		return inboxOutcome{InboxItemID: item.ID, SocialAccountID: item.SocialAccountID, Platform: item.Platform, Status: "failed", Error: err.Error()}
	}
	tool := ""
	switch action {
	case "hide":
		if hide {
			tool = "hide_comment"
		} else {
			tool = "unhide_comment"
		}
	case "like":
		tool = "like_comment"
	case "delete":
		tool = "delete_comment"
	}
	if tool == "" {
		return inboxOutcome{InboxItemID: item.ID, SocialAccountID: item.SocialAccountID, Platform: item.Platform, Status: "unsupported", Reason: "zernio moderation action unsupported"}
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, map[string]any{
		"postId":    item.ExternalPostID,
		"commentId": item.ExternalID,
	})
	if err != nil {
		return inboxOutcome{InboxItemID: item.ID, SocialAccountID: item.SocialAccountID, Platform: item.Platform, Status: "failed", Error: err.Error()}
	}
	if res == nil || !res.Success {
		return inboxOutcome{InboxItemID: item.ID, SocialAccountID: item.SocialAccountID, Platform: item.Platform, Status: "failed", Error: upstreamError(res).Error()}
	}
	return inboxOutcome{InboxItemID: item.ID, SocialAccountID: item.SocialAccountID, Platform: item.Platform, Status: "ok"}
}

func (a *App) editZernioPost(ctx *sdk.AppCtx, out targetEditOutcome, connID int64, providerPostID string, eff map[string]any) targetEditOutcome {
	if providerPostID == "" {
		providerPostID = out.PlatformPostID
	}
	if providerPostID == "" {
		out.Status = "skipped"
		out.Reason = "zernio edit needs provider_post_id or platform_post_id"
		return out
	}
	input := map[string]any{"postId": providerPostID}
	if body, _ := eff["body"].(string); strings.TrimSpace(body) != "" {
		input["content"] = body
	}
	if title := strOption(eff, "title"); title != "" {
		input["title"] = title
	}
	if vis := strOption(eff, "visibility"); vis != "" {
		input["visibility"] = vis
	}
	if tags := anySliceOption(eff, "tags"); len(tags) > 0 {
		input["tags"] = tags
	}
	if len(input) == 1 {
		out.Status = "skipped"
		out.Reason = "zernio edit needs content or metadata"
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "update_post", input)
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	out.Status = "ok"
	return out
}

func (a *App) deleteZernioPost(ctx *sdk.AppCtx, out targetDeleteOutcome, connID int64, providerPostID string) targetDeleteOutcome {
	if providerPostID == "" {
		out.Status = "skipped"
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "delete_post", map[string]any{
		"postId": providerPostID,
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	out.Status = "deleted"
	return out
}

func zernioConnForInboxItem(ctx *sdk.AppCtx, item *inboxItem) (int64, error) {
	var connID int64
	var provider string
	err := ctx.AppDB().QueryRow(
		`SELECT connection_id, COALESCE(provider_slug,'native')
		   FROM social_accounts WHERE id=? AND project_id=?`,
		item.SocialAccountID, item.ProjectID,
	).Scan(&connID, &provider)
	if err != nil {
		return 0, err
	}
	if provider != zernioProviderSlug {
		return 0, errors.New("account is not zernio-backed")
	}
	return connID, nil
}

func latestZernioInsight(series insightSeries, names ...string) int64 {
	for _, name := range names {
		if v := latestInsight(series, name); v != 0 {
			return v
		}
	}
	return 0
}

func parseZernioMetricSeries(raw json.RawMessage) insightSeries {
	items := jsonItems(raw, "metrics", "data", "items", "results", "series")
	out := insightSeries{}
	for _, item := range items {
		t := firstString(item, "date", "day", "time", "timestamp", "point_time", "createdAt")
		if t == "" {
			continue
		}
		for k, v := range item {
			if k == "date" || k == "day" || k == "time" || k == "timestamp" || k == "point_time" || k == "createdAt" {
				continue
			}
			n := insightValueToInt64(v)
			if n == 0 {
				continue
			}
			out[k] = append(out[k], insightPoint{Time: t, Value: n})
		}
	}
	return out
}

func zernioCapabilities(platform string) map[string]any {
	return map[string]any{
		"post":      true,
		"image":     true,
		"video":     true,
		"analytics": true,
		"inbox":     true,
		"comments":  true,
		"provider":  zernioProviderSlug,
		"platform":  platform,
	}
}

func zernioPlatformSpecificData(opts map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range opts {
		switch k {
		case "body", "title", "visibility", "thumbnail_storage_id", "thumbnail_project_id", "thumbnail_frame_ms", "tags":
			continue
		default:
			out[k] = v
		}
	}
	return out
}

func extractZernioPostIdentity(raw json.RawMessage) (providerID, platformID, platformURL string) {
	var data any
	if json.Unmarshal(raw, &data) != nil {
		return "", "", ""
	}
	providerID = firstDeepString(data, "id", "_id", "postId", "post_id")
	platformID = firstDeepString(data, "platformPostId", "platform_post_id", "externalId", "external_id")
	platformURL = firstDeepString(data, "platformUrl", "platform_url", "permalink", "permalinkUrl", "url", "shareUrl")
	return providerID, platformID, platformURL
}

func zernioMediaURLs(item map[string]any) []string {
	out := []string{}
	for _, key := range []string{"mediaUrl", "media_url", "thumbnailUrl", "thumbnail_url", "image", "picture"} {
		if v := toString(item[key]); v != "" {
			out = append(out, v)
		}
	}
	if arr, ok := item["mediaItems"].([]any); ok {
		for _, v := range arr {
			if m, ok := v.(map[string]any); ok {
				if u := firstString(m, "url", "thumbnailUrl", "thumbnail_url"); u != "" {
					out = append(out, u)
				}
			}
		}
	}
	return out
}

func jsonItems(raw json.RawMessage, keys ...string) []map[string]any {
	var anyData any
	if json.Unmarshal(raw, &anyData) != nil {
		return nil
	}
	if arr, ok := anyData.([]any); ok {
		return mapItems(arr)
	}
	if root, ok := anyData.(map[string]any); ok {
		for _, key := range keys {
			v := walkPath(root, key)
			if arr, ok := v.([]any); ok {
				return mapItems(arr)
			}
		}
		if data, ok := root["data"].(map[string]any); ok {
			for _, key := range keys {
				if arr, ok := walkPath(data, key).([]any); ok {
					return mapItems(arr)
				}
			}
		}
	}
	return nil
}

func mapItems(arr []any) []map[string]any {
	out := make([]map[string]any, 0, len(arr))
	for _, v := range arr {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func zernioProfileIdentity(v any) (id, name string) {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x), ""
	case map[string]any:
		return firstString(x, "_id", "id", "profileId", "profile_id"), firstString(x, "name", "displayName", "display_name")
	default:
		return "", ""
	}
}

func stringSliceAny(values ...any) []string {
	out := []string{}
	for _, value := range values {
		switch x := value.(type) {
		case []any:
			for _, item := range x {
				if s := strings.TrimSpace(toString(item)); s != "" {
					out = append(out, s)
				}
			}
		case []string:
			for _, item := range x {
				if s := strings.TrimSpace(item); s != "" {
					out = append(out, s)
				}
			}
		case string:
			if s := strings.TrimSpace(x); s != "" {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return out
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(toString(walkPath(m, key))); v != "" {
			return v
		}
	}
	return ""
}

func nestedMap(m map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if v, ok := walkPath(m, key).(map[string]any); ok {
			return v
		}
	}
	return map[string]any{}
}

func firstDeepString(v any, keys ...string) string {
	switch x := v.(type) {
	case map[string]any:
		for _, key := range keys {
			if s := strings.TrimSpace(toString(x[key])); s != "" {
				return s
			}
		}
		for _, child := range x {
			if s := firstDeepString(child, keys...); s != "" {
				return s
			}
		}
	case []any:
		for _, child := range x {
			if s := firstDeepString(child, keys...); s != "" {
				return s
			}
		}
	}
	return ""
}

func firstDeepStringRaw(raw json.RawMessage, keys ...string) string {
	if len(raw) == 0 {
		return ""
	}
	var data any
	if json.Unmarshal(raw, &data) != nil {
		return ""
	}
	return firstDeepString(data, keys...)
}

func zernioPagesFromRaw(raw json.RawMessage, keys ...string) []pageEntry {
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil {
		return nil
	}
	return zernioPagesFromData(data, keys...)
}

func zernioPagesFromData(data map[string]any, keys ...string) []pageEntry {
	items := []map[string]any{}
	for _, key := range keys {
		if arr, ok := walkPath(data, key).([]any); ok {
			items = mapItems(arr)
			break
		}
	}
	if len(items) == 0 {
		if arr, ok := any(data).([]any); ok {
			items = mapItems(arr)
		}
	}
	out := make([]pageEntry, 0, len(items))
	for _, item := range items {
		id := firstString(item, "id", "_id", "pageId", "page_id", "organizationId", "organization_id", "boardId", "board_id", "locationId", "location_id", "phoneNumberId", "phone_number_id")
		if id == "" {
			continue
		}
		out = append(out, pageEntry{
			ID:     id,
			Name:   nonEmpty(firstString(item, "name", "displayName", "display_name", "title", "username", "phoneNumber", "phone_number"), id),
			Avatar: firstString(item, "avatarUrl", "avatar_url", "profilePicture", "profile_picture", "profilePictureUrl", "profile_picture_url", "picture", "image", "logoUrl", "logo_url"),
		})
	}
	return out
}

func findZernioSelection(data map[string]any, id string, keys ...string) map[string]any {
	for _, key := range keys {
		if arr, ok := walkPath(data, key).([]any); ok {
			for _, v := range mapItems(arr) {
				if firstString(v, "id", "_id", "pageId", "page_id", "organizationId", "organization_id", "boardId", "board_id", "locationId", "location_id", "phoneNumberId", "phone_number_id") == id {
					return v
				}
			}
		}
	}
	return map[string]any{}
}

func mergeMaps(base, extra map[string]any) map[string]any {
	out := copyMap(base)
	for k, v := range extra {
		if bm, ok := out[k].(map[string]any); ok {
			if em, ok := v.(map[string]any); ok {
				out[k] = mergeMaps(bm, em)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func copyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func nonEmptyMap(m map[string]any) map[string]any {
	if len(m) > 0 {
		return m
	}
	return map[string]any{}
}

func normalizeZernioPlatform(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "x":
		return "twitter"
	case "google_business", "google-business", "google_business_profile", "googlebusinessprofile":
		return "googlebusiness"
	default:
		return strings.ToLower(strings.TrimSpace(platform))
	}
}

func nonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func parseProviderTime(v string) sql.NullString {
	v = strings.TrimSpace(v)
	if v == "" {
		return sql.NullString{}
	}
	if _, err := time.Parse(time.RFC3339, v); err == nil {
		return sql.NullString{String: v, Valid: true}
	}
	return sql.NullString{String: v, Valid: true}
}
