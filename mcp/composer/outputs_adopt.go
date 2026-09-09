package main

import (
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Adoption copies artifact references; it never moves/deletes the original
// render, composition, or Storage file. Historical input revisions stay unknown.
func (a *App) toolOutputAdopt(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	row, err := outputComposition(ctx, args)
	if err != nil {
		return nil, err
	}
	if err = ensureOutputs(ctx, row); err != nil {
		return nil, err
	}
	id := row["id"].(int64)
	pid := row["project_id"].(string)
	o, err := loadOutput(ctx, id, strArg(args, "kind", ""))
	if err != nil {
		return nil, err
	}
	expected := int64Arg(args, "expected_revision", 0)
	if expected != o.Revision {
		return nil, errors.New("output revision conflict")
	}
	key := strArg(args, "idempotency_key", "")
	if key == "" || len(key) > 200 {
		return nil, errors.New("idempotency_key required (max 200 characters)")
	}
	sid := int64Arg(args, "storage_id", 0)
	duration := int64Arg(args, "duration_ms", 0)
	legacy := int64Arg(args, "render_id", 0)
	snapshot := "{}"
	if legacy > 0 {
		if err = ctx.AppDB().QueryRow(`SELECT storage_id,duration_ms,edit_snapshot FROM renders WHERE id=? AND composition_id=? AND project_id=? AND status='complete'`, legacy, id, pid).Scan(&sid, &duration, &snapshot); err != nil {
			return nil, errors.New("completed source render not found in this composition")
		}
	}
	if sid <= 0 {
		return nil, errors.New("Storage artifact required")
	}
	mime, err := outputStorageMIME(ctx.WithProject(pid), sid)
	if err != nil {
		return nil, err
	}
	format := "mp4"
	if o.Kind == "song" {
		switch mime {
		case "audio/mpeg", "audio/mp3":
			format = "mp3"
		case "audio/wav", "audio/x-wav", "audio/wave":
			format = "wav"
		case "audio/mp4", "audio/x-m4a":
			format = "m4a"
		case "audio/aac":
			format = "aac"
		default:
			return nil, errors.New("selected artifact is not a supported audio file")
		}
	} else if mime != "video/mp4" {
		return nil, errors.New("video outputs require an MP4 artifact")
	}
	settings := o.Settings
	settings.Format = format
	if _, err = ctx.AppDB().Exec(`INSERT INTO renders(composition_id,project_id,executor,status,storage_id,duration_ms,edit_snapshot,output_id,output_revision,input_revision,output_snapshot,idempotency_key)
 SELECT ?,?,'adopted','complete',?,?,?,?,?,'adopted',?,? FROM composition_outputs WHERE id=? AND revision=? ON CONFLICT(output_id,idempotency_key) WHERE output_id IS NOT NULL DO NOTHING`, id, pid, sid, duration, snapshot, o.ID, expected, outputJSON(outputSnapshot{Settings: settings}), key, o.ID, expected); err != nil {
		return nil, err
	}
	var rid int64
	if err = ctx.AppDB().QueryRow(`SELECT id FROM renders WHERE output_id=? AND idempotency_key=?`, o.ID, key).Scan(&rid); err != nil {
		return nil, errors.New("output revision conflict")
	}
	return readOutputRender(ctx, rid)
}
func outputStorageMIME(ctx *sdk.AppCtx, id int64) (string, error) {
	var result struct {
		Found bool `json:"found"`
		File  *struct {
			ContentType string `json:"content_type"`
		} `json:"file"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get", map[string]any{"id": id, "_project_id": ctx.CurrentProject()}, &result); err != nil {
		return "", err
	}
	if !result.Found || result.File == nil {
		return "", fmt.Errorf("Storage file %d is not accessible in this project", id)
	}
	return strings.ToLower(strings.TrimSpace(strings.Split(result.File.ContentType, ";")[0])), nil
}
