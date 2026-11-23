package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Customer keys are tokens that carry their own admission hook, which is
// how billing apps (SaaS today) issue keys to their customers. They only
// see public model names: provider models are neither listed nor callable,
// and usage reads leave out the provider, its ids, and its cost. Tokens
// without their own hook (admin and internal keys) are unaffected, even
// when a policy sets a hook for them.

func isCustomerKey(ident *TokenIdentity) bool {
	return ident != nil && ident.Admission.enabled()
}

func modelNotFoundError(model string) error {
	return typedErr{status: http.StatusNotFound, typ: "invalid_request_error", code: "model_not_found",
		msg: fmt.Sprintf("The model %q does not exist or you do not have access to it.", model)}
}

// checkCustomerModel refuses provider models and unknown names for
// customer keys before any routing happens.
func checkCustomerModel(db *sql.DB, ident *TokenIdentity, model string) error {
	if !isCustomerKey(ident) {
		return nil
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil // resolveAttempts reports the missing model
	}
	if providerFromModel(model) != "" {
		return modelNotFoundError(model)
	}
	pm, err := dbPublicModelGet(db, ident.ProjectID, model, true)
	if err != nil {
		return err
	}
	if pm == nil {
		return modelNotFoundError(model)
	}
	return nil
}

// Usage details a customer may see. Provider attempts, raw provider usage,
// and error text stay internal.
var customerUsageDetailKeys = []string{
	"input_tokens", "output_tokens", "cached_input_tokens", "cache_write_tokens",
	"units", "usage_source", "partial", "client_disconnected",
}

// customerUsagePayload is a usage event as a customer key sees it: the
// model it asked for and what it was charged, nothing about the provider.
func customerUsagePayload(ev *UsageEvent) map[string]any {
	details := map[string]any{}
	_ = json.Unmarshal(ev.UsageDetails, &details)
	visible := map[string]any{}
	for _, k := range customerUsageDetailKeys {
		if v, ok := details[k]; ok {
			visible[k] = v
		}
	}
	var cost any
	if ev.ChargeStatus == "settled" && ev.ChargedMicrounits != nil {
		cost = *ev.ChargedMicrounits
	}
	return map[string]any{
		"schema_version":         "2026-11-llm-usage-customer-v1",
		"usage_event_id":         ev.ID,
		"request_id":             ev.RequestID,
		"token_id":               ev.TokenID,
		"subject_type":           ev.SubjectType,
		"subject_id":             ev.SubjectID,
		"model":                  firstNonEmpty(ev.RequestedModel, ev.Model),
		"endpoint":               ev.Endpoint,
		"period":                 ev.Period,
		"status":                 ev.Status,
		"request_tokens":         ev.RequestTokens,
		"response_tokens":        ev.ResponseTokens,
		"total_tokens":           ev.TotalTokens,
		"usage_details":          visible,
		"cost_microunits":        cost,
		"cost_status":            customerCostStatus(ev.ChargeStatus),
		"latency_ms":             ev.LatencyMS,
		"time_to_first_token_ms": ev.TTFTMS,
		"admission_ref":          ev.AdmissionRef,
		"created_at":             ev.CreatedAt,
	}
}

func customerCostStatus(chargeStatus string) string {
	switch chargeStatus {
	case "settled":
		return "settled"
	case "pending":
		return "pending"
	default:
		return "none"
	}
}

// CustomerUsageSummary is /v1/usage for a customer key. cost_microunits
// only adds up settled requests; pending_cost_requests are still waiting
// for their settlement.
type CustomerUsageSummary struct {
	ProjectID           string `json:"project_id"`
	Period              string `json:"period"`
	SubjectType         string `json:"subject_type,omitempty"`
	SubjectID           string `json:"subject_id,omitempty"`
	Model               string `json:"model,omitempty"`
	Requests            int64  `json:"requests"`
	RequestTokens       int64  `json:"request_tokens"`
	ResponseTokens      int64  `json:"response_tokens"`
	TotalTokens         int64  `json:"total_tokens"`
	CostMicrounits      int64  `json:"cost_microunits"`
	SettledRequests     int64  `json:"settled_requests"`
	PendingCostRequests int64  `json:"pending_cost_requests"`
}

func dbCustomerUsageGet(db *sql.DB, f usageFilter) (*CustomerUsageSummary, error) {
	if f.Period == "" {
		f.Period = currentPeriod()
	}
	where, args := usageWhere(f, "status NOT IN ('reserved','denied')")
	out := CustomerUsageSummary{ProjectID: f.ProjectID, Period: f.Period, SubjectType: f.SubjectType, SubjectID: f.SubjectID, Model: f.Model}
	err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(request_tokens),0), COALESCE(SUM(response_tokens),0), COALESCE(SUM(total_tokens),0),
		COALESCE(SUM(CASE WHEN charge_status='settled' THEN COALESCE(charged_microunits,0) ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN charge_status='settled' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN charge_status='pending' THEN 1 ELSE 0 END),0)
		FROM usage_events WHERE `+where, args...).Scan(&out.Requests, &out.RequestTokens, &out.ResponseTokens, &out.TotalTokens,
		&out.CostMicrounits, &out.SettledRequests, &out.PendingCostRequests)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// settledCharge reads what the billing app charged from its settle reply:
// `committed` (SaaS: the amount actually taken from credit), else
// `charged_microunits`. A reply that skipped or released the hold charged
// nothing. ok is false when the reply says nothing about the charge.
func settledCharge(reply any) (int64, bool) {
	m, _ := reply.(map[string]any)
	if m == nil {
		return 0, false
	}
	for _, key := range []string{"committed", "charged_microunits"} {
		if v, exists := m[key]; exists {
			if n, ok := jsonInt(v); ok {
				return n, true
			}
		}
	}
	if b, _ := m["released"].(bool); b {
		return 0, true
	}
	if s, _ := m["skipped"].(string); s != "" {
		return 0, true
	}
	return 0, false
}

func jsonInt(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
}

// usageWhere builds the WHERE clause shared by usage queries.
func usageWhere(f usageFilter, statusCond string) (string, []any) {
	if f.Period == "" {
		f.Period = currentPeriod()
	}
	conds := []string{"project_id = ?", "period = ?", statusCond}
	args := []any{f.ProjectID, f.Period}
	add := func(cond string, v any) {
		conds = append(conds, cond)
		args = append(args, v)
	}
	if f.SubjectType != "" {
		add("subject_type = ?", f.SubjectType)
	}
	if f.SubjectID != "" {
		add("subject_id = ?", f.SubjectID)
	}
	if f.Model != "" {
		if f.PublicView {
			add("requested_model = ?", f.Model)
		} else {
			add("model = ?", f.Model)
		}
	}
	if f.TokenID > 0 {
		add("token_id = ?", f.TokenID)
	}
	if f.Endpoint != "" {
		add("endpoint = ?", f.Endpoint)
	}
	return strings.Join(conds, " AND "), args
}
