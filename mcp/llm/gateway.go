package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Every metered request goes through the same lifecycle:
//
//	admit:  resolve the model (public name or provider/model), check policy,
//	        charge per-minute limits, reserve monthly usage, then ask the
//	        admission hook (if any) for approval.
//	run:    try provider routes in order, failing over on retryable errors
//	        until a stream has started.
//	finish: close the reservation and, in the same transaction, queue the
//	        settlement for the admission hook.

const endpointChat = "chat.completions"

type gatewayRequest struct {
	Endpoint       string
	Model          string
	Body           map[string]any
	EstimatedInput int64
	EnforceOutput  bool
	CheckModality  bool
	RequestID      string
	Stream         bool
}

type gatewayAdmission struct {
	app            *App
	appCtx         *sdk.AppCtx
	ident          *TokenIdentity
	req            gatewayRequest
	policies       []*Policy
	public         *PublicModel
	attempts       []providerAttempt
	reservation    *UsageEvent
	maxOutput      int64
	hook           *AdmissionConfig
	admissionRef   string
	hookBypassed   bool
	rate           *rateCharge
	RateStatus     *rateStatus
	start          time.Time
	firstTokenAt   time.Time
	streamStarted  bool
	attemptDetails []any
	failedAttempts int
}

type reservationMeta struct {
	Endpoint       string
	RequestedModel string
	TokenMetadata  json.RawMessage
}

func (adm *gatewayAdmission) viaPublic() bool { return adm.public != nil }

func (adm *gatewayAdmission) markFirstToken() {
	if adm.firstTokenAt.IsZero() {
		adm.firstTokenAt = time.Now()
	}
}

// resolveAttempts turns the requested model into ordered provider routes.
func resolveAttempts(db *sql.DB, projectID, model string, policies []*Policy, maxTokens int64) (*PublicModel, []providerAttempt, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, nil, userError("model is required")
	}
	provider := providerFromModel(model)
	if provider == "" {
		pm, err := dbPublicModelGet(db, projectID, model, true)
		if err != nil {
			return nil, nil, err
		}
		if pm == nil {
			return nil, nil, userError(fmt.Sprintf("model %q is not a public model name; provider models need a prefix, for example openai/gpt-4.1", model))
		}
		if err := publicModelAllowed(policies, pm.Name); err != nil {
			return nil, nil, err
		}
		var attempts []providerAttempt
		seen := map[string]bool{}
		var firstErr error
		for _, route := range pm.Routes {
			p := normalizeProvider(firstNonEmpty(route.Provider, providerFromModel(route.Model)))
			m := gatewayModelID(p, route.Model)
			key := p + "\x00" + m
			if p == "" || seen[key] {
				continue
			}
			seen[key] = true
			if err := policiesAllowRoute(policies, p, m, maxTokens, true); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			attempts = append(attempts, providerAttempt{Provider: p, Model: m})
		}
		if len(attempts) == 0 {
			if firstErr == nil {
				firstErr = forbiddenError("public model has no usable routes")
			}
			return nil, nil, firstErr
		}
		for _, fb := range fallbackAttempts(policies, attempts[0].Provider, attempts[0].Model)[1:] {
			key := fb.Provider + "\x00" + fb.Model
			if !seen[key] {
				seen[key] = true
				attempts = append(attempts, fb)
			}
		}
		return pm, attempts, nil
	}
	if err := policiesAllow(policies, provider, model, maxTokens); err != nil {
		return nil, nil, err
	}
	return nil, fallbackAttempts(policies, provider, model), nil
}

func (a *App) admit(reqCtx context.Context, ctx *sdk.AppCtx, ident *TokenIdentity, req gatewayRequest) (*gatewayAdmission, error) {
	db := ctx.AppDB()
	adm := &gatewayAdmission{app: a, appCtx: ctx, ident: ident, req: req, start: time.Now()}
	if adm.req.RequestID == "" {
		adm.req.RequestID = "llm_req_" + randomSuffix(16)
	}
	requestedOutput := int64(0)
	if req.Body != nil {
		requestedOutput = int64Arg(req.Body, "max_tokens")
	}
	policies, err := dbEffectivePolicies(db, ident)
	if err != nil {
		return nil, err
	}
	adm.policies = policies
	if err := checkCustomerModel(db, ident, req.Model); err != nil {
		_ = dbAudit(db, ident, "request.denied", providerFromModel(req.Model), req.Model, "denied", err.Error(), map[string]any{"endpoint": req.Endpoint, "reason": "customer_key_model"})
		return nil, err
	}
	hook := effectiveAdmission(ident, policies)
	adm.req.EstimatedInput = applyInputMargin(req.EstimatedInput, hook)
	public, attempts, err := resolveAttempts(db, ident.ProjectID, req.Model, policies, requestedOutput)
	if err != nil {
		if isPolicyError(err) || isUserError(err) {
			_ = dbAudit(db, ident, "request.denied", providerFromModel(req.Model), req.Model, "denied", err.Error(), map[string]any{"endpoint": req.Endpoint})
		}
		return nil, err
	}
	adm.public, adm.attempts = public, attempts
	primary := attempts[0]
	if req.CheckModality {
		if err := ensureModelSupportsRequest(db, ident.ProjectID, primary.Provider, primary.Model, req.Body); err != nil {
			_ = dbAudit(db, ident, "request.denied", primary.Provider, primary.Model, "denied", err.Error(), map[string]any{"input_modality": "image"})
			return nil, err
		}
	}

	charge, status, err := a.limiter.Charge(rateRulesFor(ident, policies), adm.req.EstimatedInput+requestedOutput)
	if err != nil {
		ctx.EmitWithProject("llm.policy.limit_exceeded", ident.ProjectID, map[string]any{
			"request_id": adm.req.RequestID, "model": req.Model, "subject_type": ident.SubjectType, "subject_id": ident.SubjectID,
			"error": err.Error(), "limit": "rate",
		})
		_ = dbAudit(db, ident, "request.rate_limited", primary.Provider, primary.Model, "denied", err.Error(), map[string]any{"request_id": adm.req.RequestID})
		return nil, err
	}
	adm.rate, adm.RateStatus = charge, status

	meta := reservationMeta{Endpoint: req.Endpoint, RequestedModel: req.Model, TokenMetadata: ident.Metadata}
	reservation, grantedMax, err := reserveUsageMeta(db, ident, primary.Provider, primary.Model, adm.req.EstimatedInput, requestedOutput, adm.req.RequestID, policies, req.EnforceOutput, meta)
	if err != nil {
		adm.rate.Release()
		if isPolicyError(err) {
			ctx.EmitWithProject("llm.policy.limit_exceeded", ident.ProjectID, map[string]any{
				"request_id": adm.req.RequestID, "model": req.Model, "subject_type": ident.SubjectType, "subject_id": ident.SubjectID, "error": err.Error(),
			})
		}
		_ = dbAudit(db, ident, "request.denied", primary.Provider, primary.Model, "denied", err.Error(), map[string]any{"request_id": adm.req.RequestID})
		return nil, err
	}
	adm.reservation = reservation
	adm.maxOutput = requestedOutput
	if adm.maxOutput == 0 && grantedMax > 0 {
		adm.maxOutput = grantedMax
	}

	if hook.enabled() {
		adm.hook = hook
		if err := adm.authorize(reqCtx); err != nil {
			return nil, err
		}
	}
	if req.Body != nil && adm.maxOutput > 0 && req.EnforceOutput {
		req.Body["max_tokens"] = adm.maxOutput
	}
	return adm, nil
}

func (adm *gatewayAdmission) authorize(reqCtx context.Context) error {
	db := adm.appCtx.AppDB()
	ident := adm.ident
	primary := adm.attempts[0]
	fallbacks := []string{}
	for _, at := range adm.attempts[1:] {
		fallbacks = append(fallbacks, at.Model)
	}
	input := map[string]any{
		"request_id":             adm.req.RequestID,
		"token_id":               ident.ID,
		"subject_type":           ident.SubjectType,
		"subject_id":             ident.SubjectID,
		"token_metadata":         rawJSON(string(firstNonEmptyRaw(ident.Metadata, `{}`))),
		"model":                  adm.req.Model,
		"route":                  primary.Model,
		"provider":               primary.Provider,
		"fallback_routes":        fallbacks,
		"endpoint":               adm.req.Endpoint,
		"stream":                 adm.req.Stream,
		"estimated_input_tokens": adm.req.EstimatedInput,
		"max_output_tokens":      adm.maxOutput,
		"project_id":             ident.ProjectID,
		"_project_id":            ident.ProjectID,
	}
	var decision admissionDecision
	err := callHookWithTimeout(reqCtx, adm.appCtx, ident.ProjectID, adm.hook.App, adm.hook.AuthorizeTool, input, &decision, adm.hook.timeout())
	if err == nil && !decision.allowSet {
		err = errors.New("admission hook response is missing allow")
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			adm.deny("client_cancelled", err.Error())
			return err
		}
		if !adm.hook.failOpen() {
			adm.deny("admission_unavailable", err.Error())
			adm.appCtx.EmitWithProject("llm.request.denied", ident.ProjectID, adm.deniedPayload("admission_unavailable", err.Error()))
			return admissionUnavailableError(err)
		}
		adm.hookBypassed = true
		_ = dbAudit(db, ident, "admission.bypassed", primary.Provider, primary.Model, "warning", err.Error(), map[string]any{"request_id": adm.req.RequestID})
	} else if !decision.Allow {
		denyErr := admissionDeniedError(decision)
		_, typ := errorStatus(denyErr)
		adm.deny(typ, denyErr.Error())
		adm.appCtx.EmitWithProject("llm.request.denied", ident.ProjectID, adm.deniedPayload(typ, denyErr.Error()))
		return denyErr
	} else {
		adm.admissionRef = strings.TrimSpace(decision.AdmissionRef)
		if decision.MaxOutputTokens > 0 {
			adm.maxOutput = minPositive(adm.maxOutput, decision.MaxOutputTokens)
		}
	}
	_, err = db.Exec(`UPDATE usage_events SET admission_ref=?, admission_json=? WHERE id=? AND status='reserved'`,
		adm.admissionRef, admissionJSON(adm.hook), adm.reservation.ID)
	return err
}

func (adm *gatewayAdmission) deniedPayload(typ, reason string) map[string]any {
	return map[string]any{
		"request_id": adm.req.RequestID, "model": adm.req.Model, "endpoint": adm.req.Endpoint,
		"subject_type": adm.ident.SubjectType, "subject_id": adm.ident.SubjectID, "token_id": adm.ident.ID,
		"error_type": typ, "reason": reason,
	}
}

// deny closes the reservation without usage. The request id is freed so the
// client can retry with the same idempotency key once the hook would allow it.
func (adm *gatewayAdmission) deny(typ, reason string) {
	db := adm.appCtx.AppDB()
	adm.rate.Release()
	details, _ := json.Marshal(map[string]any{"denied_by": "admission", "error_type": typ, "reason": truncate(reason, 500)})
	_, _ = db.Exec(`UPDATE usage_events SET status='denied', request_tokens=0, response_tokens=0, total_tokens=0,
		provider_cost_microunits=0, estimated_cost_cents=0, latency_ms=?, usage_details_json=?,
		request_id=request_id || ':denied:' || id
		WHERE id=? AND status='reserved'`, time.Since(adm.start).Milliseconds(), string(details), adm.reservation.ID)
	_ = dbAudit(db, adm.ident, "request.denied", adm.attempts[0].Provider, adm.attempts[0].Model, "denied", reason,
		map[string]any{"request_id": adm.req.RequestID, "denied_by": "admission", "error_type": typ})
}

type attemptFunc func(attempt providerAttempt, cfg *ProviderConfig, apiKey string, body map[string]any) (*chatResult, error)

// run tries each allowed route in order. It stops failing over once a
// stream has started sending bytes to the client.
func (adm *gatewayAdmission) run(body map[string]any, call attemptFunc) (*chatResult, providerAttempt, error) {
	ctx := adm.appCtx
	ident := adm.ident
	var lastErr error
	var result *chatResult
	used := adm.attempts[0]
	maxTokens := int64(0)
	if body != nil {
		maxTokens = int64Arg(body, "max_tokens")
	}
	for i, attempt := range adm.attempts {
		if err := policiesAllowRoute(adm.policies, attempt.Provider, attempt.Model, maxTokens, adm.viaPublic()); err != nil {
			continue
		}
		used = attempt
		if i > 0 && adm.req.CheckModality {
			if err := ensureModelSupportsRequest(ctx.AppDB(), ident.ProjectID, attempt.Provider, attempt.Model, body); err != nil {
				lastErr = err
				adm.failedAttempts++
				adm.attemptDetails = append(adm.attemptDetails, map[string]any{"provider": attempt.Provider, "model": attempt.Model, "status": "skipped", "error": err.Error()})
				continue
			}
		}
		cfg, err := providerConfigFor(ctx, ident.ProjectID, attempt.Provider)
		var callResult *chatResult
		if err == nil {
			key := ""
			if cfg.Provider != "openai-codex" {
				key, err = resolveProviderKey(ctx, cfg)
			}
			if err == nil {
				attemptBody := cloneMap(body)
				if attemptBody == nil {
					attemptBody = map[string]any{}
				}
				// Underscore keys are gateway bookkeeping, never provider input.
				for k := range attemptBody {
					if strings.HasPrefix(k, "_") {
						delete(attemptBody, k)
					}
				}
				attemptBody["model"] = attempt.Model
				callResult, err = call(attempt, cfg, key, attemptBody)
			}
		}
		if err == nil {
			result = callResult
			adm.attemptDetails = append(adm.attemptDetails, map[string]any{"provider": attempt.Provider, "model": attempt.Model, "status": "completed"})
			return result, used, nil
		}
		lastErr = err
		if adm.streamStarted {
			adm.attemptDetails = append(adm.attemptDetails, map[string]any{"provider": attempt.Provider, "model": attempt.Model, "status": "interrupted", "error": err.Error()})
			return callResult, used, err
		}
		adm.failedAttempts++
		adm.attemptDetails = append(adm.attemptDetails, map[string]any{"provider": attempt.Provider, "model": attempt.Model, "status": "failed", "error": err.Error()})
		retryable := isRetryableProviderError(err)
		ctx.EmitWithProject("llm.provider.failed", ident.ProjectID, map[string]any{
			"request_id": adm.req.RequestID, "provider": attempt.Provider, "model": attempt.Model,
			"subject_type": ident.SubjectType, "subject_id": ident.SubjectID, "endpoint": adm.req.Endpoint,
			"error": err.Error(), "retryable": retryable, "failover": retryable && i+1 < len(adm.attempts),
		})
		_ = dbAudit(ctx.AppDB(), ident, "provider.failed", attempt.Provider, attempt.Model, "error", err.Error(), map[string]any{"request_id": adm.req.RequestID, "retryable": retryable, "endpoint": adm.req.Endpoint})
		if !retryable {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no allowed provider route is available")
	}
	return nil, used, lastErr
}

// complete closes the reservation for a finished (or failed) request.
func (adm *gatewayAdmission) complete(used providerAttempt, result *chatResult, callErr error) (*UsageEvent, error) {
	ctx := adm.appCtx
	db := ctx.AppDB()
	ident := adm.ident
	latency := time.Since(adm.start).Milliseconds()
	ttft := latency
	if !adm.firstTokenAt.IsZero() {
		ttft = adm.firstTokenAt.Sub(adm.start).Milliseconds()
	} else if adm.req.Stream {
		ttft = 0
	}
	extra := map[string]any{}
	if adm.hookBypassed {
		extra["admission"] = "bypassed"
	}
	partial := false
	status := "completed"
	input := adm.req.EstimatedInput
	output := int64(0)
	var details json.RawMessage
	var cost int64
	var currency, providerRequestID string
	var costReported bool
	if result != nil {
		if result.RequestTokens > 0 {
			input = result.RequestTokens
		}
		output = result.ResponseTokens
		details = result.UsageDetails
		cost, currency, costReported = result.ProviderCostMicrounits, result.ProviderCostCurrency, result.ProviderCostReported
		providerRequestID = result.RequestID
		if result.UsageEstimated {
			extra["usage_source"] = "estimated"
		}
		if len(result.Units) > 0 {
			extra["units"] = result.Units
		}
		if result.ClientDisconnected {
			extra["client_disconnected"] = true
		}
	}
	if callErr != nil {
		if result != nil && adm.streamStarted && output > 0 {
			partial = true
			extra["partial"] = true
			extra["error"] = truncate(callErr.Error(), 500)
		} else {
			status = "failed"
			output = 0
			if result == nil || result.RequestTokens == 0 {
				input = adm.req.EstimatedInput
			}
			cost, currency, costReported = 0, "", false
			providerRequestID = ""
			details = nil
		}
	} else if result != nil && result.ClientDisconnected {
		partial = true
		extra["partial"] = true
	}
	details = mergeUsageDetails(details, adm.attemptDetails, adm.failedAttempts)
	details = mergeDetailFields(details, extra)
	ev, err := finishUsageReservationFull(db, adm.reservation.ID, finishArgs{
		Provider: used.Provider, Model: used.Model, Input: input, Output: output, Status: status,
		ProviderRequestID: providerRequestID, ReportedCost: cost, ReportedCurrency: currency, CostReported: costReported,
		UsageDetails: details, LatencyMS: latency, TTFTMS: ttft, Partial: partial,
	})
	if err != nil {
		if callErr != nil {
			return nil, adm.clientError(fmt.Errorf("provider failed: %v; record usage: %w", callErr, err))
		}
		return nil, err
	}
	adm.rate.Settle(ev.RequestTokens + ev.ResponseTokens)
	if asyncSettlement && ev.AdmissionJSON != "{}" && ev.AdmissionJSON != "" {
		if outboxID := outboxIDForUsage(db, ev.ID); outboxID > 0 {
			go func() { _, _ = deliverOutbox(ctx, outboxID) }()
		}
	}
	payloadExtra := map[string]any{}
	if adm.req.Endpoint != endpointChat && adm.req.Endpoint != "responses" {
		payloadExtra["operation"] = strings.SplitN(adm.req.Endpoint, ".", 2)[0]
	}
	if status == "failed" {
		payloadExtra["error"] = callErr.Error()
		_ = dbAudit(db, ident, "request.failed", used.Provider, used.Model, "error", callErr.Error(), map[string]any{"request_id": adm.req.RequestID, "usage_event_id": ev.ID, "endpoint": adm.req.Endpoint})
		payload := usageEventPayload(ev, payloadExtra)
		ctx.EmitWithProject("llm.usage.recorded", ident.ProjectID, payload)
		ctx.EmitWithProject("llm.request.failed", ident.ProjectID, payload)
		return ev, adm.clientError(callErr)
	}
	_ = dbAudit(db, ident, "request.completed", used.Provider, used.Model, "completed", "", map[string]any{"request_id": adm.req.RequestID, "provider_request_id": ev.ProviderRequestID, "usage_event_id": ev.ID, "endpoint": adm.req.Endpoint, "partial": partial})
	payload := usageEventPayload(ev, payloadExtra)
	ctx.EmitWithProject("llm.usage.recorded", ident.ProjectID, payload)
	ctx.EmitWithProject("llm.request.completed", ident.ProjectID, payload)
	if result != nil {
		result.UsageEvent = ev
		if adm.viaPublic() {
			result.Body = withPublicModelName(result.Body, adm.public.Name)
		}
	}
	return ev, adm.clientError(callErr)
}

// Public model names hide which provider serves a request. Responses carry
// the public name back, and provider failures become generic errors. The
// provider, its model id, and the raw error stay in usage records, audit
// logs, and events, which callers of the public name never see.

const (
	defaultInputEstimateMargin = 1.25
	publicModelUnavailableMsg  = "The model is temporarily unavailable."
)

// applyInputMargin pads a pre-request input estimate so reservations and
// admission checks err on the high side. Hooks may set their own margin.
func applyInputMargin(estimate int64, hook *AdmissionConfig) int64 {
	if estimate <= 0 {
		return estimate
	}
	margin := defaultInputEstimateMargin
	if hook != nil && hook.InputEstimateMargin > 0 {
		margin = hook.InputEstimateMargin
	}
	return int64(math.Ceil(float64(estimate) * margin))
}

// clientError is the error returned to the caller. Requests made under a
// public model name get a generic OpenAI-style error with no provider
// detail; provider-prefixed requests see the raw error.
func (adm *gatewayAdmission) clientError(err error) error {
	if err == nil || !adm.viaPublic() || errors.Is(err, context.Canceled) {
		return err
	}
	return publicModelError(err)
}

func publicModelError(err error) error {
	status, typ := errorStatus(err)
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) ||
		status == http.StatusGatewayTimeout || status == http.StatusRequestTimeout:
		return typedErr{status: http.StatusGatewayTimeout, typ: "timeout_error", msg: "The model took too long to respond."}
	case status == http.StatusTooManyRequests:
		return typedErr{status: http.StatusTooManyRequests, typ: "rate_limit_error", msg: "The model is receiving too many requests. Please retry shortly."}
	case typ == "invalid_request_error" || (typ == "provider_error" && status >= 400 && status < 500 &&
		status != http.StatusUnauthorized && status != http.StatusForbidden && status != http.StatusNotFound):
		return typedErr{status: http.StatusBadRequest, typ: "invalid_request_error", msg: "The model could not process this request."}
	default:
		return typedErr{status: http.StatusBadGateway, typ: "server_error", msg: publicModelUnavailableMsg}
	}
}

// withPublicModelName replaces a JSON response's top-level model field.
// Other fields keep their exact bytes.
func withPublicModelName(body []byte, name string) []byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return body
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(trimmed, &fields) != nil {
		return body
	}
	if _, ok := fields["model"]; !ok {
		return body
	}
	fields["model"], _ = json.Marshal(name)
	out, err := json.Marshal(fields)
	if err != nil {
		return body
	}
	return out
}

// asyncSettlement sends a settlement right after the request finishes instead
// of waiting for the next outbox tick. Tests turn it off for determinism.
var asyncSettlement = true

func outboxIDForUsage(db *sql.DB, usageEventID int64) int64 {
	var id int64
	_ = db.QueryRow(`SELECT id FROM admission_outbox WHERE usage_event_id=?`, usageEventID).Scan(&id)
	return id
}

type finishArgs struct {
	Provider          string
	Model             string
	Input             int64
	Output            int64
	Status            string
	ProviderRequestID string
	ReportedCost      int64
	ReportedCurrency  string
	CostReported      bool
	UsageDetails      json.RawMessage
	LatencyMS         int64
	TTFTMS            int64
	Partial           bool
}

// finishUsageReservationFull closes a reservation and, when the request was
// admitted by a hook, queues its settlement in the same transaction.
func finishUsageReservationFull(db *sql.DB, id int64, f finishArgs) (*UsageEvent, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := scanUsageEvent(tx.QueryRow(`SELECT `+usageEventColumns+` FROM usage_events WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	costMicrounits := int64(0)
	costCurrency := "USD"
	costStatus := "unpriced"
	costSource := ""
	rateID := int64(0)
	usageDetails := f.UsageDetails
	if f.CostReported {
		costMicrounits = f.ReportedCost
		costCurrency = firstNonEmpty(strings.ToUpper(f.ReportedCurrency), "USD")
		costStatus = "reported"
		costSource = "provider_response"
	} else if rate, resolveErr := resolveProviderRate(tx, current.ProjectID, f.Provider, f.Model); resolveErr != nil {
		return nil, resolveErr
	} else if rate != nil && rate.hasMeteredRate() && f.Status == "completed" {
		var details struct {
			CachedInputTokens  int64 `json:"cached_input_tokens"`
			CacheWriteTokens   int64 `json:"cache_write_tokens"`
			CacheWrite1hTokens int64 `json:"cache_write_1h_tokens"`
		}
		_ = json.Unmarshal(usageDetails, &details)
		costMicrounits = calculateProviderCostDetailed(rate, f.Input, f.Output, details.CachedInputTokens, details.CacheWriteTokens, details.CacheWrite1hTokens)
		costCurrency = rate.Currency
		costStatus = "calculated"
		if providerCostCalculationPartial(rate, f.Input, details.CacheWrite1hTokens) {
			costStatus = "partial"
		}
		costSource = rate.Source
		rateID = rate.ID
	}
	if len(usageDetails) == 0 {
		usageDetails = json.RawMessage(`{}`)
	}
	if usageHasUnknownAttemptCost(usageDetails) {
		costStatus = "partial"
	}
	res, err := tx.Exec(`UPDATE usage_events SET provider=?, model=?, request_tokens=?, response_tokens=?, total_tokens=?,
		estimated_cost_cents=?, provider_cost_microunits=?, provider_cost_currency=?, provider_cost_status=?,
		provider_cost_source=?, provider_rate_id=?, usage_details_json=?, status=?, provider_request_id=?,
		latency_ms=?, ttft_ms=?
		WHERE id=? AND status='reserved'`,
		f.Provider, f.Model, f.Input, f.Output, f.Input+f.Output, (costMicrounits+5000)/10000, costMicrounits, costCurrency,
		costStatus, costSource, rateID, string(usageDetails), f.Status, f.ProviderRequestID, f.LatencyMS, f.TTFTMS, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errors.New("usage reservation is no longer active")
	}
	ev, err := scanUsageEvent(tx.QueryRow(`SELECT `+usageEventColumns+` FROM usage_events WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	if err := enqueueSettlement(tx, ev, f.Partial); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ev, nil
}

func mergeDetailFields(raw json.RawMessage, fields map[string]any) json.RawMessage {
	if len(fields) == 0 {
		return raw
	}
	out := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	if out == nil {
		out = map[string]any{}
	}
	for k, v := range fields {
		out[k] = v
	}
	b, _ := json.Marshal(out)
	return b
}

func firstNonEmptyRaw(raw json.RawMessage, fallback string) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(fallback)
	}
	return raw
}

func isUserError(err error) bool {
	var te typedErr
	return errors.As(err, &te) && te.typ == "invalid_request_error"
}

// identityFromArgs builds the identity for MCP tool calls, which are trusted
// app-to-app calls without a bearer token.
func identityFromArgs(args map[string]any) (*TokenIdentity, error) {
	ident := &TokenIdentity{
		ProjectID:   projectFromArgs(args),
		SubjectType: firstNonEmpty(strArg(args, "subject_type"), "app"),
		SubjectID:   firstNonEmpty(strArg(args, "subject_id"), "mcp"),
	}
	if v, ok := args["metadata"]; ok {
		md, err := tokenMetadataFromAny(v)
		if err != nil {
			return nil, err
		}
		ident.Metadata = md
		delete(args, "metadata")
	}
	return ident, nil
}

func writeAdmissionHeaders(w http.ResponseWriter, adm *gatewayAdmission) {
	if adm == nil {
		return
	}
	writeRateHeaders(w, adm.RateStatus)
	if adm.admissionRef != "" {
		w.Header().Set("X-Apteva-Admission-Ref", adm.admissionRef)
	}
}
