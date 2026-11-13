package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Frozen at queue time. Retrying a candidate never rereads operator defaults.
type transcriptionOptions struct {
	Explicit bool   `json:"-"`
	Model    string `json:"model"`
	Language string `json:"language"`
	Diarize  bool   `json:"diarize"`
}

var transcriptModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var transcriptLanguagePattern = regexp.MustCompile(`^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8})*$`)

func resolveTranscriptionOptions(ctx *sdk.AppCtx, args map[string]any) (transcriptionOptions, error) {
	o := transcriptionOptions{Model: strings.TrimSpace(ctx.Config().Get("transcribe_model")), Language: strings.TrimSpace(ctx.Config().Get("transcribe_language")), Diarize: configBool(ctx.Config().Get("transcribe_diarize"), false)}
	if o.Model == "" {
		o.Model = "nova-3"
	}
	if o.Language == "" {
		o.Language = "auto"
	}
	for _, field := range []struct {
		name  string
		value *string
	}{{"model", &o.Model}, {"language", &o.Language}} {
		if raw, ok := args[field.name]; ok {
			o.Explicit = true
			v, valid := raw.(string)
			if !valid || strings.TrimSpace(v) == "" {
				return o, errors.New(field.name + " must be a nonempty string")
			}
			*field.value = strings.TrimSpace(v)
		}
	}
	if !transcriptModelPattern.MatchString(o.Model) {
		return o, errors.New("invalid transcription model")
	}
	if len(o.Language) > 64 || !(o.Language == "auto" || o.Language == "multi" || transcriptLanguagePattern.MatchString(o.Language)) {
		return o, errors.New("invalid transcription language")
	}
	return o, nil
}

func transcriptOptionsJSON(o *transcriptionOptions) string {
	if o == nil {
		return "{}"
	}
	b, _ := json.Marshal(o)
	return string(b)
}
func decodeTranscriptOptions(raw string) *transcriptionOptions {
	if raw == "" || raw == "{}" {
		return nil
	}
	var o transcriptionOptions
	if json.Unmarshal([]byte(raw), &o) != nil {
		return nil
	}
	return &o
}

// Only pre-upgrade queue rows lack settings. Freeze those once at first claim.
func freezeLegacyTranscriptOptions(db *sql.DB, row *TranscriptRow, defaults transcriptionOptions) error {
	if row.Options != nil {
		return nil
	}
	if _, err := db.Exec(`UPDATE transcripts SET request_options=? WHERE project_id=? AND file_id=? AND status='running' AND started_at=? AND request_options='{}'`, transcriptOptionsJSON(&defaults), row.ProjectID, row.FileID, row.StartedAt); err != nil {
		return err
	}
	var raw string
	if err := db.QueryRow(`SELECT request_options FROM transcripts WHERE project_id=? AND file_id=? AND status='running' AND started_at=?`, row.ProjectID, row.FileID, row.StartedAt).Scan(&raw); err != nil {
		return err
	}
	row.Options = decodeTranscriptOptions(raw)
	if row.Options == nil {
		return errors.New("transcription attempt has no saved options")
	}
	return nil
}

func transcriptionDefaults(ctx *sdk.AppCtx) transcriptionOptions {
	return transcriptionOptions{Model: firstNonEmpty(strings.TrimSpace(ctx.Config().Get("transcribe_model")), "nova-3"), Language: firstNonEmpty(strings.TrimSpace(ctx.Config().Get("transcribe_language")), "auto"), Diarize: configBool(ctx.Config().Get("transcribe_diarize"), false)}
}

func sameTranscriptionOptions(a, b *transcriptionOptions) bool {
	return a != nil && b != nil && a.Model == b.Model && a.Language == b.Language && a.Diarize == b.Diarize
}
