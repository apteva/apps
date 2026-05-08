package main

import (
	sdk "github.com/apteva/app-sdk"
)

// streamingCaller is the abstraction over CallAppResult("streaming", …)
// that lets tests inject a fake. Production wiring uses
// platformStreamingCaller, which forwards to the SDK's PlatformAPI.

type streamingCaller interface {
	CreateStream(req CreateStreamReq) (CreateStreamResp, error)
	GetStream(id int64) (StreamSnapshot, error)
	StopStream(id int64) error
	DeleteStream(id int64) error
	GetMetrics(id int64) (StreamMetrics, error)
	ReplayURL(id int64) (ReplayURLs, error)
}

type CreateStreamReq struct {
	Name      string `json:"name"`
	OwnerApp  string `json:"owner_app,omitempty"`
	OwnerTag  string `json:"owner_tag,omitempty"`
	Record    bool   `json:"record"`
	ProjectID string `json:"_project_id,omitempty"`
}

type CreateStreamResp struct {
	Stream StreamSnapshot `json:"stream"`
}

type StreamSnapshot struct {
	ID                 int64   `json:"id"`
	Name               string  `json:"name"`
	OwnerApp           string  `json:"owner_app,omitempty"`
	OwnerTag           string  `json:"owner_tag,omitempty"`
	IngestPort         int     `json:"ingest_port,omitempty"`
	IngestURL          string  `json:"ingest_url,omitempty"`
	StreamKey          string  `json:"stream_key,omitempty"`
	PlaybackURL        string  `json:"playback_url,omitempty"`
	PlaybackToken      string  `json:"playback_token,omitempty"`
	Status             string  `json:"status"`
	CurrentBitrateKbps int     `json:"current_bitrate_kbps,omitempty"`
	CurrentFPS         float64 `json:"current_fps,omitempty"`
	Resolution         string  `json:"resolution,omitempty"`
	CurrentViewers     int     `json:"current_viewers"`
	PeakViewers        int     `json:"peak_viewers"`
	TotalViewerSeconds int     `json:"total_viewer_seconds"`
	StartedAt          string  `json:"started_at,omitempty"`
	EndedAt            string  `json:"ended_at,omitempty"`
}

type StreamMetrics struct {
	ID                 int64   `json:"id"`
	Status             string  `json:"status"`
	CurrentBitrateKbps int     `json:"current_bitrate_kbps,omitempty"`
	CurrentFPS         float64 `json:"current_fps,omitempty"`
	Resolution         string  `json:"resolution,omitempty"`
	CurrentViewers     int     `json:"current_viewers"`
	PeakViewers        int     `json:"peak_viewers"`
	TotalViewerSeconds int     `json:"total_viewer_seconds"`
	UptimeSeconds      int     `json:"uptime_seconds"`
}

type ReplayURLs struct {
	Available bool   `json:"available"`
	HLSURL    string `json:"hls_url,omitempty"`
	MP4URL    string `json:"mp4_url,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// ─── Production wiring ────────────────────────────────────────────

type platformStreamingCaller struct {
	ctx *sdk.AppCtx
}

func newPlatformStreamingCaller(ctx *sdk.AppCtx) streamingCaller {
	return &platformStreamingCaller{ctx: ctx}
}

func (p *platformStreamingCaller) CreateStream(req CreateStreamReq) (CreateStreamResp, error) {
	args := map[string]any{
		"name":      req.Name,
		"owner_app": req.OwnerApp,
		"owner_tag": req.OwnerTag,
		"record":    req.Record,
	}
	if req.ProjectID != "" {
		args["_project_id"] = req.ProjectID
	}
	var out CreateStreamResp
	err := p.ctx.PlatformAPI().CallAppResult("streaming", "streams_create", args, &out)
	return out, err
}

func (p *platformStreamingCaller) GetStream(id int64) (StreamSnapshot, error) {
	var wrap struct {
		Stream StreamSnapshot `json:"stream"`
		Found  bool           `json:"found"`
	}
	err := p.ctx.PlatformAPI().CallAppResult("streaming", "streams_get",
		map[string]any{"id": id}, &wrap)
	return wrap.Stream, err
}

func (p *platformStreamingCaller) StopStream(id int64) error {
	var out map[string]any
	return p.ctx.PlatformAPI().CallAppResult("streaming", "streams_stop",
		map[string]any{"id": id}, &out)
}

func (p *platformStreamingCaller) DeleteStream(id int64) error {
	var out map[string]any
	return p.ctx.PlatformAPI().CallAppResult("streaming", "streams_delete",
		map[string]any{"id": id}, &out)
}

func (p *platformStreamingCaller) GetMetrics(id int64) (StreamMetrics, error) {
	var out StreamMetrics
	err := p.ctx.PlatformAPI().CallAppResult("streaming", "streams_get_metrics",
		map[string]any{"id": id}, &out)
	return out, err
}

func (p *platformStreamingCaller) ReplayURL(id int64) (ReplayURLs, error) {
	var out ReplayURLs
	err := p.ctx.PlatformAPI().CallAppResult("streaming", "streams_replay_url",
		map[string]any{"id": id}, &out)
	return out, err
}
