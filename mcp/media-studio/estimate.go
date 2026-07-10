package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type generationEstimate struct {
	Kind                     string  `json:"kind"`
	Provider                 string  `json:"provider,omitempty"`
	Model                    string  `json:"model,omitempty"`
	Capability               string  `json:"capability,omitempty"`
	CostUSD                  float64 `json:"cost_usd"`
	Available                bool    `json:"available"`
	Source                   string  `json:"source"`
	EstimatedDurationSeconds float64 `json:"estimated_duration_seconds,omitempty"`
}

func (a *App) toolMediaEstimate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = withProjectScope(ctx, args)
	estimate, err := a.estimateGeneration(ctx, args)
	if err != nil {
		return mcpError("estimate: " + err.Error()), nil
	}
	text := "Cost estimate unavailable."
	if estimate.Available {
		text = "Estimated cost: " + formatUSDEstimate(estimate.CostUSD)
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"_meta":   estimate,
	}, nil
}

func (a *App) handleEstimate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" && projectArg(body) == "" {
		body["project_id"] = pid
	}
	pid := projectScopeFromArgs(globalCtx, body)
	if pid == "" {
		http.Error(w, "project_id required", http.StatusBadRequest)
		return
	}
	body["_project_id"] = pid
	estimate, err := a.estimateGeneration(globalCtx.WithProject(pid), body)
	writeJSON(w, estimate, err)
}

func (a *App) estimateGeneration(ctx *sdk.AppCtx, args map[string]any) (generationEstimate, error) {
	kind := strArg(args, "kind", "")
	if kind == "" {
		return generationEstimate{}, errors.New("kind required")
	}
	h, ok := handlers[kind]
	if !ok {
		return generationEstimate{}, errors.New("unknown kind: " + kind)
	}
	capability := h.ResolveCapability(args)
	out := generationEstimate{
		Kind:                     kind,
		Capability:               capability,
		Model:                    strArg(args, "model", ""),
		EstimatedDurationSeconds: estimatedDurationSeconds(kind, args),
		Source:                   "unknown",
	}
	bound, err := selectBoundProvider(ctx, h, args, capability)
	if err != nil {
		return out, err
	}
	if bound == nil {
		return out, nil
	}
	out.Model = strArg(args, "model", "")
	out.Provider = bound.AppSlug

	switch kind {
	case KindVideo:
		if cost := veniceVideoQuote(ctx, bound.AppSlug, args); cost > 0 {
			out.CostUSD = cost
			out.Available = true
			out.Source = "provider_quote"
			return out, nil
		}
	case KindAvatar:
		if cost := heygenAvatarQuote(bound.AppSlug, args); cost > 0 {
			out.CostUSD = cost
			out.Available = true
			out.Source = "provider_formula"
			return out, nil
		}
	default:
		if cost := computeGenerationCost(ctx, bound, kind, capability, out.Model, args); cost > 0 {
			out.CostUSD = cost
			out.Available = true
			out.Source = "provider_pricing"
			return out, nil
		}
	}

	if bound.AppSlug == "venice-ai" {
		if price := modelListPrice(ctx, kind, capability, out.Model); price > 0 {
			out.CostUSD = price
			out.Available = true
			out.Source = "model_price"
		}
	}
	return out, nil
}

func modelListPrice(ctx *sdk.AppCtx, kind, capability, model string) float64 {
	if strings.TrimSpace(model) == "" {
		return 0
	}
	models, err := loadModelsForCapability(ctx, kind, capability)
	if err != nil {
		return 0
	}
	for _, m := range models {
		if m.ID == model && m.PriceUSD > 0 {
			return m.PriceUSD
		}
	}
	return 0
}

func estimateCostFromRaw(raw json.RawMessage) float64 {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0
	}
	return estimateCostFromValue(v)
}

func estimateCostFromValue(v any) float64 {
	switch x := v.(type) {
	case map[string]any:
		for _, key := range []string{"quote", "cost_usd", "estimated_cost_usd", "estimated_cost", "price_usd", "cost", "amount"} {
			if cost := numericOrNestedCost(x[key]); cost > 0 {
				return cost
			}
		}
		for _, key := range []string{"data", "result", "estimate", "pricing"} {
			if cost := estimateCostFromValue(x[key]); cost > 0 {
				return cost
			}
		}
	case []any:
		for _, item := range x {
			if cost := estimateCostFromValue(item); cost > 0 {
				return cost
			}
		}
	}
	return 0
}

func numericOrNestedCost(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case map[string]any:
		for _, key := range []string{"usd", "value", "amount", "cost_usd"} {
			if cost := numericOrNestedCost(x[key]); cost > 0 {
				return cost
			}
		}
		return estimateCostFromValue(x)
	}
	return 0
}

func formatUSDEstimate(n float64) string {
	if n >= 0.01 {
		return "$" + trimTrailingZeros(n, 2)
	}
	if n >= 0.001 {
		return "$" + trimTrailingZeros(n, 4)
	}
	return "$" + trimTrailingZeros(n, 6)
}

func trimTrailingZeros(n float64, decimals int) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.*f", decimals, n), "0"), ".")
}
