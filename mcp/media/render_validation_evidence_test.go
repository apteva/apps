package main

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestLiveRuntimeModeAndPhaseBeforeCompletion(t *testing.T) {
	app := newTestCtx(t)
	id, err := insertRender(app.AppDB(), testProj, "trim", []string{"1"}, map[string]any{"trim_mode": "auto"}, "out.mov", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claimNextPending(app.AppDB()); err != nil {
		t.Fatal(err)
	}
	forwardProgress(io.NopCloser(strings.NewReader("APTEVA_STATUS:{\"stage\":\"video_copy\",\"diagnostics\":{\"trim_diagnostics\":{\"mode\":\"keyframe_copy\",\"reencoded\":false}}}\nout_time_ms=2000000\nAPTEVA_STATUS:{\"stage\":\"validation\",\"diagnostics\":{}}\nout_time_ms=500000\n")), app.AppDB(), id, app, testProj, 2000)
	row, err := getRender(app.AppDB(), testProj, id)
	if err != nil {
		t.Fatal(err)
	}
	var params, metrics map[string]any
	_ = json.Unmarshal(row.ResolvedParams, &params)
	_ = json.Unmarshal(row.Metrics, &metrics)
	if row.Status != "running" || params["trim_diagnostics"].(map[string]any)["mode"] != "keyframe_copy" || metrics["stage"] != "validation" || metrics["stage_progress_pct"] != float64(25) {
		t.Fatalf("row=%+v params=%v metrics=%v", row, params, metrics)
	}
	persistRuntimeStatus(app, id, testProj, `{"stage":"upload"}`)
	row, _ = getRender(app.AppDB(), testProj, id)
	_ = json.Unmarshal(row.Metrics, &metrics)
	if metrics["stage"] != "upload" || metrics["stage_progress_pct"] != nil {
		t.Fatal(metrics)
	}
}
