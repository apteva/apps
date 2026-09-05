package main

import (
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
)

type tikTokCreatorInfo struct {
	Privacy         []string `json:"privacy_level_options"`
	CommentDisabled bool     `json:"comment_disabled"`
	DuetDisabled    bool     `json:"duet_disabled"`
	StitchDisabled  bool     `json:"stitch_disabled"`
	MaxDuration     int64    `json:"max_video_post_duration_sec"`
}

func loadTikTokCreatorInfo(ctx *sdk.AppCtx, connID int64) (tikTokCreatorInfo, error) {
	var out struct {
		Data  tikTokCreatorInfo `json:"data"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "query_creator_info", map[string]any{})
	if err != nil {
		return out.Data, err
	}
	if res == nil || !res.Success {
		return out.Data, upstreamError(res)
	}
	if err = json.Unmarshal(res.Data, &out); err != nil {
		return out.Data, err
	}
	if out.Error.Code != "" && out.Error.Code != "ok" {
		return out.Data, fmt.Errorf("TikTok creator info: %s", out.Error.Message)
	}
	if len(out.Data.Privacy) == 0 {
		return out.Data, fmt.Errorf("TikTok returned no available privacy settings")
	}
	return out.Data, nil
}
func tikTokPostInfo(ctx *sdk.AppCtx, j publishJob) (map[string]any, error) {
	info, err := loadTikTokCreatorInfo(ctx, j.connID)
	if err != nil {
		return nil, err
	}
	privacy := strOption(j.options, "privacy_level")
	allowed := false
	for _, p := range info.Privacy {
		if p == privacy {
			allowed = true
		}
	}
	if !allowed {
		return nil, fmt.Errorf("choose a valid TikTok privacy_level from the creator's current options")
	}
	post := map[string]any{"privacy_level": privacy}
	for _, setting := range []struct {
		name     string
		disabled bool
	}{{"disable_comment", info.CommentDisabled}, {"disable_duet", info.DuetDisabled}, {"disable_stitch", info.StitchDisabled}} {
		disabled, _ := boolOption(j.options, setting.name)
		post[setting.name] = disabled || setting.disabled
	}
	for _, m := range j.media {
		if m.IsVideo() && info.MaxDuration > 0 && m.Duration > float64(info.MaxDuration) {
			return nil, fmt.Errorf("TikTok video exceeds creator limit of %d seconds", info.MaxDuration)
		}
	}
	return post, nil
}
func (a *App) handleTikTokCreatorInfo(w http.ResponseWriter, r *http.Request, id int64) {
	ctx := globalCtx.WithProject(projectScope(globalCtx, projectArgsFromRequest(r)))
	var conn int64
	if err := ctx.AppDB().QueryRow(`SELECT connection_id FROM social_accounts WHERE id=? AND project_id=? AND status='active' AND platform='tiktok' AND COALESCE(provider_slug,'native')='native'`, id, projectScope(ctx)).Scan(&conn); err != nil {
		http.Error(w, "account not found", 404)
		return
	}
	info, err := loadTikTokCreatorInfo(ctx, conn)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	writeJSON(w, info)
}
