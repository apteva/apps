package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: voice-notes
display_name: Voice Notes
version: 0.1.3
description: Lightweight audio notes backed by Storage with optional Deepgram transcription.
author: Apteva
icon: icon.svg
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.apps.call
    - platform.connections.execute
  apps:
    - name: storage
      version: ">=0.8.1"
      reason: Stores raw audio recordings and mints signed playback/transcription URLs.
  integrations:
    - role: transcripts
      kind: integration
      compatible_slugs: [deepgram]
      capabilities: [audio.transcribe]
      tools:
        audio.transcribe: listen
        transcribe: listen
      required: false
      label: "Speech-to-text provider"
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: voice_notes_create,         description: "Create a text/manual voice note shell." }
    - { name: voice_notes_upload_audio,   description: "Upload audio to Storage and create a note." }
    - { name: voice_notes_get,            description: "Fetch one voice note." }
    - { name: voice_notes_list,           description: "List/search voice notes." }
    - { name: voice_notes_transcribe,     description: "Transcribe a saved note using Deepgram." }
    - { name: voice_notes_set_transcript, description: "Manually set a note transcript." }
  ui_panels:
    - slot: project.page
      label: Voice Notes
      icon: mic
      entry: /ui/VoiceNotesPanel.mjs
  publishes:
    - name: voice_note.created
      description: A voice note was created.
    - name: voice_note.transcribed
      description: A voice note transcript was saved.
runtime:
  kind: source
  source: { repo: github.com/apteva/apps, ref: main, entry: mcp/voice-notes }
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/voice-notes.db
  migrations: migrations/
config_schema:
  - { name: default_folder, type: text, default: "/voice-notes/", label: "Default storage folder" }
  - { name: auto_transcribe, type: bool, default: true, label: "Auto-transcribe" }
  - { name: transcribe_model, type: text, default: "nova-3", label: "Deepgram model" }
  - { name: transcribe_language, type: text, default: "auto", label: "Language" }
upgrade_policy: auto-patch
`

var globalCtx *sdk.AppCtx

type App struct{}

func main() { sdk.Run(&App{}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("voice-notes requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("voice-notes mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/notes", Handler: a.handleNotes},
		{Pattern: "/notes/", Handler: a.handleNoteItem},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "voice_notes_create",
			Description: "Create a note shell or manual transcript note. Args: title?, transcript_text?, tags?.",
			InputSchema: schemaObject(map[string]any{
				"title":           sString(),
				"transcript_text": sString(),
				"tags":            sArray("string"),
			}, nil),
			Handler: a.toolCreate,
		},
		{
			Name:        "voice_notes_upload_audio",
			Description: "Upload a base64 audio recording to Storage, create a note, and optionally transcribe it. Args: name, content_base64, content_type?, title?, duration_ms?, transcribe?.",
			InputSchema: schemaObject(map[string]any{
				"name":           sString(),
				"content_base64": sString(),
				"content_type":   sString(),
				"title":          sString(),
				"duration_ms":    sInteger(),
				"transcribe":     sBool(),
				"tags":           sArray("string"),
			}, []string{"name", "content_base64"}),
			Handler: a.toolUploadAudio,
		},
		{
			Name:        "voice_notes_get",
			Description: "Fetch one note by id, including signed playback URL when available. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": sInteger(),
			}, []string{"id"}),
			Handler: a.toolGet,
		},
		{
			Name:        "voice_notes_list",
			Description: "List/search notes. Args: q?, status?, limit?.",
			InputSchema: schemaObject(map[string]any{
				"q":      sString(),
				"status": sString(),
				"limit":  sInteger(),
			}, nil),
			Handler: a.toolList,
		},
		{
			Name:        "voice_notes_transcribe",
			Description: "Transcribe a saved note using the bound Deepgram connection. Args: id, force?.",
			InputSchema: schemaObject(map[string]any{
				"id":    sInteger(),
				"force": sBool(),
			}, []string{"id"}),
			Handler: a.toolTranscribe,
		},
		{
			Name:        "voice_notes_set_transcript",
			Description: "Manually set or replace a transcript. Args: id, text, language?.",
			InputSchema: schemaObject(map[string]any{
				"id":       sInteger(),
				"text":     sString(),
				"language": sString(),
			}, []string{"id", "text"}),
			Handler: a.toolSetTranscript,
		},
	}
}

type VoiceNote struct {
	ID                     int64           `json:"id"`
	ProjectID              string          `json:"project_id,omitempty"`
	Title                  string          `json:"title"`
	Status                 string          `json:"status"`
	StorageFileID          string          `json:"storage_file_id,omitempty"`
	StorageURL             string          `json:"storage_url,omitempty"`
	PlaybackURL            string          `json:"playback_url,omitempty"`
	FileName               string          `json:"file_name,omitempty"`
	ContentType            string          `json:"content_type,omitempty"`
	SizeBytes              int64           `json:"size_bytes,omitempty"`
	DurationMS             int64           `json:"duration_ms,omitempty"`
	TranscriptStatus       string          `json:"transcript_status"`
	TranscriptText         string          `json:"transcript_text,omitempty"`
	TranscriptLanguage     string          `json:"transcript_language,omitempty"`
	TranscriptProvider     string          `json:"transcript_provider,omitempty"`
	TranscriptModel        string          `json:"transcript_model,omitempty"`
	TranscriptSegmentsJSON json.RawMessage `json:"transcript_segments,omitempty"`
	ErrorMessage           string          `json:"error_message,omitempty"`
	TagsJSON               json.RawMessage `json:"tags,omitempty"`
	RecordedAt             string          `json:"recorded_at,omitempty"`
	CreatedAt              string          `json:"created_at"`
	UpdatedAt              string          `json:"updated_at"`
}

type storageUploadResult struct {
	ID        int64  `json:"id"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Folder    string `json:"folder"`
	Name      string `json:"name"`
}

type storageURLResult struct {
	URL       string `json:"url"`
	ExpiresAt any    `json:"expires_at,omitempty"`
}

type parsedTranscript struct {
	Text     string
	Language string
	Segments []transcriptSegment
}

type transcriptSegment struct {
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Text    string `json:"text"`
	Speaker string `json:"speaker,omitempty"`
}

func (a *App) toolCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(strArg(args, "title"))
	text := strings.TrimSpace(strArg(args, "transcript_text"))
	if title == "" {
		title = deriveTitle(text)
	}
	if title == "" {
		title = "Untitled voice note"
	}
	status := "draft"
	tstatus := "none"
	if text != "" {
		status = "ready"
		tstatus = "manual"
	}
	id, err := insertNote(ctx.AppDB(), pid, &noteInput{
		Title:            title,
		Status:           status,
		TranscriptStatus: tstatus,
		TranscriptText:   text,
		TranscriptProvider: func() string {
			if text != "" {
				return "manual"
			}
			return ""
		}(),
		TagsJSON: tagsJSON(args["tags"]),
	})
	if err != nil {
		return nil, err
	}
	n, err := getNote(ctx, pid, id, true)
	if err != nil {
		return nil, err
	}
	emit(ctx, pid, "voice_note.created", n)
	return map[string]any{"note": n}, nil
}

func (a *App) toolUploadAudio(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	n, err := a.createFromAudio(ctx.WithProject(pid), pid, uploadRequest{
		Name:          strArg(args, "name"),
		Title:         strArg(args, "title"),
		ContentBase64: strArg(args, "content_base64"),
		ContentType:   strArg(args, "content_type"),
		DurationMS:    int64Arg(args, "duration_ms"),
		TagsJSON:      tagsJSON(args["tags"]),
		Transcribe:    boolArgDefault(args, "transcribe", configBool(ctx.Config().Get("auto_transcribe"), true)),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"note": n}, nil
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	n, err := getNote(ctx.WithProject(pid), pid, int64Arg(args, "id"), true)
	if err != nil {
		return nil, err
	}
	return map[string]any{"note": n}, nil
}

func (a *App) toolList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	notes, err := listNotes(ctx.AppDB(), pid, strArg(args, "q"), strArg(args, "status"), intArg(args, "limit", 50))
	if err != nil {
		return nil, err
	}
	return map[string]any{"notes": notes, "count": len(notes)}, nil
}

func (a *App) toolTranscribe(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	n, err := a.transcribe(ctx.WithProject(pid), pid, int64Arg(args, "id"), boolArg(args, "force"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"note": n}, nil
}

func (a *App) toolSetTranscript(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	text := strings.TrimSpace(strArg(args, "text"))
	if id == 0 || text == "" {
		return nil, errors.New("id and text required")
	}
	if err := updateTranscript(ctx.AppDB(), pid, id, parsedTranscript{Text: text, Language: strArg(args, "language")}, "manual", ""); err != nil {
		return nil, err
	}
	n, err := getNote(ctx.WithProject(pid), pid, id, true)
	if err != nil {
		return nil, err
	}
	emit(ctx, pid, "voice_note.transcribed", n)
	return map[string]any{"note": n}, nil
}

type uploadRequest struct {
	Name          string
	Title         string
	ContentBase64 string
	ContentType   string
	DurationMS    int64
	TagsJSON      string
	Transcribe    bool
}

func (a *App) createFromAudio(ctx *sdk.AppCtx, pid string, req uploadRequest) (*VoiceNote, error) {
	if strings.TrimSpace(req.Name) == "" {
		req.Name = defaultAudioName()
	}
	if req.ContentBase64 == "" {
		return nil, errors.New("content_base64 required")
	}
	if req.ContentType == "" {
		req.ContentType = contentTypeFromName(req.Name)
	}
	if req.ContentType == "" {
		req.ContentType = "audio/webm"
	}
	body, err := base64.StdEncoding.DecodeString(req.ContentBase64)
	if err != nil {
		return nil, fmt.Errorf("decode content_base64: %w", err)
	}
	up, err := uploadToStorage(ctx, req.Name, defaultFolder(ctx), req.ContentType, body)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = strings.TrimSuffix(up.Name, extSuffix(up.Name))
	}
	if title == "" {
		title = "Voice note"
	}
	id, err := insertNote(ctx.AppDB(), pid, &noteInput{
		Title:            title,
		Status:           "recorded",
		StorageFileID:    strconv.FormatInt(up.ID, 10),
		StorageURL:       up.URL,
		FileName:         up.Name,
		ContentType:      req.ContentType,
		SizeBytes:        up.SizeBytes,
		DurationMS:       req.DurationMS,
		TranscriptStatus: "none",
		TagsJSON:         req.TagsJSON,
		RecordedAt:       time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return nil, err
	}
	n, err := getNote(ctx, pid, id, true)
	if err != nil {
		return nil, err
	}
	emit(ctx, pid, "voice_note.created", n)
	if req.Transcribe && ctx.IntegrationFor("transcripts") != nil {
		if transcribed, err := a.transcribe(ctx, pid, id, false); err == nil {
			return transcribed, nil
		}
	}
	return n, nil
}

func (a *App) transcribe(ctx *sdk.AppCtx, pid string, id int64, force bool) (*VoiceNote, error) {
	n, err := getNote(ctx, pid, id, false)
	if err != nil {
		return nil, err
	}
	if n.StorageFileID == "" {
		return nil, errors.New("note has no audio file")
	}
	if n.TranscriptStatus == "ok" && !force {
		return n, nil
	}
	bound := ctx.IntegrationFor("transcripts")
	if bound == nil {
		return nil, errors.New("no Deepgram transcript integration bound")
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE voice_notes SET transcript_status='running', status='transcribing', error_message='', updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`,
		pid, id); err != nil {
		return nil, err
	}
	fileID, err := strconv.ParseInt(n.StorageFileID, 10, 64)
	if err != nil {
		_ = markTranscriptFailed(ctx.AppDB(), pid, id, "storage_file_id is not numeric")
		return nil, err
	}
	signed, err := signedStorageURL(ctx, fileID)
	if err != nil {
		_ = markTranscriptFailed(ctx.AppDB(), pid, id, "storage.files_get_url: "+err.Error())
		return nil, err
	}
	model := strings.TrimSpace(ctx.Config().Get("transcribe_model"))
	if model == "" {
		model = "nova-3"
	}
	language := strings.TrimSpace(ctx.Config().Get("transcribe_language"))
	if language == "" {
		language = "auto"
	}
	input := map[string]any{
		"url":          signed.URL,
		"model":        model,
		"smart_format": true,
		"paragraphs":   true,
	}
	if language == "auto" {
		input["detect_language"] = true
	} else {
		input["language"] = language
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, bound.ToolFor("transcribe"), input)
	if err != nil {
		_ = markTranscriptFailed(ctx.AppDB(), pid, id, "deepgram call: "+err.Error())
		return nil, err
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		_ = markTranscriptFailed(ctx.AppDB(), pid, id, "deepgram non-2xx: "+truncate(body, 500))
		return getNote(ctx, pid, id, true)
	}
	parsed, err := parseDeepgramResponse(res.Data)
	if err != nil {
		_ = markTranscriptFailed(ctx.AppDB(), pid, id, "parse deepgram: "+err.Error())
		return nil, err
	}
	if err := updateTranscript(ctx.AppDB(), pid, id, *parsed, "deepgram", model); err != nil {
		return nil, err
	}
	out, err := getNote(ctx, pid, id, true)
	if err != nil {
		return nil, err
	}
	emit(ctx, pid, "voice_note.transcribed", out)
	return out, nil
}

func (a *App) handleNotes(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := globalCtx.WithProject(pid)
	switch r.Method {
	case http.MethodGet:
		notes, err := listNotes(ctx.AppDB(), pid, r.URL.Query().Get("q"), r.URL.Query().Get("status"), intQuery(r, "limit", 50))
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		attachPlaybackURLs(ctx, notes)
		httpJSON(w, map[string]any{"notes": notes})
	case http.MethodPost:
		var body struct {
			Title         string   `json:"title"`
			Name          string   `json:"name"`
			ContentBase64 string   `json:"content_base64"`
			ContentType   string   `json:"content_type"`
			DurationMS    int64    `json:"duration_ms"`
			Transcribe    *bool    `json:"transcribe"`
			Tags          []string `json:"tags"`
			Transcript    string   `json:"transcript_text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		if body.ContentBase64 != "" {
			transcribe := configBool(ctx.Config().Get("auto_transcribe"), true)
			if body.Transcribe != nil {
				transcribe = *body.Transcribe
			}
			n, err := a.createFromAudio(ctx, pid, uploadRequest{
				Name:          body.Name,
				Title:         body.Title,
				ContentBase64: body.ContentBase64,
				ContentType:   body.ContentType,
				DurationMS:    body.DurationMS,
				TagsJSON:      tagsJSON(body.Tags),
				Transcribe:    transcribe,
			})
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, map[string]any{"note": n})
			return
		}
		out, err := a.toolCreate(ctx, map[string]any{
			"_project_id":     pid,
			"title":           body.Title,
			"transcript_text": body.Transcript,
			"tags":            body.Tags,
		})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleNoteItem(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := globalCtx.WithProject(pid)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/notes/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			n, err := getNote(ctx, pid, id, true)
			if err != nil {
				httpErr(w, http.StatusNotFound, err.Error())
				return
			}
			httpJSON(w, map[string]any{"note": n})
		case http.MethodPatch:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if err := updateNoteFields(ctx.AppDB(), pid, id, body); err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			n, _ := getNote(ctx, pid, id, true)
			httpJSON(w, map[string]any{"note": n})
		default:
			httpErr(w, http.StatusMethodNotAllowed, "GET or PATCH")
		}
		return
	}
	switch parts[1] {
	case "transcribe":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		n, err := a.transcribe(ctx, pid, id, r.URL.Query().Get("force") == "1")
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"note": n})
	case "transcript":
		if r.Method != http.MethodPut {
			httpErr(w, http.StatusMethodNotAllowed, "PUT")
			return
		}
		var body struct {
			Text     string `json:"text"`
			Language string `json:"language"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		out, err := a.toolSetTranscript(ctx, map[string]any{"_project_id": pid, "id": id, "text": body.Text, "language": body.Language})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusNotFound, "not found")
	}
}

type noteInput struct {
	Title              string
	Status             string
	StorageFileID      string
	StorageURL         string
	FileName           string
	ContentType        string
	SizeBytes          int64
	DurationMS         int64
	TranscriptStatus   string
	TranscriptText     string
	TranscriptProvider string
	TagsJSON           string
	RecordedAt         string
}

func insertNote(db *sql.DB, pid string, in *noteInput) (int64, error) {
	if in.Status == "" {
		in.Status = "draft"
	}
	if in.TranscriptStatus == "" {
		in.TranscriptStatus = "none"
	}
	if in.TagsJSON == "" {
		in.TagsJSON = "[]"
	}
	res, err := db.Exec(`
		INSERT INTO voice_notes (
			project_id, title, status, storage_file_id, storage_url, file_name,
			content_type, size_bytes, duration_ms, transcript_status,
			transcript_text, transcript_provider, tags_json, recorded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, in.Title, in.Status, in.StorageFileID, in.StorageURL, in.FileName,
		in.ContentType, in.SizeBytes, in.DurationMS, in.TranscriptStatus,
		in.TranscriptText, in.TranscriptProvider, in.TagsJSON, in.RecordedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func getNote(ctx *sdk.AppCtx, pid string, id int64, withURL bool) (*VoiceNote, error) {
	if id == 0 {
		return nil, errors.New("id required")
	}
	n := &VoiceNote{}
	var segs, tags string
	err := ctx.AppDB().QueryRow(`
		SELECT id, project_id, title, status, storage_file_id, storage_url,
		       file_name, content_type, size_bytes, duration_ms, transcript_status,
		       transcript_text, transcript_language, transcript_provider,
		       transcript_model, transcript_segments_json, error_message,
		       tags_json, COALESCE(recorded_at,''), created_at, updated_at
		  FROM voice_notes WHERE project_id=? AND id=?`,
		pid, id).Scan(
		&n.ID, &n.ProjectID, &n.Title, &n.Status, &n.StorageFileID, &n.StorageURL,
		&n.FileName, &n.ContentType, &n.SizeBytes, &n.DurationMS, &n.TranscriptStatus,
		&n.TranscriptText, &n.TranscriptLanguage, &n.TranscriptProvider,
		&n.TranscriptModel, &segs, &n.ErrorMessage,
		&tags, &n.RecordedAt, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		return nil, err
	}
	n.TranscriptSegmentsJSON = json.RawMessage(defaultJSON(segs, "[]"))
	n.TagsJSON = json.RawMessage(defaultJSON(tags, "[]"))
	if withURL {
		attachPlaybackURL(ctx, n)
	}
	return n, nil
}

func attachPlaybackURLs(ctx *sdk.AppCtx, notes []*VoiceNote) {
	for _, n := range notes {
		attachPlaybackURL(ctx, n)
	}
}

func attachPlaybackURL(ctx *sdk.AppCtx, n *VoiceNote) {
	if ctx == nil || n == nil || n.StorageFileID == "" || n.PlaybackURL != "" {
		return
	}
	fid, err := strconv.ParseInt(n.StorageFileID, 10, 64)
	if err != nil {
		return
	}
	if got, err := signedStorageURL(ctx, fid); err == nil {
		n.PlaybackURL = got.URL
	}
}

func listNotes(db *sql.DB, pid, q, status string, limit int) ([]*VoiceNote, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := []string{"project_id=?"}
	args := []any{pid}
	if status != "" {
		where = append(where, "status=?")
		args = append(args, status)
	}
	if q != "" {
		where = append(where, "(title LIKE ? OR transcript_text LIKE ? OR file_name LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like, like)
	}
	args = append(args, limit)
	rows, err := db.Query(`
		SELECT id, project_id, title, status, storage_file_id, storage_url,
		       file_name, content_type, size_bytes, duration_ms, transcript_status,
		       transcript_text, transcript_language, transcript_provider,
		       transcript_model, transcript_segments_json, error_message,
		       tags_json, COALESCE(recorded_at,''), created_at, updated_at
		  FROM voice_notes WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY created_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*VoiceNote{}
	for rows.Next() {
		n := &VoiceNote{}
		var segs, tags string
		if err := rows.Scan(
			&n.ID, &n.ProjectID, &n.Title, &n.Status, &n.StorageFileID, &n.StorageURL,
			&n.FileName, &n.ContentType, &n.SizeBytes, &n.DurationMS, &n.TranscriptStatus,
			&n.TranscriptText, &n.TranscriptLanguage, &n.TranscriptProvider,
			&n.TranscriptModel, &segs, &n.ErrorMessage,
			&tags, &n.RecordedAt, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		n.TranscriptSegmentsJSON = json.RawMessage(defaultJSON(segs, "[]"))
		n.TagsJSON = json.RawMessage(defaultJSON(tags, "[]"))
		out = append(out, n)
	}
	return out, nil
}

func updateTranscript(db *sql.DB, pid string, id int64, tr parsedTranscript, provider, model string) error {
	segs, _ := json.Marshal(tr.Segments)
	title := deriveTitle(tr.Text)
	args := []any{"ready", "ok", tr.Text, tr.Language, provider, model, string(segs), pid, id}
	q := `UPDATE voice_notes
	         SET status=?, transcript_status=?, transcript_text=?, transcript_language=?,
	             transcript_provider=?, transcript_model=?, transcript_segments_json=?,
	             error_message='', updated_at=CURRENT_TIMESTAMP`
	if title != "" {
		q += `, title = CASE WHEN title='' OR title='Voice note' OR title='Untitled voice note' THEN ? ELSE title END`
		args = []any{"ready", "ok", tr.Text, tr.Language, provider, model, string(segs), title, pid, id}
	}
	q += ` WHERE project_id=? AND id=?`
	res, err := db.Exec(q, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func markTranscriptFailed(db *sql.DB, pid string, id int64, msg string) error {
	_, err := db.Exec(`
		UPDATE voice_notes
		   SET status=CASE WHEN storage_file_id='' THEN status ELSE 'recorded' END,
		       transcript_status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP
		 WHERE project_id=? AND id=?`,
		msg, pid, id)
	return err
}

func updateNoteFields(db *sql.DB, pid string, id int64, patch map[string]any) error {
	if patch == nil {
		return nil
	}
	sets := []string{}
	args := []any{}
	if v, ok := patch["title"].(string); ok {
		sets = append(sets, "title=?")
		args = append(args, strings.TrimSpace(v))
	}
	if v, ok := patch["tags"]; ok {
		sets = append(sets, "tags_json=?")
		args = append(args, tagsJSON(v))
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
	args = append(args, pid, id)
	res, err := db.Exec(`UPDATE voice_notes SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND id=?`, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func uploadToStorage(ctx *sdk.AppCtx, name, folder, contentType string, body []byte) (*storageUploadResult, error) {
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("no platform client")
	}
	args := map[string]any{
		"name":           name,
		"folder":         folder,
		"content_base64": base64.StdEncoding.EncodeToString(body),
		"content_type":   contentType,
		"source":         "voice-notes",
		"tags":           []string{"voice-note"},
	}
	var out storageUploadResult
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_upload", args, &out); err != nil {
		return nil, fmt.Errorf("storage.files_upload: %w", err)
	}
	if out.ID == 0 {
		return nil, errors.New("storage returned id=0")
	}
	if out.Name == "" {
		out.Name = name
	}
	if out.SizeBytes == 0 {
		out.SizeBytes = int64(len(body))
	}
	return &out, nil
}

func signedStorageURL(ctx *sdk.AppCtx, fileID int64) (*storageURLResult, error) {
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("no platform client")
	}
	var out storageURLResult
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_url", map[string]any{
		"id":          fileID,
		"ttl_seconds": 3600,
	}, &out); err != nil {
		return nil, err
	}
	if out.URL == "" {
		return nil, errors.New("storage returned empty url")
	}
	return &out, nil
}

func parseDeepgramResponse(data json.RawMessage) (*parsedTranscript, error) {
	var root struct {
		Results struct {
			Channels []struct {
				Alternatives []struct {
					Transcript string `json:"transcript"`
					Language   string `json:"detected_language"`
					Paragraphs struct {
						Paragraphs []struct {
							Speaker   *int `json:"speaker"`
							Sentences []struct {
								Text  string  `json:"text"`
								Start float64 `json:"start"`
								End   float64 `json:"end"`
							} `json:"sentences"`
						} `json:"paragraphs"`
					} `json:"paragraphs"`
				} `json:"alternatives"`
			} `json:"channels"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if len(root.Results.Channels) == 0 || len(root.Results.Channels[0].Alternatives) == 0 {
		return nil, errors.New("deepgram response has no channels/alternatives")
	}
	alt := root.Results.Channels[0].Alternatives[0]
	text := strings.TrimSpace(alt.Transcript)
	if text == "" {
		return nil, errors.New("deepgram response transcript is empty")
	}
	out := &parsedTranscript{Text: text, Language: alt.Language}
	for _, p := range alt.Paragraphs.Paragraphs {
		speaker := ""
		if p.Speaker != nil {
			speaker = fmt.Sprintf("speaker_%d", *p.Speaker)
		}
		for _, s := range p.Sentences {
			t := strings.TrimSpace(s.Text)
			if t == "" {
				continue
			}
			out.Segments = append(out.Segments, transcriptSegment{
				StartMS: int64(s.Start * 1000),
				EndMS:   int64(s.End * 1000),
				Text:    t,
				Speaker: speaker,
			})
		}
	}
	return out, nil
}

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v, ok := args["_project_id"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v), nil
	}
	return "", errors.New("project_id missing - pass _project_id when scope=global")
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v := strings.TrimSpace(r.URL.Query().Get("project_id")); v != "" {
		return v, nil
	}
	return "", errors.New("project_id missing")
}

func emit(ctx *sdk.AppCtx, pid, topic string, n *VoiceNote) {
	if ctx == nil || n == nil {
		return
	}
	data := map[string]any{
		"id":              n.ID,
		"title":           n.Title,
		"status":          n.Status,
		"storage_file_id": n.StorageFileID,
	}
	if topic == "voice_note.transcribed" {
		data["language"] = n.TranscriptLanguage
		data["chars"] = len(n.TranscriptText)
	}
	ctx.EmitWithProject(topic, pid, data)
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func sString() map[string]any  { return map[string]any{"type": "string"} }
func sInteger() map[string]any { return map[string]any{"type": "integer"} }
func sBool() map[string]any    { return map[string]any{"type": "boolean"} }
func sArray(item string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": item}}
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func int64Arg(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

func intArg(args map[string]any, key string, def int) int {
	n := int(int64Arg(args, key))
	if n == 0 {
		return def
	}
	return n
}

func boolArg(args map[string]any, key string) bool {
	v, _ := args[key].(bool)
	return v
}

func boolArgDefault(args map[string]any, key string, def bool) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return def
}

func intQuery(r *http.Request, key string, def int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || n == 0 {
		return def
	}
	return n
}

func configBool(v string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return def
}

func tagsJSON(v any) string {
	var tags []string
	switch t := v.(type) {
	case []string:
		tags = t
	case []any:
		for _, item := range t {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				tags = append(tags, s)
			}
		}
	default:
		return "[]"
	}
	b, _ := json.Marshal(tags)
	return string(b)
}

func defaultJSON(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func defaultFolder(ctx *sdk.AppCtx) string {
	f := strings.TrimSpace(ctx.Config().Get("default_folder"))
	if f == "" {
		f = "/voice-notes/"
	}
	if !strings.HasPrefix(f, "/") {
		f = "/" + f
	}
	if !strings.HasSuffix(f, "/") {
		f += "/"
	}
	return f
}

func deriveTitle(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return ""
	}
	if len(text) > 72 {
		return strings.TrimSpace(text[:72]) + "..."
	}
	return text
}

func defaultAudioName() string {
	return "voice-note-" + time.Now().UTC().Format("20060102-150405") + ".webm"
}

func contentTypeFromName(name string) string {
	switch strings.ToLower(extSuffix(name)) {
	case ".webm":
		return "audio/webm"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".m4a":
		return "audio/mp4"
	case ".ogg":
		return "audio/ogg"
	}
	return ""
}

func extSuffix(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return ""
	}
	return name[i:]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
