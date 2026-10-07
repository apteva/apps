package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

//go:embed render_runtime.py
var renderRuntimePython string

const audioNormalizationVersion = "media-two-pass-loudnorm-4"

func needsRenderRuntime(op string, raw json.RawMessage) bool {
	var p struct {
		TrimMode string `json:"trim_mode"`
		Mode     string `json:"mode"`
	}
	_ = json.Unmarshal(raw, &p)
	p.Mode = strings.ToLower(strings.TrimSpace(p.Mode))
	return op == "trim" && p.TrimMode == "auto" || op == "audio_filter" && (p.Mode == "" || p.Mode == "normalize" || p.Mode == "speech_clean")
}

type runtimeRequest struct {
	Operation        string          `json:"operation"`
	Params           json.RawMessage `json:"params"`
	Source           string          `json:"source"`
	Output           string          `json:"output"`
	FFmpeg           string          `json:"ffmpeg"`
	FFprobe          string          `json:"ffprobe"`
	Args             []string        `json:"args"`
	Progress         string          `json:"progress"`
	RemainingSeconds float64         `json:"remaining_seconds"`
}

func makeRuntimeRequest(ctx context.Context, row *RenderRow, binary, source, output string, args []string, progress string) []byte {
	remaining := 14400.0
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline).Seconds()
	}
	req := runtimeRequest{row.Operation, row.Params, source, output, binary, filepath.Join(filepath.Dir(binary), "ffprobe"), args, progress, remaining}
	if filepath.Dir(binary) == "." {
		req.FFprobe = "ffprobe"
	}
	raw, _ := json.Marshal(req)
	return raw
}

func localRuntimeCommand(ctx context.Context, row *RenderRow, dir, binary, source, output string, args []string) (*exec.Cmd, error) {
	python, err := exec.LookPath("python3")
	if err != nil {
		return nil, fmt.Errorf("render_runtime_unavailable: Python 3 is required for auto trim and two-pass normalization")
	}
	if err := os.WriteFile(filepath.Join(dir, "render-runtime.py"), []byte(renderRuntimePython), 0600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime-request.json"), makeRuntimeRequest(ctx, row, binary, source, output, args, "pipe:1"), 0600); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, python, filepath.Join(dir, "render-runtime.py"), filepath.Join(dir, "runtime-request.json"))
	cmd.Dir = dir
	// Kill the helper's FFmpeg children as well as the Python parent on cancel.
	configureRuntimeProcess(cmd)
	return cmd, nil
}

func remoteRuntimeScript(row *RenderRow, binary, source, output string, args []string) string {
	// The remote run-command already enforces the remaining context deadline.
	var p struct {
		Budget struct {
			Effective int `json:"effective_timeout_seconds"`
		} `json:"render_budget"`
	}
	_ = json.Unmarshal(row.Params, &p)
	raw := makeRuntimeRequest(context.Background(), row, binary, source, output, args, "progress.log")
	var req map[string]any
	_ = json.Unmarshal(raw, &req)
	if p.Budget.Effective > 0 {
		req["remaining_seconds"] = p.Budget.Effective
	}
	var params map[string]any
	_ = json.Unmarshal(row.Params, &params)
	if remaining, ok := params["_runtime_remaining_seconds"].(float64); ok {
		req["remaining_seconds"] = remaining
	}
	raw, _ = json.Marshal(req)
	script := "if command -v python3 >/dev/null; then\n" +
		"cat > render-runtime.py <<'__MEDIA_RUNTIME_PY__'\n" + renderRuntimePython + "\n__MEDIA_RUNTIME_PY__\n" +
		"cat > runtime-request.json <<'__MEDIA_RUNTIME_JSON__'\n" + string(raw) + "\n__MEDIA_RUNTIME_JSON__\n" +
		"python3 render-runtime.py runtime-request.json\n"
	if row.Operation == "trim" {
		script += "TRIM_EXPECTED=$(python3 -c 'import json; d=json.load(open(\"runtime-result.json\"))[\"trim_diagnostics\"]; print((d[\"actual_end_ms\"]-d[\"actual_start_ms\"])/1000)')\n"
		var params map[string]any
		_ = json.Unmarshal(row.Params, &params)
		d, _ := params["trim_diagnostics"].(map[string]any)
		if d == nil {
			d = map[string]any{}
		}
		d["fallback_reason"] = "python3_unavailable"
		fallback, _ := json.Marshal(map[string]any{"trim_diagnostics": d})
		estimate := 0.0
		if budget, ok := params["render_budget"].(map[string]any); ok {
			estimate, _ = budget["estimated_seconds"].(float64)
		}
		remaining, _ := req["remaining_seconds"].(float64)
		guard := ""
		if estimate > remaining {
			guard = "echo 'render_budget_exceeded: accurate fallback estimate exceeds remaining timeout'; exit 1\n"
		}
		script += "else\n" + guard + shellCommand(binary, args) + "\nprintf '%s\\n' " + shellQuote("APTEVA_RUNTIME:"+string(fallback)) + "\nTRIM_EXPECTED=" + formatSeconds(expectedProgressDurationMs(nil, row)) + "\nfi\n"
	} else {
		script += "else\necho 'render_runtime_unavailable: Python 3 is required for two-pass normalization'; exit 1\nfi\n"
	}
	return script
}

func persistRuntimeResult(app *sdk.AppCtx, row *RenderRow, raw []byte) error {
	var result, params map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if err := json.Unmarshal(row.Params, &params); err != nil {
		return err
	}
	for k, v := range result {
		params[k] = v
	}
	if d, ok := params["trim_diagnostics"].(map[string]any); ok {
		d["app_version"] = app.Manifest().Version
	}
	if d, ok := params["audio_normalization"].(map[string]any); ok {
		d["app_version"] = app.Manifest().Version
	}
	row.Params, _ = json.Marshal(params)
	return renderUpdateResolvedParams(app.AppDB(), row.ID, row.Params)
}

func runtimeResultFromLog(log string) []byte {
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, "APTEVA_RUNTIME:") {
			return []byte(strings.TrimPrefix(line, "APTEVA_RUNTIME:"))
		}
	}
	return nil
}
