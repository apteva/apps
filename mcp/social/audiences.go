package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type audienceRequest struct {
	Type     string   `json:"type"`
	LevelIDs []string `json:"level_ids,omitempty"`
}
type membershipLevel struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Price *float64 `json:"price"`
}
type audienceCapabilities struct {
	Discovery         string `json:"discovery"`
	Publishing        string `json:"publishing"`
	TierTargeting     bool   `json:"tier_targeting"`
	Verification      string `json:"verification"`
	StagingVisibility string `json:"staging_visibility,omitempty"`
}
type audienceAccount struct {
	ID, ConnectionID                                int64
	Project, Platform, Provider, ExternalID, Status string
}

// Each integration/channel adapter owns discovery and publication. The UI and
// target contract do not contain provider-specific membership assumptions.
type audienceAdapter struct {
	SetupURL     func(string) string
	Capabilities audienceCapabilities
	Discover     func(*sdk.AppCtx, audienceAccount) ([]membershipLevel, error)
	Publish      func(*App, *sdk.AppCtx, platformDef, publishJob, audienceRequest) (string, string, error)
}

var audienceAdapters map[string]audienceAdapter

func init() {
	audienceAdapters = map[string]audienceAdapter{
		"native:youtube": {
			Capabilities: audienceCapabilities{Discovery: "supported", Publishing: "manual", TierTargeting: true, Verification: "human_confirmed", StagingVisibility: "unlisted"},
			Discover:     discoverYouTubeLevels,
			SetupURL:     func(id string) string { return "https://studio.youtube.com/video/" + url.PathEscape(id) + "/edit" },
			Publish: func(a *App, ctx *sdk.AppCtx, def platformDef, j publishJob, r audienceRequest) (string, string, error) {
				return a.publishYouTubeMembership(ctx, def, j, r)
			},
		},
	}

}
func adapterForAudience(platform, provider string) audienceAdapter {
	if provider == "" {
		provider = "native"
	}
	if adapter, ok := audienceAdapters[provider+":"+platform]; ok {
		return adapter
	}
	return audienceAdapter{Capabilities: audienceCapabilities{Discovery: "unsupported", Publishing: "unsupported", Verification: "unavailable"}}
}
func loadAudienceAccount(ctx *sdk.AppCtx, pid string, id int64) (audienceAccount, error) {
	var account audienceAccount
	err := ctx.AppDB().QueryRow(`SELECT id,project_id,platform,COALESCE(provider_slug,'native'),connection_id,COALESCE(external_account_id,''),status FROM social_accounts WHERE id=? AND project_id=?`, id, pid).Scan(&account.ID, &account.Project, &account.Platform, &account.Provider, &account.ConnectionID, &account.ExternalID, &account.Status)
	return account, err
}
func parseAudience(options map[string]any) (audienceRequest, error) {
	r := audienceRequest{Type: "standard"}
	raw, present := options["audience"]
	if !present {
		return r, nil
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return r, errors.New("audience must be an object")
	}
	for key := range object {
		if key != "type" && key != "level_ids" {
			return r, fmt.Errorf("unknown audience field %q", key)
		}
	}
	r.Type, _ = object["type"].(string)
	if r.Type != "standard" && r.Type != "members" {
		return r, errors.New("audience.type must be standard or members")
	}
	if rawIDs, present := object["level_ids"]; present {
		data, err := json.Marshal(rawIDs)
		if err != nil {
			return r, err
		}
		if err := json.Unmarshal(data, &r.LevelIDs); err != nil || r.LevelIDs == nil {
			return r, errors.New("audience.level_ids must be an array of tier IDs")
		}
		seen := map[string]bool{}
		for _, id := range r.LevelIDs {
			if strings.TrimSpace(id) == "" || len(id) > 256 || seen[id] {
				return r, errors.New("invalid or duplicated audience tier ID")
			}
			seen[id] = true
		}
	}
	if r.Type != "members" && len(r.LevelIDs) > 0 {
		return r, errors.New("tier IDs require audience.type=members")
	}
	return r, nil
}
func validateAudienceTarget(ctx *sdk.AppCtx, pid string, t targetSpec) error {
	r, err := parseAudience(t.Options)
	if err != nil {
		return err
	}
	if r.Type == "standard" {
		return nil
	}
	account, err := loadAudienceAccount(ctx, pid, t.SocialAccountID)
	if err != nil {
		return err
	}
	adapter := adapterForAudience(account.Platform, account.Provider)
	if adapter.Publish == nil {
		return errors.New("membership publishing is unsupported by this destination")
	}
	if adapter.Capabilities.Publishing == "manual" {
		if visibility := strOption(t.Options, "visibility"); visibility != "" && visibility != adapter.Capabilities.StagingVisibility {
			return fmt.Errorf("membership staging requires %s visibility; remove conflicting visibility", adapter.Capabilities.StagingVisibility)
		}
	}
	if len(r.LevelIDs) == 0 {
		return nil
	}
	var status, raw, successful string
	var connection int64
	err = ctx.AppDB().QueryRow(`SELECT status,levels,successful_at,connection_id FROM social_membership_cache WHERE social_account_id=?`, account.ID).Scan(&status, &raw, &successful, &connection)
	if err != nil || status != "available" || connection != account.ConnectionID || membershipCacheStale(successful) {
		return errors.New("refresh membership discovery before selecting tiers")
	}
	var levels []membershipLevel
	if err := json.Unmarshal([]byte(raw), &levels); err != nil {
		return err
	}
	known := map[string]bool{}
	for _, level := range levels {
		known[level.ID] = true
	}
	for _, id := range r.LevelIDs {
		if !known[id] {
			return errors.New("membership tier does not belong to this destination")
		}
	}
	return nil
}
func membershipCacheStale(at string) bool {
	t, err := time.Parse(time.RFC3339Nano, at)
	return err != nil || time.Since(t) > 24*time.Hour
}
func (a *App) audienceTools() []sdk.Tool {
	props := map[string]any{"social_account_id": map[string]any{"type": "integer"}}
	return []sdk.Tool{
		{Name: "account_audience_capabilities", Description: "Optional: read audience capabilities for a connected account. No provider calls. Membership publishing is native, manual, or unsupported; ordinary publishing is unchanged.", InputSchema: schemaObject(props, []string{"social_account_id"}), Handler: a.toolAudienceCapabilities, Annotations: map[string]any{"readOnlyHint": true}},
		{Name: "account_membership_levels", Description: "Optional: read cached membership tiers. refresh=true explicitly queries this account's existing integration connection. Returns discovery status separately from levels; errors never mean no tiers. YouTube may require OAuth reauthorization and channel-level API approval.", InputSchema: schemaObject(map[string]any{"social_account_id": map[string]any{"type": "integer"}, "refresh": map[string]any{"type": "boolean", "default": false}}, []string{"social_account_id"}), Handler: a.toolMembershipLevels, Annotations: map[string]any{"readOnlyHint": true}},
	}
}
func (a *App) toolAudienceCapabilities(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	account, err := loadAudienceAccount(ctx, projectScope(ctx, args), int64(intArg(args, "social_account_id", 0)))
	if err != nil {
		return mcpError("social account not found in this project"), nil
	}
	return map[string]any{"social_account_id": account.ID, "capabilities": adapterForAudience(account.Platform, account.Provider).Capabilities}, nil
}
func (a *App) toolMembershipLevels(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	account, err := loadAudienceAccount(ctx, projectScope(ctx, args), int64(intArg(args, "social_account_id", 0)))
	if err != nil {
		return mcpError("social account not found in this project"), nil
	}
	unlock := lockSocialResource(ctx, account.ID, "membership-discovery")
	defer unlock()
	adapter := adapterForAudience(account.Platform, account.Provider)
	status, reason, checked, successful := "unknown", "Refresh to discover membership tiers.", "", ""
	levels := []membershipLevel{}
	var raw string
	var connection int64
	if ctx.AppDB().QueryRow(`SELECT status,reason,levels,checked_at,successful_at,connection_id FROM social_membership_cache WHERE social_account_id=?`, account.ID).Scan(&status, &reason, &raw, &checked, &successful, &connection) == nil {
		if connection == account.ConnectionID {
			if err := json.Unmarshal([]byte(raw), &levels); err != nil {
				return nil, err
			}
		} else {
			status, reason, checked, successful = "unknown", "Connection changed; refresh membership tiers.", "", ""
		}
	}
	switch {
	case adapter.Discover == nil:
		status, reason = "unsupported", "This destination does not expose membership discovery."
	case account.Status != "active":
		status, reason = "permission_required", "Reconnect this account before discovering memberships."
	case boolArg(args, "refresh", false):
		checked = time.Now().UTC().Format(time.RFC3339Nano)
		fresh, err := adapter.Discover(ctx, account)
		if err != nil {
			status, reason = membershipErrorStatus(err), safeMembershipError(err)
		} else {
			status, reason = "available", ""
			levels = fresh
			successful = checked
			if len(levels) == 0 {
				status = "empty"
			}
		}
		data, _ := json.Marshal(levels)
		_, err = ctx.AppDB().Exec(`INSERT INTO social_membership_cache(social_account_id,connection_id,status,reason,levels,checked_at,successful_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(social_account_id) DO UPDATE SET connection_id=excluded.connection_id,status=excluded.status,reason=excluded.reason,levels=excluded.levels,checked_at=excluded.checked_at,successful_at=excluded.successful_at`, account.ID, account.ConnectionID, status, reason, string(data), checked, successful)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"social_account_id": account.ID, "connection_id": account.ConnectionID, "status": status, "reason": reason, "levels": levels, "stale": (status != "available" && status != "empty") || membershipCacheStale(successful), "checked_at": checked, "successful_at": successful}, nil
}
func membershipErrorStatus(err error) string {
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "tool not found") || strings.Contains(text, "unknown tool") || strings.Contains(text, "unknown integration"):
		return "catalog_update_required"
	case strings.Contains(text, "insufficient") || strings.Contains(text, "401") || strings.Contains(text, "scope") || strings.Contains(text, "inactive"):
		return "permission_required"
	case strings.Contains(text, "403"):
		return "approval_required"
	default:
		return "error"
	}
}
func safeMembershipError(err error) string {
	switch membershipErrorStatus(err) {
	case "permission_required":
		return "Membership permission is missing or expired. Reauthorize the existing YouTube connection in Integrations."
	case "approval_required":
		return "YouTube denied access. Check channel memberships, OAuth permission, and channel-level API approval."
	case "catalog_update_required":
		return "The integration catalog needs the membership discovery tool."
	default:
		return "Membership discovery failed; cached tiers may be stale. Refresh again."
	}
}
func discoverYouTubeLevels(ctx *sdk.AppCtx, account audienceAccount) ([]membershipLevel, error) {
	conn, err := ctx.PlatformAPI().GetConnection(account.ConnectionID)
	if err != nil {
		return nil, err
	}
	if conn == nil || conn.ID != account.ConnectionID || conn.Status != "active" || conn.ProjectID != account.Project || conn.AppSlug != "youtube-api" {
		return nil, errors.New("YouTube connection is inactive or does not match this project")
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(account.ConnectionID, "get_my_channel", map[string]any{"part": "id"})
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		return nil, upstreamError(res)
	}
	var channel struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if json.Unmarshal(res.Data, &channel) != nil || len(channel.Items) != 1 || channel.Items[0].ID == "" {
		return nil, errors.New("cannot identify connected channel")
	}
	identity := channel.Items[0].ID
	if account.ExternalID != "" && account.ExternalID != identity {
		return nil, errors.New("connection belongs to a different channel")
	}
	res, err = ctx.PlatformAPI().ExecuteIntegrationTool(account.ConnectionID, "list_membership_levels", map[string]any{"part": "id,snippet"})
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		return nil, upstreamError(res)
	}
	var response struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Creator string `json:"creatorChannelId"`
				Details struct {
					Name string `json:"displayName"`
				} `json:"levelDetails"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if json.Unmarshal(res.Data, &response) != nil || response.Items == nil {
		return nil, errors.New("membership response did not contain items")
	}
	levels := []membershipLevel{}
	seen := map[string]bool{}
	for _, item := range response.Items {
		if item.ID == "" || seen[item.ID] || item.Snippet.Creator != identity {
			return nil, errors.New("membership tier belongs to a different channel or is invalid")
		}
		seen[item.ID] = true
		levels = append(levels, membershipLevel{ID: item.ID, Name: item.Snippet.Details.Name})
	}
	return levels, nil
}
func (a *App) handleAudienceAccount(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/audiences/accounts/"), "/"), "/")
	if len(parts) != 2 || r.Method != "GET" {
		http.NotFound(w, r)
		return
	}
	args := scopedQueryArgs(r, "refresh")
	args["social_account_id"] = parts[0]
	var out any
	var err error
	switch parts[1] {
	case "capabilities":
		out, err = a.toolAudienceCapabilities(globalCtx, args)
	case "memberships":
		out, err = a.toolMembershipLevels(globalCtx, args)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Audience request failed", 500)
		return
	}
	writeJSON(w, out)
}
func (a *App) handleAudienceConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	// This authenticated panel action is an operator attestation, never an
	// assertion that the provider API verified members-only access.
	if agent := r.Header.Get("X-Apteva-Caller-Agent"); agent != "" && agent != "0" {
		http.Error(w, "operator confirmation required", 403)
		return
	}
	var body struct {
		PostID           int64 `json:"post_id"`
		TargetID         int64 `json:"target_id"`
		ExpectedRevision int64 `json:"expected_revision"`
		Confirmed        bool  `json:"confirmed"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil || !body.Confirmed {
		http.Error(w, "explicit confirmation is required", 400)
		return
	}
	ctx := globalCtx.WithProject(projectScope(globalCtx, projectArgsFromRequest(r)))
	pid := projectScope(ctx)
	unlock := lockSocialPost(ctx, body.PostID)
	defer unlock()
	var options string
	err := ctx.AppDB().QueryRow(`SELECT COALESCE(t.options,'') FROM post_targets t JOIN posts p ON p.id=t.post_id WHERE t.id=? AND t.post_id=? AND p.project_id=? AND p.revision=? AND t.status='awaiting_audience_setup' AND COALESCE(t.platform_post_id,'')!=''`, body.TargetID, body.PostID, pid, body.ExpectedRevision).Scan(&options)
	if err != nil {
		http.Error(w, "stale or unavailable audience setup", 409)
		return
	}
	var opts map[string]any
	if json.Unmarshal([]byte(options), &opts) != nil {
		http.Error(w, "invalid audience", 409)
		return
	}
	audience, err := parseAudience(opts)
	if err != nil || audience.Type != "members" {
		http.Error(w, "membership audience required", 409)
		return
	}
	actor := r.Header.Get("X-Apteva-User")
	if actor == "" {
		actor = "authenticated_operator"
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		http.Error(w, "confirmation failed", 500)
		return
	}
	defer tx.Rollback()
	current, err := loadRecoveryTargetFrom(tx, pid, body.PostID, body.TargetID)
	if err != nil {
		http.Error(w, "stale confirmation", 409)
		return
	}
	facts, err := loadPublicationFacts(tx, pid, body.TargetID)
	if err != nil {
		http.Error(w, "confirmation failed", 500)
		return
	}
	if facts.PublishedAt != nil {
		facts.PublishedAt = nil
		facts.TimeSource = "unknown_membership_publication"
		if _, err = correctPublicationFacts(tx, pid, current, facts, map[string]any{"kind": "audience_confirmation_is_not_publication_time", "actor": actor}); err != nil {
			http.Error(w, "stale confirmation", 409)
			return
		}
	}
	result, err := tx.Exec(`UPDATE post_targets SET status='published',audience_status='human_confirmed',audience_confirmed_at=CURRENT_TIMESTAMP,audience_confirmation_actor=?,published_at=NULL,delivery_recorded_at=COALESCE(delivery_recorded_at,CURRENT_TIMESTAMP),last_error=NULL WHERE id=? AND post_id=? AND status='awaiting_audience_setup' AND EXISTS(SELECT 1 FROM posts p WHERE p.id=post_targets.post_id AND p.project_id=? AND p.revision=?)`, actor, body.TargetID, body.PostID, pid, body.ExpectedRevision)
	if err != nil {
		http.Error(w, "confirmation failed", 500)
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		http.Error(w, "stale confirmation", 409)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "confirmation failed", 500)
		return
	}
	status := a.rollupPostStatus(ctx, body.PostID)
	ctx.Emit("post.audience_confirmed", map[string]any{"post_id": body.PostID, "target_id": body.TargetID, "verification": "human_confirmed"})
	writeJSON(w, map[string]any{"status": status, "verification": "human_confirmed"})
}
func audienceSetupURL(platform, provider, videoID string) string {
	adapter := adapterForAudience(platform, provider)
	if videoID != "" && adapter.SetupURL != nil {
		return adapter.SetupURL(videoID)
	}
	return ""
}
