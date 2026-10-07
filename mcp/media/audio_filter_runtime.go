package main

// Runtime-only audio-filter enrichment.
//
// FFmpeg's loudnorm filter operates at 192 kHz in dynamic mode. The planner
// needs the indexed source rate so it can explicitly resample back before the
// encoder, otherwise AAC commonly promotes 48 kHz phone audio to 96 kHz.

import (
	"database/sql"
	"encoding/json"
)

func prepareAudioFilterParams(db *sql.DB, projectID, operation string, sourceFileIDs []string, raw json.RawMessage) json.RawMessage {
	if operation != "audio_filter" {
		return raw
	}
	rate := lookupSourceSampleRate(db, projectID, sourceFileIDs)
	if rate == 0 {
		rate = 48_000
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		return raw
	}
	if params == nil {
		params = map[string]any{}
	}
	params["_source_sample_rate"] = rate
	delete(params, "_validated_video_evidence")
	delete(params, "video_evidence")
	delete(params, "trim_validation_log")
	if db != nil && len(sourceFileIDs) == 1 {
		var resolved string
		if db.QueryRow(`SELECT resolved_params FROM renders WHERE project_id=? AND output_file_id=? AND status='ok' ORDER BY id DESC LIMIT 1`, projectID, sourceFileIDs[0]).Scan(&resolved) == nil {
			var previous map[string]any
			if json.Unmarshal([]byte(resolved), &previous) == nil {
				evidence, ok := previous["video_evidence"].(map[string]any)
				// The executor checks this bound hash against fresh Storage
				// metadata after download; indexing need not have finished.
				if ok && evidence["sha256"] != nil && evidence["sha256"] != "" && evidence["algorithm_version"] == "media-shared-validation-1" && evidence["decode_ok"] == true {
					params["_validated_video_evidence"] = evidence
				}
			}
		}
	}
	out, err := json.Marshal(params)
	if err != nil {
		return raw
	}
	return out
}

func lookupSourceSampleRate(db *sql.DB, projectID string, sourceFileIDs []string) int {
	if db == nil || projectID == "" || len(sourceFileIDs) != 1 {
		return 0
	}
	var rate int
	if err := db.QueryRow(
		`SELECT COALESCE(sample_rate, 0) FROM media WHERE project_id = ? AND file_id = ?`,
		projectID, sourceFileIDs[0],
	).Scan(&rate); err != nil {
		return 0
	}
	return rate
}
