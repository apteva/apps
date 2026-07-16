package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) handleCompositionCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	id, ok := cardPathID(r.URL.Path, "/cards/composition/")
	if !ok || globalCtx == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	pid := projectScopeFromArgs(globalCtx, map[string]any{"project_id": r.URL.Query().Get("project_id")})
	out, err := compositionCardData(globalCtx, id, pid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonResp(w, out)
}

func (a *App) handleRenderCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		http.Error(w, "GET or DELETE only", http.StatusMethodNotAllowed)
		return
	}
	id, ok := cardPathID(r.URL.Path, "/cards/render/")
	if !ok || globalCtx == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	pid := projectScopeFromArgs(globalCtx, map[string]any{"project_id": r.URL.Query().Get("project_id")})
	if r.Method == http.MethodDelete {
		if !renderBelongsToProject(globalCtx, id, pid) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		out, err := cancelQueuedRender(globalCtx, id, pid)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		jsonResp(w, out)
		return
	}
	out, err := renderCardData(globalCtx, id, pid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonResp(w, out)
}

func cardPathID(path, prefix string) (int64, bool) {
	raw := strings.SplitN(strings.TrimPrefix(path, prefix), "/", 2)[0]
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0
}

func compositionCardData(ctx *sdk.AppCtx, id int64, projectID string) (map[string]any, error) {
	var name, editJSON, outputJSON, updatedAt string
	var duration float64
	if err := ctx.AppDB().QueryRow(
		`SELECT name, edit_json, output_json, duration_seconds, updated_at
		 FROM compositions WHERE id=? AND project_id=?`, id, projectID,
	).Scan(&name, &editJSON, &outputJSON, &duration, &updatedAt); err != nil {
		return nil, fmt.Errorf("composition not found")
	}
	edit, output, _, _, err := renderEditFromStoredJSON(editJSON, outputJSON)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{"visual": 0, "audio": 0, "text": 0, "silence": 0, "ai": 0}
	aiCounts := map[string]int{"total": 0, "ready": 0, "generating": 0, "draft": 0, "failed": 0}
	lanes := make([]map[string]any, 0, len(edit.Timeline.Tracks))
	for ti, track := range edit.Timeline.Tracks {
		kind := trackKind(track)
		clips := make([]map[string]any, 0, len(track.Clips))
		for ci, clip := range track.Clips {
			assetKind := clipAssetType(clip, kind)
			countKey := kind
			if assetKind == "silence" {
				countKey = "silence"
			} else if assetKind == "text" || kind == "overlay" {
				countKey = "text"
			} else if kind == "visual" {
				countKey = "visual"
			} else {
				countKey = "audio"
			}
			counts[countKey]++
			status := ""
			if clip.AI != nil {
				counts["ai"]++
				aiCounts["total"]++
				status = strings.ToLower(strings.TrimSpace(clip.AI.Status))
				if status == "" {
					status = "draft"
				}
				if _, exists := aiCounts[status]; exists {
					aiCounts[status]++
				} else {
					aiCounts["draft"]++
				}
			}
			uid := clip.UID
			if uid == "" {
				uid = fmt.Sprintf("clip-%d-%d", ti+1, ci+1)
			}
			clips = append(clips, map[string]any{
				"uid": uid, "start": clip.Start, "length": clipDuration(clip),
				"kind": assetKind, "ai_status": status,
			})
		}
		lanes = append(lanes, map[string]any{"type": kind, "clips": clips})
	}
	if edit.Timeline.Soundtrack != nil {
		counts["audio"]++
		if edit.Timeline.Soundtrack.AI != nil {
			counts["ai"]++
			aiCounts["total"]++
			status := strings.ToLower(strings.TrimSpace(edit.Timeline.Soundtrack.AI.Status))
			if status == "" {
				status = "draft"
			}
			if _, exists := aiCounts[status]; exists {
				aiCounts[status]++
			} else {
				aiCounts["draft"]++
			}
		}
	}
	return map[string]any{
		"id": id, "name": name, "duration_seconds": duration, "updated_at": updatedAt,
		"output": output, "track_count": len(edit.Timeline.Tracks), "counts": counts,
		"ai": aiCounts, "lanes": lanes, "latest_render": loadLatestRender(ctx, id),
	}, nil
}

func renderCardData(ctx *sdk.AppCtx, id int64, projectID string) (map[string]any, error) {
	var compositionID, storageID, durationMS, attempts int64
	var name, executor, status, phase, progressJSON, outputJSON, errMsg, qaJSON, createdAt, updatedAt string
	var progressPct, costUSD float64
	err := ctx.AppDB().QueryRow(
		`SELECT r.composition_id, c.name, r.executor, r.status, COALESCE(r.phase,''),
		        COALESCE(r.progress_pct,0), COALESCE(r.progress_json,'{}'), r.storage_id,
		        r.duration_ms, r.cost_usd, r.error, r.attempts, r.qa_json,
		        COALESCE(NULLIF(r.output_snapshot,'{}'), c.output_json), r.created_at, r.updated_at
		 FROM renders r JOIN compositions c ON c.id=r.composition_id
		 WHERE r.id=? AND r.project_id=? AND c.project_id=?`, id, projectID, projectID,
	).Scan(&compositionID, &name, &executor, &status, &phase, &progressPct, &progressJSON,
		&storageID, &durationMS, &costUSD, &errMsg, &attempts, &qaJSON, &outputJSON, &createdAt, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("render not found")
	}
	var output Output
	_ = json.Unmarshal([]byte(outputJSON), &output)
	validateOutput(&output)
	out := map[string]any{
		"render_id": id, "composition_id": compositionID, "composition_name": name,
		"executor": executor, "status": status, "phase": phase, "progress_pct": progressPct,
		"progress": decodeJSONMap(progressJSON), "storage_id": storageID,
		"duration_ms": durationMS, "cost_usd": costUSD, "error": errMsg,
		"attempts": attempts, "qa": decodeRenderQA(qaJSON), "output": output,
		"created_at": createdAt, "updated_at": updatedAt,
	}
	if storageID > 0 {
		out["output_url"] = "/api/apps/storage/files/" + strconv.FormatInt(storageID, 10) + "/content?project_id=" + url.QueryEscape(projectID)
	} else if url := localCacheURL(id); url != "" {
		out["output_url"] = url
	}
	return out, nil
}

func renderBelongsToProject(ctx *sdk.AppCtx, renderID int64, projectID string) bool {
	var one int
	return ctx.AppDB().QueryRow(`SELECT 1 FROM renders WHERE id=? AND project_id=?`, renderID, projectID).Scan(&one) == nil
}
