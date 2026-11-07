// analytics_retention — audience retention curves for published videos,
// one shape for every platform that reports one: points along the video
// (position 0..1) with the share of the audience still watching, kept as
// decimals so the opening drop-off is visible.
package main

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type retentionPoint struct {
	// Position is how far into the video, 0 (start) to 1 (end).
	Position float64 `json:"position"`
	// AudienceWatchRatio is the share of views still watching here. It can
	// exceed 1 when viewers rewatch a section.
	AudienceWatchRatio float64 `json:"audience_watch_ratio"`
	// RelativeRetention compares with similar videos: above 0.5 means
	// better than typical at this point (YouTube only).
	RelativeRetention *float64 `json:"relative_retention,omitempty"`
}

type retentionCurve struct {
	TargetID        int64            `json:"target_id,omitempty"`
	SocialAccountID int64            `json:"social_account_id"`
	Platform        string           `json:"platform"`
	PlatformPostID  string           `json:"platform_post_id,omitempty"`
	Status          string           `json:"status"` // ok | empty | unsupported | skipped | failed
	Reason          string           `json:"reason,omitempty"`
	Source          string           `json:"source,omitempty"`
	AudienceType    string           `json:"audience_type,omitempty"`
	StartDate       string           `json:"start_date,omitempty"`
	EndDate         string           `json:"end_date,omitempty"`
	Points          []retentionPoint `json:"points,omitempty"`
	Summary         map[string]any   `json:"summary,omitempty"`
}

var youtubeRetentionAudiences = map[string]string{
	"organic":      "ORGANIC",
	"ad_instream":  "AD_INSTREAM",
	"ad_indisplay": "AD_INDISPLAY",
}

func (a *App) toolPostRetention(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectScope(ctx, args)
	ctx = ctx.WithProject(pid)
	audience := strings.ToLower(strings.TrimSpace(toString(args["audience_type"])))
	if audience == "" {
		audience = "organic"
	}
	if audience != "all" && youtubeRetentionAudiences[audience] == "" {
		return mcpError("audience_type must be organic, ad_instream, ad_indisplay or all"), nil
	}
	postID := int64(intArg(args, "post_id", 0))
	accountID := toInt64Loose(args["social_account_id"])
	videoID := strings.TrimSpace(toString(args["platform_post_id"]))
	var targets []metricsTarget
	switch {
	case postID > 0 && videoID != "":
		return mcpError("use post_id, or social_account_id with platform_post_id, not both"), nil
	case postID > 0:
		var err error
		if targets, err = loadMetricsTargets(ctx, pid, postID); err != nil {
			return mcpError(err.Error()), nil
		}
	case accountID > 0 && videoID != "":
		target, err := loadAccountMetricsTarget(ctx, pid, accountID, videoID)
		if err != nil {
			return mcpError(err.Error()), nil
		}
		targets = []metricsTarget{target}
	default:
		return mcpError("post_id, or social_account_id with platform_post_id, required"), nil
	}
	window := retentionWindow{start: dateArg(args, "start_date"), end: dateArg(args, "end_date")}
	curves := make([]retentionCurve, 0, len(targets))
	for _, target := range targets {
		curves = append(curves, a.retentionCurve(ctx, target, audience, window))
	}
	out := map[string]any{"targets": curves}
	if postID > 0 {
		out["post_id"] = postID
	}
	return out, nil
}

// retentionWindow limits the views a curve is built from; empty means
// the video's whole life.
type retentionWindow struct{ start, end string }

func (a *App) retentionCurve(ctx *sdk.AppCtx, target metricsTarget, audience string, window retentionWindow) retentionCurve {
	curve := retentionCurve{TargetID: target.TargetID, SocialAccountID: target.SocialAccountID, Platform: target.Platform, PlatformPostID: target.ExtPostID}
	switch {
	case target.Platform == "" || target.ConnID == 0:
		curve.Status, curve.Reason = "skipped", "social account row gone — was the account disconnected?"
	case target.ExtPostID == "":
		curve.Status, curve.Reason = "skipped", "post has no platform ID yet"
	case target.ProviderSlug != "" && target.ProviderSlug != "native":
		curve.Status, curve.Reason = "unsupported", "Retention curves need a direct connection; provider-backed accounts do not expose them."
	case target.Platform == "youtube":
		a.youtubeRetention(ctx, target, audience, window, &curve)
	case target.Platform == "facebook":
		a.facebookRetention(ctx, target, &curve)
	default:
		curve.Status, curve.Reason = "unsupported", platformDisplayName(target.Platform)+" does not expose audience retention curves."
	}
	if curve.Status == "ok" {
		curve.Summary = retentionSummary(curve.Points)
	}
	return curve
}

func (a *App) youtubeRetention(ctx *sdk.AppCtx, target metricsTarget, audience string, window retentionWindow, curve *retentionCurve) {
	curve.Source = "youtube_analytics"
	curve.AudienceType = audience
	// YouTube times out on very long ranges, so a lifetime curve starts at
	// the video's publish date rather than an early fixed date.
	if window.end == "" {
		window.end = time.Now().UTC().Format("2006-01-02")
	}
	if window.start == "" {
		window.start = youtubePublishDate(ctx, target)
	}
	curve.StartDate, curve.EndDate = window.start, window.end
	filters := "video==" + target.ExtPostID
	if audience != "all" {
		filters += ";audienceType==" + youtubeRetentionAudiences[audience]
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(target.ConnID, "query_analytics_report", map[string]any{
		"ids":        "channel==MINE",
		"startDate":  window.start,
		"endDate":    window.end,
		"metrics":    "audienceWatchRatio,relativeRetentionPerformance",
		"dimensions": "elapsedVideoTimeRatio",
		"filters":    filters,
		"sort":       "elapsedVideoTimeRatio",
	})
	if err != nil {
		curve.Status, curve.Reason = "failed", err.Error()
		return
	}
	if res == nil || !res.Success {
		curve.Status, curve.Reason = "failed", upstreamError(res).Error()
		return
	}
	var resp struct {
		Rows [][]any `json:"rows"`
	}
	_ = json.Unmarshal(res.Data, &resp)
	for _, row := range resp.Rows {
		if len(row) < 2 {
			continue
		}
		point := retentionPoint{Position: insightValueToFloat64(row[0]), AudienceWatchRatio: insightValueToFloat64(row[1])}
		if len(row) > 2 {
			relative := insightValueToFloat64(row[2])
			point.RelativeRetention = &relative
		}
		curve.Points = append(curve.Points, point)
	}
	setRetentionStatus(curve, "YouTube has no retention data for this video and audience yet (new or low-view videos often have none).")
}

// youtubePublishDate is the video's publish day, or a year back when
// YouTube does not say.
func youtubePublishDate(ctx *sdk.AppCtx, target metricsTarget) string {
	fallback := time.Now().UTC().AddDate(-1, 0, 0).Format("2006-01-02")
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(target.ConnID, "get_video", map[string]any{"id": target.ExtPostID, "part": "snippet"})
	if err != nil || res == nil || !res.Success {
		return fallback
	}
	var resp struct {
		Items []struct {
			Snippet struct {
				PublishedAt string `json:"publishedAt"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if json.Unmarshal(res.Data, &resp) != nil || len(resp.Items) == 0 || len(resp.Items[0].Snippet.PublishedAt) < 10 {
		return fallback
	}
	return resp.Items[0].Snippet.PublishedAt[:10]
}

// facebookRetention reads total_video_retention_graph: the share of
// viewers still watching at each of 40 equal steps through the video.
func (a *App) facebookRetention(ctx *sdk.AppCtx, target metricsTarget, curve *retentionCurve) {
	curve.Source = "facebook_video_insights"
	input := map[string]any{"videoId": target.ExtPostID, "metric": "total_video_retention_graph"}
	if token := extractPageToken(target.PageCreds); token != "" {
		input["access_token"] = token
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(target.ConnID, "facebook_get_video_insights", input)
	if err != nil {
		curve.Status, curve.Reason = "failed", err.Error()
		return
	}
	if res == nil || !res.Success {
		curve.Status, curve.Reason = "failed", upstreamError(res).Error()
		return
	}
	curve.Points = parseFacebookRetention(res.Data)
	setRetentionStatus(curve, "Facebook has no retention graph for this post; it exists only for Page videos.")
}

func parseFacebookRetention(raw json.RawMessage) []retentionPoint {
	var resp struct {
		Data []struct {
			Name   string `json:"name"`
			Values []struct {
				Value map[string]any `json:"value"`
			} `json:"values"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &resp) != nil {
		return nil
	}
	for _, metric := range resp.Data {
		if metric.Name != "total_video_retention_graph" || len(metric.Values) == 0 {
			continue
		}
		graph := metric.Values[0].Value
		steps := len(graph)
		points := make([]retentionPoint, 0, steps)
		for key, value := range graph {
			step, err := strconv.Atoi(key)
			if err != nil || steps < 2 {
				continue
			}
			points = append(points, retentionPoint{Position: float64(step) / float64(steps-1), AudienceWatchRatio: insightValueToFloat64(value)})
		}
		sort.Slice(points, func(i, j int) bool { return points[i].Position < points[j].Position })
		return points
	}
	return nil
}

func setRetentionStatus(curve *retentionCurve, emptyReason string) {
	if len(curve.Points) == 0 {
		curve.Status, curve.Reason = "empty", emptyReason
		return
	}
	curve.Status = "ok"
}

// retentionSummary picks out the opening drop-off and the hold through
// the video, read from the closest point at or after each mark.
func retentionSummary(points []retentionPoint) map[string]any {
	at := func(position float64) *float64 {
		for _, point := range points {
			if point.Position >= position-1e-9 {
				value := point.AudienceWatchRatio
				return &value
			}
		}
		return nil
	}
	summary := map[string]any{"points": len(points)}
	for key, position := range map[string]float64{"start": 0, "at_5pct": 0.05, "at_10pct": 0.1, "at_25pct": 0.25, "at_50pct": 0.5, "at_75pct": 0.75} {
		if value := at(position); value != nil {
			summary[key] = *value
		}
	}
	var total float64
	for _, point := range points {
		total += point.AudienceWatchRatio
	}
	summary["average"] = total / float64(len(points))
	return summary
}
