package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	defaultRecordSeconds = 600
	maxRecordSeconds     = 1800
	recordingRetention   = 7 * 24 * time.Hour
	maxMarkers           = 200
)

type Marker struct {
	Name string `json:"name"`
	AtMS int64  `json:"at_ms"`
}

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type Recording struct {
	ID          string   `json:"id"`
	ProjectID   string   `json:"project_id"`
	Kind        string   `json:"kind"`
	ParentID    string   `json:"parent_id,omitempty"`
	SessionID   string   `json:"session_id,omitempty"`
	RunID       string   `json:"run_id,omitempty"`
	DeviceID    string   `json:"device_id,omitempty"`
	Platform    string   `json:"platform,omitempty"`
	Method      string   `json:"method,omitempty"`
	Preset      string   `json:"preset,omitempty"`
	Audio       bool     `json:"audio"`
	Status      string   `json:"status"`
	FileName    string   `json:"file_name,omitempty"`
	ContentType string   `json:"content_type,omitempty"`
	SizeBytes   int64    `json:"size_bytes"`
	DurationMS  int64    `json:"duration_ms"`
	Width       int      `json:"width"`
	Height      int      `json:"height"`
	FPS         float64  `json:"fps"`
	VideoCodec  string   `json:"video_codec,omitempty"`
	AudioCodec  string   `json:"audio_codec,omitempty"`
	Markers     []Marker `json:"markers"`
	Checks      []Check  `json:"checks"`
	Valid       *bool    `json:"valid,omitempty"`
	Error       string   `json:"error,omitempty"`
	CreatedAt   int64    `json:"created_at"`
	StartedAt   int64    `json:"started_at,omitempty"`
	FinishedAt  *int64   `json:"finished_at,omitempty"`
}

type recordOptions struct {
	Quality    string
	Audio      bool
	MaxSeconds int
}

// recordPlan is the capture method chosen for one device and request.
type recordPlan struct {
	Method string
	Audio  bool
	Ext    string
}

// recorder is one running capture. Stop ends it and leaves a finished file
// at the path given to StartRecording.
type recorder interface {
	Stop(context.Context) error
	Done() <-chan struct{}
}

type RecordingBackend interface {
	RecordPlan(Device, recordOptions) (recordPlan, error)
	StartRecording(context.Context, Session, recordPlan, recordOptions, string) (recorder, error)
}

type activeRecording struct {
	mu       sync.Mutex
	rec      Recording
	path     string
	r        recorder
	started  time.Time
	elapsed  time.Duration
	stopOnce sync.Once
	finished chan struct{}
	timer    *time.Timer
}

const recordingColumns = "id,project_id,kind,parent_id,session_id,run_id,device_id,platform,method,preset,audio,status,file_name,content_type,size_bytes,duration_ms,width,height,fps,video_codec,audio_codec,markers_json,checks_json,error,created_at,started_at,finished_at"

func scanRecording(row scanner) (Recording, error) {
	var r Recording
	var audio int
	var markers, checks string
	err := row.Scan(&r.ID, &r.ProjectID, &r.Kind, &r.ParentID, &r.SessionID, &r.RunID, &r.DeviceID, &r.Platform, &r.Method, &r.Preset, &audio, &r.Status, &r.FileName, &r.ContentType, &r.SizeBytes, &r.DurationMS, &r.Width, &r.Height, &r.FPS, &r.VideoCodec, &r.AudioCodec, &markers, &checks, &r.Error, &r.CreatedAt, &r.StartedAt, &r.FinishedAt)
	if err != nil {
		return r, err
	}
	r.Audio = audio == 1
	r.Markers, r.Checks = []Marker{}, []Check{}
	_ = json.Unmarshal([]byte(markers), &r.Markers)
	_ = json.Unmarshal([]byte(checks), &r.Checks)
	if len(r.Checks) > 0 {
		valid := true
		for _, c := range r.Checks {
			valid = valid && c.OK
		}
		r.Valid = &valid
	}
	return r, nil
}

func (e *engine) insertRecording(r Recording) error {
	audio := 0
	if r.Audio {
		audio = 1
	}
	_, err := e.db.Exec(`INSERT INTO mobile_recordings (`+recordingColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.ProjectID, r.Kind, r.ParentID, r.SessionID, r.RunID, r.DeviceID, r.Platform, r.Method, r.Preset, audio, r.Status, r.FileName, r.ContentType, r.SizeBytes, r.DurationMS, r.Width, r.Height, r.FPS, r.VideoCodec, r.AudioCodec, jsonText(r.Markers), jsonText(r.Checks), r.Error, r.CreatedAt, r.StartedAt, r.FinishedAt)
	return err
}

func (e *engine) saveRecording(r Recording) error {
	audio := 0
	if r.Audio {
		audio = 1
	}
	_, err := e.db.Exec(`UPDATE mobile_recordings SET status=?,audio=?,size_bytes=?,duration_ms=?,width=?,height=?,fps=?,video_codec=?,audio_codec=?,markers_json=?,checks_json=?,error=?,finished_at=? WHERE id=?`,
		r.Status, audio, r.SizeBytes, r.DurationMS, r.Width, r.Height, r.FPS, r.VideoCodec, r.AudioCodec, jsonText(r.Markers), jsonText(r.Checks), r.Error, r.FinishedAt, r.ID)
	return err
}

func (e *engine) recording(pid, rid string) (Recording, error) {
	r, err := scanRecording(e.db.QueryRow(`SELECT `+recordingColumns+` FROM mobile_recordings WHERE project_id=? AND id=?`, pid, rid))
	if errors.Is(err, sql.ErrNoRows) {
		return r, problem("not_found", "recording not found")
	}
	return r, err
}

func (e *engine) listRecordings(pid string, args map[string]any) ([]Recording, error) {
	query := `SELECT ` + recordingColumns + ` FROM mobile_recordings WHERE project_id=? AND status<>'deleted'`
	params := []any{pid}
	for _, field := range []string{"status", "run_id", "parent_id", "session_id", "kind"} {
		if v := str(args, field); v != "" {
			query += " AND " + field + "=?"
			params = append(params, v)
		}
	}
	limit := integer(args, "limit", 50)
	if limit < 1 || limit > 200 {
		limit = 50
	}
	query += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT %d", limit)
	rows, err := e.db.Query(query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Recording{}
	for rows.Next() {
		r, err := scanRecording(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (e *engine) recordingPath(r Recording) string {
	return filepath.Join(e.dataDir, "recordings", r.ID+filepath.Ext(r.FileName))
}

// RecordingFile returns a finished recording's path for streaming.
func (e *engine) RecordingFile(pid, rid string) (string, Recording, error) {
	r, err := e.recording(pid, rid)
	if err != nil {
		return "", r, err
	}
	if r.Status != "ready" {
		return "", r, problem("not_found", "recording file is "+r.Status)
	}
	p := e.recordingPath(r)
	if _, err := os.Stat(p); err != nil {
		return "", r, problem("not_found", "recording file is no longer on the runner")
	}
	return p, r, nil
}

func recordingOptions(args map[string]any) (recordOptions, error) {
	o := recordOptions{Quality: str(args, "quality"), MaxSeconds: integer(args, "max_seconds", defaultRecordSeconds)}
	if o.Quality == "" {
		o.Quality = "auto"
	}
	if o.Quality != "auto" && o.Quality != "standard" && o.Quality != "high" {
		return o, problem("invalid_argument", "quality must be auto, standard or high")
	}
	if o.MaxSeconds < 5 || o.MaxSeconds > maxRecordSeconds {
		return o, problem("invalid_argument", fmt.Sprintf("max_seconds must be 5..%d", maxRecordSeconds))
	}
	if _, ok := args["audio"]; ok {
		o.Audio = boolean(args, "audio")
	} else {
		o.Audio = true
	}
	return o, nil
}

func (e *engine) activeForSession(sid string) *activeRecording {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range e.recorders {
		if a.rec.SessionID == sid {
			return a
		}
	}
	return nil
}

// startRecording must run under the session's device gate.
func (e *engine) startRecording(ctx context.Context, s Session, runID string, args map[string]any) (Recording, error) {
	opts, err := recordingOptions(args)
	if err != nil {
		return Recording{}, err
	}
	if e.activeForSession(s.ID) != nil {
		return Recording{}, problem("invalid_argument", "this session is already recording; stop it first")
	}
	explicitAudio := false
	if _, ok := args["audio"]; ok {
		explicitAudio = opts.Audio
	}
	plan, err := e.recordBackend.RecordPlan(s.Device, opts)
	if err != nil {
		return Recording{}, err
	}
	if explicitAudio && !plan.Audio {
		return Recording{}, problem("unsupported", "audio capture is not available for this device and quality; use quality=high with scrcpy (Android 11+) or USB capture (iOS), or pass audio=false")
	}
	now := time.Now()
	r := Recording{ID: id("rec_"), ProjectID: s.ProjectID, Kind: "capture", SessionID: s.ID, RunID: runID, DeviceID: s.DeviceID, Platform: s.Device.Platform, Method: plan.Method, Audio: plan.Audio, Status: "recording", ContentType: contentType(plan.Ext), Markers: []Marker{}, Checks: []Check{}, CreatedAt: now.Unix(), StartedAt: now.Unix()}
	r.FileName = recordingName(s.Device.Name, now, r.ID, plan.Ext)
	if err := os.MkdirAll(filepath.Join(e.dataDir, "recordings"), 0700); err != nil {
		return r, err
	}
	path := e.recordingPath(r)
	e.mu.Lock()
	if e.ctx.Err() != nil {
		e.mu.Unlock()
		return r, problem("infrastructure_error", "runner is shutting down")
	}
	e.mu.Unlock()
	if err := e.insertRecording(r); err != nil {
		return r, err
	}
	rec, err := e.recordBackend.StartRecording(ctx, s, plan, opts, path)
	if err != nil {
		r.Status, r.Error = "failed", err.Error()
		finished := time.Now().Unix()
		r.FinishedAt = &finished
		_ = e.saveRecording(r)
		_ = os.Remove(path)
		return r, err
	}
	// Compilation, USB setup and encoder warm-up are not recorded time. Align
	// the panel timer with the same ready boundary used by markers and limits.
	started := time.Now()
	r.StartedAt = started.Unix()
	if _, err := e.db.Exec(`UPDATE mobile_recordings SET started_at=? WHERE id=?`, r.StartedAt, r.ID); err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = rec.Stop(stopCtx)
		cancel()
		r.Status, r.Error = "failed", "could not persist recording start time"
		finished := time.Now().Unix()
		r.FinishedAt = &finished
		_ = e.saveRecording(r)
		_ = os.Remove(path)
		return r, err
	}
	a := &activeRecording{rec: r, path: path, r: rec, started: started, finished: make(chan struct{})}
	e.mu.Lock()
	e.recorders[r.ID] = a
	e.mu.Unlock()
	a.timer = time.AfterFunc(time.Duration(opts.MaxSeconds)*time.Second, func() { e.stopActive(a, "") })
	e.recWG.Add(1)
	go func() {
		defer e.recWG.Done()
		select {
		case <-rec.Done():
			// The capture process ended without a stop request (crash, unplug,
			// or the tool's own limit). Finalise whatever it produced.
			e.stopActive(a, "capture stopped unexpectedly")
		case <-a.finished:
		}
	}()
	return r, nil
}

// stopActive finalises a capture exactly once, in the background.
func (e *engine) stopActive(a *activeRecording, reason string) {
	a.stopOnce.Do(func() {
		// Freeze the take boundary before recorder shutdown, USB copy or encoding.
		elapsed := time.Since(a.started)
		if a.timer != nil {
			a.timer.Stop()
		}
		a.mu.Lock()
		a.elapsed = elapsed
		if reason == "capture stopped unexpectedly" {
			a.rec.Error = reason
		}
		a.rec.Status = "processing"
		rec := a.rec
		a.mu.Unlock()
		_ = e.saveRecording(rec)
		e.recWG.Add(1)
		go func() {
			defer e.recWG.Done()
			defer close(a.finished)
			e.finalize(a)
		}()
	})
}

func (e *engine) finalize(a *activeRecording) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	stopErr := a.r.Stop(ctx)
	a.mu.Lock()
	r := a.rec
	elapsed := a.elapsed
	a.mu.Unlock()
	var timelineErr error
	if stopErr == nil && (r.Method == methodScreenrecord || r.Method == methodScrcpy) {
		timelineErr = normaliseAndroidTake(ctx, a.path, elapsed, r.Audio)
	}
	finished := time.Now().Unix()
	r.FinishedAt = &finished
	info, statErr := os.Stat(a.path)
	switch {
	case statErr != nil || info.Size() == 0:
		r.Status = "failed"
		r.Error = "capture produced no video"
		if stopErr != nil {
			r.Error = stopErr.Error()
		}
		_ = os.Remove(a.path)
	default:
		r.SizeBytes = info.Size()
		probe, probeErr := probeMedia(ctx, a.path)
		switch {
		case probeErr == nil:
			if timelineErr != nil {
				r.Status, r.Error = "failed", timelineErr.Error()
				break
			}
			if r.Audio && !probe.HasAudio {
				r.Status, r.Error = "failed", "requested device audio was not captured"
				break
			}
			probe.apply(&r)
			r.Status = "ready"
			if stopErr != nil {
				r.Error = "capture ended with an error but produced a playable file: " + stopErr.Error()
			}
		case errors.Is(probeErr, errNoProbe):
			// Without ffprobe the file is kept unverified.
			r.Status = "ready"
			r.Error = "ffprobe is not installed; video metadata is unavailable"
		default:
			r.Status = "failed"
			r.Error = "capture file is not playable: " + probeErr.Error()
			if stopErr != nil {
				r.Error = stopErr.Error()
			}
		}
	}
	_ = e.saveRecording(r)
	e.mu.Lock()
	delete(e.recorders, r.ID)
	e.mu.Unlock()
}

// stopSessionRecordings stops and waits for every capture on a session, so a
// driver session is never closed under an active recorder.
func (e *engine) stopSessionRecordings(sid string, wait time.Duration) {
	e.mu.Lock()
	var active []*activeRecording
	for _, a := range e.recorders {
		if a.rec.SessionID == sid {
			active = append(active, a)
		}
	}
	e.mu.Unlock()
	deadline := time.After(wait)
	for _, a := range active {
		e.stopActive(a, "session closed")
		select {
		case <-a.finished:
		case <-deadline:
			return
		}
	}
}

func (e *engine) recordingMark(pid, rid, name string) (Recording, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return Recording{}, problem("invalid_argument", "marker name must be 1..120 characters")
	}
	e.mu.Lock()
	a := e.recorders[rid]
	e.mu.Unlock()
	if a == nil || a.rec.ProjectID != pid {
		return Recording{}, problem("invalid_argument", "markers can only be added while recording")
	}
	a.mu.Lock()
	if len(a.rec.Markers) >= maxMarkers {
		a.mu.Unlock()
		return Recording{}, problem("invalid_argument", "marker limit reached")
	}
	a.rec.Markers = append(a.rec.Markers, Marker{Name: name, AtMS: time.Since(a.started).Milliseconds()})
	rec := a.rec
	a.mu.Unlock()
	_, err := e.db.Exec(`UPDATE mobile_recordings SET markers_json=? WHERE id=?`, jsonText(rec.Markers), rec.ID)
	return rec, err
}

func (e *engine) markSession(sid, name string) {
	if a := e.activeForSession(sid); a != nil {
		_, _ = e.recordingMark(a.rec.ProjectID, a.rec.ID, name)
	}
}

func (e *engine) recordingStop(pid, rid string, wait time.Duration) (Recording, error) {
	e.mu.Lock()
	a := e.recorders[rid]
	e.mu.Unlock()
	if a == nil || a.rec.ProjectID != pid {
		return e.recording(pid, rid)
	}
	select {
	case <-a.r.Done():
		e.stopActive(a, "capture stopped unexpectedly")
	default:
		e.stopActive(a, "requested")
	}
	select {
	case <-a.finished:
	case <-time.After(wait):
	}
	return e.recording(pid, rid)
}

func (e *engine) recordingDelete(pid, rid string) (Recording, error) {
	r, err := e.recording(pid, rid)
	if err != nil {
		return r, err
	}
	if r.Status == "recording" || r.Status == "processing" {
		return r, problem("invalid_argument", "stop the recording before deleting it")
	}
	_ = os.Remove(e.recordingPath(r))
	r.Status = "deleted"
	_, err = e.db.Exec(`UPDATE mobile_recordings SET status='deleted' WHERE id=?`, r.ID)
	return r, err
}

// pruneRecordings removes runner-local video files past retention. Rows stay
// so storage copies and history remain discoverable.
func (e *engine) pruneRecordings() {
	cutoff := time.Now().Add(-recordingRetention).Unix()
	rows, err := e.db.Query(`SELECT `+recordingColumns+` FROM mobile_recordings WHERE status IN ('ready','failed') AND created_at<? LIMIT 200`, cutoff)
	if err != nil {
		return
	}
	var expired []Recording
	for rows.Next() {
		if r, err := scanRecording(rows); err == nil {
			expired = append(expired, r)
		}
	}
	rows.Close()
	for _, r := range expired {
		_ = os.Remove(e.recordingPath(r))
		_, _ = e.db.Exec(`UPDATE mobile_recordings SET status='expired' WHERE id=?`, r.ID)
	}
}

func (e *engine) recordTool(ctx context.Context, pid string, args map[string]any) (any, error) {
	switch str(args, "action") {
	case "start":
		var out Recording
		_, err := e.withSession(ctx, pid, str(args, "session_id"), func(ctx context.Context, s Session) (any, error) {
			r, err := e.startRecording(ctx, s, "", args)
			out = r
			return nil, err
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"recording": out}, nil
	case "stop":
		rid := str(args, "recording_id")
		if rid == "" && str(args, "session_id") != "" {
			if a := e.activeForSession(str(args, "session_id")); a != nil && a.rec.ProjectID == pid {
				rid = a.rec.ID
			}
		}
		if rid == "" {
			return nil, problem("invalid_argument", "recording_id or a recording session_id is required")
		}
		r, err := e.recordingStop(pid, rid, 20*time.Second)
		return map[string]any{"recording": r}, err
	case "mark":
		r, err := e.recordingMark(pid, str(args, "recording_id"), str(args, "name"))
		return map[string]any{"recording": r}, err
	case "get":
		r, err := e.recording(pid, str(args, "recording_id"))
		return map[string]any{"recording": r}, err
	case "list":
		list, err := e.listRecordings(pid, args)
		return map[string]any{"recordings": list}, err
	case "delete":
		if !boolean(args, "confirm") {
			return nil, problem("invalid_argument", "delete requires confirm=true")
		}
		r, err := e.recordingDelete(pid, str(args, "recording_id"))
		return map[string]any{"recording": r}, err
	case "presets":
		return map[string]any{"presets": builtinPresets()}, nil
	default:
		return nil, problem("invalid_argument", "record action must be start, stop, mark, get, list, delete or presets")
	}
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func recordingName(device string, at time.Time, rid, ext string) string {
	base := strings.Trim(unsafeName.ReplaceAllString(device, "-"), "-.")
	if base == "" {
		base = "device"
	}
	if len(base) > 40 {
		base = base[:40]
	}
	return fmt.Sprintf("%s-%s-%s%s", strings.ToLower(base), at.UTC().Format("20060102-150405"), rid[len(rid)-6:], ext)
}

func contentType(ext string) string {
	if ext == ".mov" {
		return "video/quicktime"
	}
	return "video/mp4"
}
