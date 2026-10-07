package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type renderBudget struct {
	EffectiveTimeoutSeconds int     `json:"effective_timeout_seconds"`
	EstimatedSeconds        int     `json:"estimated_seconds,omitempty"`
	EstimatedEncodeSeconds  int     `json:"estimated_encode_seconds,omitempty"`
	EstimateBasis           string  `json:"estimate_basis"`
	EstimatedSpeed          float64 `json:"estimated_encode_speed,omitempty"`
	Warning                 string  `json:"warning,omitempty"`
}

func computeRenderBudget(raw json.RawMessage, durationMs int64, hevc bool, defaultSeconds, maxSeconds int, hevcSpeed float64) (renderBudget, error) {
	var p struct {
		Timeout int    `json:"timeout_seconds"`
		Mode    string `json:"trim_mode"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return renderBudget{}, err
	}
	if maxSeconds < 30 {
		maxSeconds = 14400
	}
	if defaultSeconds > maxSeconds {
		defaultSeconds = maxSeconds
	}
	b := renderBudget{EffectiveTimeoutSeconds: defaultSeconds, EstimateBasis: "operator_default"}
	if hevc && durationMs > 0 {
		if hevcSpeed <= 0 {
			hevcSpeed = .2
		}
		b.EstimatedSpeed = hevcSpeed
		b.EstimatedEncodeSeconds = int(math.Ceil(float64(durationMs) / 1000 / hevcSpeed))
		// Full decode/validation plus transfer reserve. Host-configurable speed is
		// conservative; it is an estimate, not a performance guarantee.
		b.EstimatedSeconds = b.EstimatedEncodeSeconds + int(math.Ceil(float64(durationMs)/1000/.8)) + 180
		b.EstimateBasis = "configured_hevc_speed_plus_validation_and_transfer_reserve"
		desired := int(math.Ceil(float64(b.EstimatedSeconds) * 1.15))
		if desired > b.EffectiveTimeoutSeconds {
			b.EffectiveTimeoutSeconds = desired
		}
		if b.EffectiveTimeoutSeconds > maxSeconds {
			b.EffectiveTimeoutSeconds = maxSeconds
		}
	}
	if p.Timeout != 0 {
		if p.Timeout < 30 || p.Timeout > maxSeconds {
			return b, fmt.Errorf("render_budget_exceeded: timeout_seconds must be 30..%d", maxSeconds)
		}
		b.EffectiveTimeoutSeconds = p.Timeout
	}
	if b.EstimatedSeconds > b.EffectiveTimeoutSeconds {
		b.Warning = "Estimated accurate HEVC encoding plus validation/transfer exceeds the timeout. Auto may copy; an encoding fallback will be rejected."
		if p.Mode != "auto" {
			return b, fmt.Errorf("render_budget_exceeded: estimated %ds exceeds effective timeout %ds", b.EstimatedSeconds, b.EffectiveTimeoutSeconds)
		}
	} else if b.EffectiveTimeoutSeconds > defaultSeconds {
		b.Warning = "Timeout increased for the estimated HEVC job; host performance and transfer time may vary."
	}
	return b, nil
}

func prepareRenderBudget(app *sdk.AppCtx, row *RenderRow, defaultSeconds int) (int, error) {
	b, err := describeRenderBudget(app, row, defaultSeconds)
	var params map[string]any
	_ = json.Unmarshal(row.Params, &params)
	if params == nil {
		params = map[string]any{}
	}
	params["render_budget"] = b
	row.Params, _ = json.Marshal(params)
	if storeErr := renderUpdateResolvedParams(app.AppDB(), row.ID, row.Params); storeErr != nil {
		return b.EffectiveTimeoutSeconds, storeErr
	}
	return b.EffectiveTimeoutSeconds, err
}

func describeRenderBudget(app *sdk.AppCtx, row *RenderRow, defaultSeconds int) (renderBudget, error) {
	raw := prepareTrimParams(app.AppDB(), row.ProjectID, row.Operation, row.SourceFileIDs, row.Params)
	var p struct {
		Source trimVideoEncoding `json:"_trim_source_video"`
	}
	_ = json.Unmarshal(raw, &p)
	hevc := row.Operation == "trim" && trimUsesHEVC(p.Source)
	maxSeconds := parseConfigIntFallback(app.Config().Get("render_max_timeout_seconds"), 14400)
	speed := .2
	if configured := app.Config().Get("render_hevc_estimated_speed"); configured != "" {
		var v float64
		if json.Unmarshal([]byte(configured), &v) == nil && v > 0 {
			speed = v
		}
	}
	return computeRenderBudget(row.Params, expectedProgressDurationMs(app.AppDB(), row), hevc, defaultSeconds, maxSeconds, speed)
}

func timeoutSchema() map[string]any {
	return map[string]any{"type": "integer", "minimum": 30, "description": "Optional job timeout in seconds, bounded by render_max_timeout_seconds (default 14400). HEVC trims otherwise use an estimated encoding/validation/transfer budget. Effective timeout and estimate appear in render_budget."}
}

func checkRemainingRenderBudget(ctx context.Context, row *RenderRow) error {
	var p struct {
		Budget renderBudget `json:"render_budget"`
	}
	_ = json.Unmarshal(row.Params, &p)
	if deadline, ok := ctx.Deadline(); ok && float64(p.Budget.EstimatedSeconds) > time.Until(deadline).Seconds() {
		return fmt.Errorf("render_budget_exceeded: estimated %ds exceeds remaining job timeout", p.Budget.EstimatedSeconds)
	}
	return nil
}
