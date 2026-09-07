package main

import (
	"context"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"strconv"
	"strings"
)

// Only explicitly trusted backend apps may resolve the external principal of
// an agent thread. Browser input and agent tool arguments cannot call this API.
func (a *App) resolveThreadIdentity(c *sdk.Caller, allowed string, agentID int64, threadID string) (map[string]any, error) {
	if c == nil || c.AgentID != 0 || c.AppInstallID <= 0 || c.AppName == "" || c.ProjectID == "" {
		return nil, errors.New("authenticated sibling app required")
	}
	trusted := false
	for _, id := range strings.Split(allowed, ",") {
		if strings.TrimSpace(id) == strconv.FormatInt(c.AppInstallID, 10) {
			trusted = true
		}
	}
	if !trusted {
		return nil, errors.New("identity resolver app not permitted")
	}
	if agentID <= 0 || strings.TrimSpace(threadID) == "" {
		return nil, errors.New("agent and thread required")
	}
	conv, err := a.boundConversation(&callIdentity{ProjectID: c.ProjectID, AgentID: agentID, ThreadID: threadID})
	if err != nil {
		return nil, err
	}
	if conv == nil || conv.OwnerUserID >= 0 {
		return nil, errors.New("active external conversation required")
	}
	if member, err := a.store.IsParticipantAgent(conv.ID, agentID); err != nil || !member {
		return nil, errors.New("agent no longer participates")
	}
	var active int
	if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM conversations WHERE id=? AND archived_at IS NULL`, conv.ID).Scan(&active); err != nil || active != 1 {
		return nil, errors.New("active external conversation required")
	}
	var project, issuer, install, kind, subject, org string
	err = a.store.db.QueryRow(`SELECT project_id,issuer_app,issuer_install_id,subject_type,subject_id,organization_id FROM external_principals WHERE id=? AND project_id=?`, -conv.OwnerUserID, c.ProjectID).Scan(&project, &issuer, &install, &kind, &subject, &org)
	if err != nil {
		return nil, errors.New("external principal unavailable")
	}
	return map[string]any{"project_id": project, "issuer_app": issuer, "issuer_install_id": install, "subject_type": kind, "subject_id": subject, "organization_id": org, "conversation_id": conv.ID}, nil
}

func (a *App) toolResolveThreadIdentity(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	if app == nil {
		return nil, errors.New("app context required")
	}
	agentID, _ := strconv.ParseInt(strings.TrimSpace(toIdentityString(args["agent_id"])), 10, 64)
	threadID, _ := args["thread_id"].(string)
	caller := sdk.CallerFrom(ctx)
	// The bound app callback validates and replaces _project_id before
	// dispatch. The SDK exposes that scope through CurrentProject; older
	// gateways do not also stamp it into the agent-oriented caller header.
	if caller != nil && caller.AppInstallID > 0 && caller.AppName != "" && caller.ProjectID == "" {
		copy := *caller
		copy.ProjectID = app.CurrentProject()
		caller = &copy
	}
	return a.resolveThreadIdentity(caller, app.Config()["identity_resolver_install_ids"], agentID, threadID)
}

func toIdentityString(v any) string {
	switch n := v.(type) {
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(n, 10)
	case string:
		return n
	}
	return ""
}
