package main

import (
	sdk "github.com/apteva/app-sdk"
)

// crmCaller is the abstraction over CallAppResult("crm", …). nil-safe
// throughout: when CRM isn't bound, all methods are no-ops returning
// zero values + nil error so the registration path "just works"
// without conditionals everywhere.

type crmCaller interface {
	UpsertContactByChannel(req CRMUpsertReq) (CRMUpsertResp, error)
	LogActivity(req CRMLogActivityReq) error
}

type CRMUpsertReq struct {
	Kind      string         `json:"kind"`     // "email" | "phone"
	Value     string         `json:"value"`
	Defaults  map[string]any `json:"defaults,omitempty"`
	Source    string         `json:"source,omitempty"`
	ProjectID string         `json:"_project_id,omitempty"`
}

type CRMUpsertResp struct {
	Contact struct {
		ID          int64  `json:"id"`
		DisplayName string `json:"display_name,omitempty"`
	} `json:"contact"`
	WasCreated bool `json:"was_created"`
}

type CRMLogActivityReq struct {
	ContactID int64  `json:"contact_id"`
	Kind      string `json:"kind"`
	Body      string `json:"body"`
	Source    string `json:"source,omitempty"`
	ProjectID string `json:"_project_id,omitempty"`
}

// ─── Production wiring ────────────────────────────────────────────

type platformCRMCaller struct {
	ctx *sdk.AppCtx
}

func newPlatformCRMCaller(ctx *sdk.AppCtx) crmCaller {
	return &platformCRMCaller{ctx: ctx}
}

// bound returns true if a CRM integration is currently bound. Used to
// short-circuit calls so they don't 403 with a noisy error in the
// non-bound path.
func (p *platformCRMCaller) bound() bool {
	if p == nil || p.ctx == nil {
		return false
	}
	return p.ctx.IntegrationFor("crm") != nil
}

func (p *platformCRMCaller) UpsertContactByChannel(req CRMUpsertReq) (CRMUpsertResp, error) {
	if !p.bound() {
		return CRMUpsertResp{}, nil
	}
	args := map[string]any{
		"kind":     req.Kind,
		"value":    req.Value,
		"defaults": req.Defaults,
		"source":   req.Source,
	}
	if req.ProjectID != "" {
		args["_project_id"] = req.ProjectID
	}
	var out CRMUpsertResp
	err := p.ctx.PlatformAPI().CallAppResult("crm", "contacts_upsert_by_channel", args, &out)
	return out, err
}

func (p *platformCRMCaller) LogActivity(req CRMLogActivityReq) error {
	if !p.bound() || req.ContactID == 0 {
		return nil
	}
	args := map[string]any{
		"contact_id": req.ContactID,
		"kind":       req.Kind,
		"body":       req.Body,
		"source":     req.Source,
	}
	if req.ProjectID != "" {
		args["_project_id"] = req.ProjectID
	}
	var out map[string]any
	return p.ctx.PlatformAPI().CallAppResult("crm", "contacts_log_activity", args, &out)
}
