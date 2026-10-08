package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"image"
	"io"
	"strconv"
)

type askOutputIdentity struct {
	RenderID      int64          `json:"render_id"`
	Operation     string         `json:"operation"`
	SourceFileIDs []string       `json:"source_file_ids"`
	Transform     map[string]any `json:"transform"`
}

func askRenderIdentity(app *sdk.AppCtx, project, fid string) *askOutputIdentity {
	var r askOutputIdentity
	var sources, raw string
	if app.AppDB().QueryRow(`SELECT id,operation,source_file_ids,COALESCE(NULLIF(resolved_params,''),params) FROM renders WHERE project_id=? AND output_file_id=? AND status='ok' ORDER BY id DESC LIMIT 1`, project, fid).Scan(&r.RenderID, &r.Operation, &sources, &raw) != nil {
		return nil
	}
	if json.Unmarshal([]byte(sources), &r.SourceFileIDs) != nil {
		return nil
	}
	var p map[string]any
	json.Unmarshal([]byte(raw), &p)
	r.Transform = map[string]any{}
	for _, key := range []string{"at_ms", "start_ms", "end_ms", "crop_w", "crop_h", "crop_x", "crop_y", "crop_path", "crop_version", "fit_mode", "target_ratio", "output_width", "width", "height"} {
		if v, ok := p[key]; ok {
			r.Transform[key] = v
		}
	}
	return &r
}

type askHeaderWriter struct{ bytes.Buffer }

func (w *askHeaderWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.Len()+n > 256*1024 {
		n = 256*1024 - w.Len()
		if n > 0 {
			w.Buffer.Write(p[:n])
		}
		return n, io.ErrShortWrite
	}
	return w.Buffer.Write(p)
}

func annotateAskEvidence(ctx context.Context, app *sdk.AppCtx, sc *storageClient, project string, row *MediaRow, evidence []askEvidence, limitations []string) ([]askEvidence, []string) {
	for i := range evidence {
		e := &evidence[i]
		e.SourceWidth = row.Width
		e.SourceHeight = row.Height
		if e.Kind == "source_image" {
			e.Width = row.Width
			e.Height = row.Height
			e.DimensionsSource = "media_probe"
			e.Representation = "source_image"
			e.OutputIdentity = askRenderIdentity(app, project, row.FileID)
		} else {
			e.Representation = "thumbnail"
			if e.Kind == "keyframe" {
				e.Representation = "cached_video_frame"
			}
			for _, d := range row.Derivations {
				if d.StorageFileID == e.StorageFileID {
					e.Width = d.Width
					e.Height = d.Height
					e.DimensionsSource = "derivation_record"
					break
				}
			}
			if e.Width <= 0 || e.Height <= 0 {
				var prefix askHeaderWriter
				id, _ := strconv.ParseInt(e.StorageFileID, 10, 64)
				_ = sc.DownloadContent(ctx, project, id, &prefix)
				if cfg, _, err := image.DecodeConfig(bytes.NewReader(prefix.Bytes())); err == nil {
					e.Width = cfg.Width
					e.Height = cfg.Height
					e.DimensionsSource = "image_header"
				}
			}
			limitations = append(limitations, fmt.Sprintf("Evidence %s is a reduced %s (%d×%d supplied; source %d×%d). Native-resolution details were not inspected.", e.StorageFileID, e.Representation, e.Width, e.Height, row.Width, row.Height))
		}
		if e.Width <= 0 || e.Height <= 0 {
			limitations = append(limitations, "Supplied image dimensions could not be fully verified from the existing record or image header.")
		}
	}
	if len(evidence) > 0 {
		limitations = append(limitations, "Reported dimensions describe the supplied artifacts. Provider-side image resizing and internal inspection resolution are not exposed.")
	}
	return evidence, limitations
}
