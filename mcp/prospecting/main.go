package main

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestBytes []byte

var globalCtx *sdk.AppCtx

type App struct{}

func main() { sdk.Run(&App{}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestBytes)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("prospecting requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("prospecting mounted", "project_id", ctx.CurrentProject())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/overview", Handler: a.handleOverview},
		{Pattern: "/capabilities", Handler: a.handleCapabilities},
		{Pattern: "/profiles", Handler: a.handleProfiles},
		{Pattern: "/profiles/", Handler: a.handleProfileItem},
		{Pattern: "/discover", Handler: a.handleDiscover},
		{Pattern: "/qualify", Handler: a.handleQualifyBatch},
		{Pattern: "/runs", Handler: a.handleRuns},
		{Pattern: "/candidates/import", Handler: a.handleCandidateImport},
		{Pattern: "/candidates/export", Handler: a.handleCandidateExport},
		{Pattern: "/candidates/purge-rejected", Handler: a.handleCandidatePurgeRejected},
		{Pattern: "/candidates", Handler: a.handleCandidates},
		{Pattern: "/candidates/", Handler: a.handleCandidateItem},
		{Pattern: "/exclusions", Handler: a.handleExclusions},
		{Pattern: "/exclusions/", Handler: a.handleExclusionItem},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	profileFields := map[string]any{
		"name":          sString(),
		"description":   sString(),
		"industries":    sStringArray(),
		"locations":     sStringArray(),
		"employee_min":  sInteger(),
		"employee_max":  sInteger(),
		"target_titles": sStringArray(),
		"keywords":      sStringArray(),
	}
	candidateFields := map[string]any{
		"company_name":        sString(),
		"company_domain":      sString(),
		"website":             sString(),
		"person_first_name":   sString(),
		"person_last_name":    sString(),
		"person_display_name": sString(),
		"job_title":           sString(),
		"email":               sString(),
		"phone":               sString(),
		"summary":             sString(),
		"source_url":          sString(),
	}
	return []sdk.Tool{
		{Name: "prospecting_overview", Description: "Summarize target profiles, runs, candidate statuses, evidence, and exclusions. Args: none.", InputSchema: schemaObject(nil, nil), Handler: a.toolOverview},
		{Name: "prospecting_capabilities", Description: "Report whether the optional Web discovery and CRM handoff integrations are currently connected. Args: none.", InputSchema: schemaObject(nil, nil), Handler: a.toolCapabilities},
		{Name: "prospecting_profiles_create", Description: "Create a target profile. Args: name, description?, industries?, locations?, employee_min?, employee_max?, target_titles?, keywords?.", InputSchema: schemaObject(profileFields, []string{"name"}), Handler: a.toolProfilesCreate},
		{Name: "prospecting_profiles_list", Description: "List target profiles. Args: status? (active default, archived, all).", InputSchema: schemaObject(map[string]any{"status": sString()}, nil), Handler: a.toolProfilesList},
		{Name: "prospecting_profiles_get", Description: "Get one target profile. Args: id.", InputSchema: schemaObject(map[string]any{"id": sInteger()}, []string{"id"}), Handler: a.toolProfilesGet},
		{Name: "prospecting_profiles_update", Description: "Patch a target profile. Args: id and any editable profile fields.", InputSchema: schemaObject(mergeSchemas(map[string]any{"id": sInteger()}, profileFields), []string{"id"}), Handler: a.toolProfilesUpdate},
		{Name: "prospecting_profiles_archive", Description: "Archive a target profile. Args: id.", InputSchema: schemaObject(map[string]any{"id": sInteger()}, []string{"id"}), Handler: a.toolProfilesArchive},
		{Name: "prospecting_search_run", Description: "Run a bounded Web search, fall back to another engine when blocked, filter deterministic noise, and persist new company candidates. Args: profile_id, query?, limit? (default 20, max 50), engine? (default google), fallback_engine? (default duckduckgo). Does not contact anyone.", InputSchema: schemaObject(map[string]any{"profile_id": sInteger(), "query": sString(), "limit": sInteger(), "engine": sString(), "fallback_engine": sString()}, []string{"profile_id"}), Handler: a.toolSearchRun},
		{Name: "prospecting_runs_list", Description: "List discovery runs. Args: profile_id?, limit?.", InputSchema: schemaObject(map[string]any{"profile_id": sInteger(), "limit": sInteger()}, nil), Handler: a.toolRunsList},
		{Name: "prospecting_candidates_create", Description: "Create a candidate manually without Web or CRM. Args: profile_id? (uses the newest active profile or creates Imported leads), company_name, website?, person fields?, email?, phone?, summary?, source_url?.", InputSchema: schemaObject(mergeSchemas(map[string]any{"profile_id": sInteger()}, candidateFields), []string{"company_name"}), Handler: a.toolCandidatesCreate},
		{Name: "prospecting_candidates_import", Description: "Bulk seed candidates without Web or CRM. Args: profile_id?; candidates? array of candidate objects, or data string plus format auto|csv|json. Maximum 1000 rows.", InputSchema: schemaObject(map[string]any{"profile_id": sInteger(), "candidates": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}, "data": sString(), "format": sString()}, nil), Handler: a.toolCandidatesImport},
		{Name: "prospecting_candidates_export", Description: "Export a portable JSON candidate set without CRM. Args: profile_id?, status?, q?, limit? (max 200).", InputSchema: schemaObject(map[string]any{"profile_id": sInteger(), "status": sString(), "q": sString(), "limit": sInteger()}, nil), Handler: a.toolCandidatesExport},
		{Name: "prospecting_candidates_search", Description: "Search candidates. Args: profile_id?, status?, q?, limit?, offset?.", InputSchema: schemaObject(map[string]any{"profile_id": sInteger(), "status": sString(), "q": sString(), "limit": sInteger(), "offset": sInteger()}, nil), Handler: a.toolCandidatesSearch},
		{Name: "prospecting_candidates_get", Description: "Get one candidate with evidence and CRM handoff state. Args: id.", InputSchema: schemaObject(map[string]any{"id": sInteger()}, []string{"id"}), Handler: a.toolCandidatesGet},
		{Name: "prospecting_candidates_update", Description: "Patch a candidate's company or person details and recalculate explainable scores. Args: id and editable fields.", InputSchema: schemaObject(mergeSchemas(map[string]any{"id": sInteger()}, candidateFields), []string{"id"}), Handler: a.toolCandidatesUpdate},
		{Name: "prospecting_candidates_research", Description: "Research one candidate through Web and persist cited evidence. Args: id, question?. Does not contact anyone.", InputSchema: schemaObject(map[string]any{"id": sInteger(), "question": sString()}, []string{"id"}), Handler: a.toolCandidatesResearch},
		{Name: "prospecting_candidates_qualify", Description: "Deterministically qualify one candidate from up to five first-party website pages, extracting contact details, operating signals, location, and evidence-backed scores without AI. Args: id, max_pages? (default 5). Does not contact anyone.", InputSchema: schemaObject(map[string]any{"id": sInteger(), "max_pages": sInteger()}, []string{"id"}), Handler: a.toolCandidatesQualify},
		{Name: "prospecting_candidates_qualify_batch", Description: "Deterministically qualify the next bounded batch of unenriched candidates without AI. Args: profile_id?, status? (default ready), limit? (default 10, max 25), max_pages? (default 5, max 5), requalify? (default false). Does not contact anyone.", InputSchema: schemaObject(map[string]any{"profile_id": sInteger(), "status": sString(), "limit": sInteger(), "max_pages": sInteger(), "requalify": sBoolean()}, nil), Handler: a.toolCandidatesQualifyBatch},
		{Name: "prospecting_candidates_defer", Description: "Defer a candidate. Args: id, reason?.", InputSchema: schemaObject(map[string]any{"id": sInteger(), "reason": sString()}, []string{"id"}), Handler: a.toolCandidatesDefer},
		{Name: "prospecting_candidates_reject", Description: "Reject a candidate. Args: id, reason?, exclude_company? (default false). Rejected or excluded candidates are not contacted.", InputSchema: schemaObject(map[string]any{"id": sInteger(), "reason": sString(), "exclude_company": sBoolean()}, []string{"id"}), Handler: a.toolCandidatesReject},
		{Name: "prospecting_candidates_purge_rejected", Description: "PERMANENT DELETE: remove every rejected candidate and its Prospecting evidence from the current project. Args: confirm=true. Does not delete CRM contacts or exclusions.", InputSchema: schemaObject(map[string]any{"confirm": sBoolean()}, []string{"confirm"}), Handler: a.toolCandidatesPurgeRejected},
		{Name: "prospecting_candidates_accept", Description: "REAL CRM WRITE: accept a candidate and idempotently upsert it into CRM. Requires email or phone. Args: id, list_ids? (CRM list ids or slugs). This does not send a message or create an opportunity.", InputSchema: schemaObject(map[string]any{"id": sInteger(), "list_ids": map[string]any{"type": "array"}}, []string{"id"}), Handler: a.toolCandidatesAccept},
		{Name: "prospecting_exclusions_list", Description: "List exclusions. Args: kind? (domain|company|email|phone), limit?.", InputSchema: schemaObject(map[string]any{"kind": sString(), "limit": sInteger()}, nil), Handler: a.toolExclusionsList},
		{Name: "prospecting_exclusions_remove", Description: "Remove one exclusion. Args: id.", InputSchema: schemaObject(map[string]any{"id": sInteger()}, []string{"id"}), Handler: a.toolExclusionsRemove},
	}
}

func (a *App) toolOverview(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	return overview(ctx.AppDB(), ctx.CurrentProject())
}

func (a *App) toolProfilesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	p, err := createProfile(ctx.AppDB(), ctx.CurrentProject(), args)
	if err != nil {
		return nil, err
	}
	ctx.EmitWithProject("prospecting.profile.created", p.ProjectID, map[string]any{"id": p.ID, "name": p.Name})
	return map[string]any{"profile": p}, nil
}

func (a *App) toolProfilesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	profiles, err := listProfiles(ctx.AppDB(), ctx.CurrentProject(), stringArg(args, "status"))
	return map[string]any{"profiles": profiles, "count": len(profiles)}, err
}

func (a *App) toolProfilesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	p, err := getProfile(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, sql.ErrNoRows
	}
	return map[string]any{"profile": p, "search_query": buildSearchQuery(p)}, nil
}

func (a *App) toolProfilesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	p, err := updateProfile(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "id"), args)
	return map[string]any{"profile": p}, err
}

func (a *App) toolProfilesArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	p, err := archiveProfile(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "id"))
	return map[string]any{"profile": p}, err
}

func (a *App) toolSearchRun(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return runDiscoveryWithOptions(ctx, int64Arg(args, "profile_id"), stringArg(args, "query"), intArg(args, "limit", 20), stringArg(args, "engine"), stringArg(args, "fallback_engine"))
}

func (a *App) toolRunsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	runs, err := listSearchRuns(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "profile_id"), intArg(args, "limit", 50))
	return map[string]any{"runs": runs, "count": len(runs)}, err
}

func (a *App) toolCandidatesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	profileID := int64Arg(args, "profile_id")
	profile, profileCreated, err := resolveSeedProfile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	input, err := candidateInputFromArgs(args)
	if err != nil {
		return nil, err
	}
	input.ProfileID = profileID
	candidate, created, err := insertCandidate(ctx.AppDB(), ctx.CurrentProject(), input, profile)
	if err != nil {
		return nil, err
	}
	if created {
		ctx.EmitWithProject("prospecting.candidate.created", candidate.ProjectID, candidateEvent(candidate))
	}
	return map[string]any{"candidate": candidate, "was_created": created, "profile_created": profileCreated}, nil
}

func (a *App) toolCandidatesSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	candidates, total, err := listCandidates(ctx.AppDB(), ctx.CurrentProject(), candidateFilter{
		ProfileID: int64Arg(args, "profile_id"), Status: stringArg(args, "status"), Q: stringArg(args, "q"),
		Limit: intArg(args, "limit", 100), Offset: intArg(args, "offset", 0),
	})
	return map[string]any{"candidates": candidates, "count": len(candidates), "total": total}, err
}

func (a *App) toolCandidatesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	candidate, err := getCandidate(ctx.AppDB(), ctx.CurrentProject(), id)
	if err != nil {
		return nil, err
	}
	if candidate == nil {
		return nil, sql.ErrNoRows
	}
	evidence, err := listEvidence(ctx.AppDB(), ctx.CurrentProject(), id)
	if err != nil {
		return nil, err
	}
	handoff, err := getHandoff(ctx.AppDB(), ctx.CurrentProject(), id)
	return map[string]any{"candidate": candidate, "evidence": evidence, "handoff": handoff}, err
}

func (a *App) toolCandidatesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	candidate, err := updateCandidate(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "id"), args)
	return map[string]any{"candidate": candidate}, err
}

func (a *App) toolCandidatesResearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return researchCandidate(ctx, int64Arg(args, "id"), stringArg(args, "question"))
}

func (a *App) toolCandidatesQualify(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return qualifyCandidate(ctx, int64Arg(args, "id"), intArg(args, "max_pages", 5))
}

func (a *App) toolCandidatesQualifyBatch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return qualifyBatch(ctx, int64Arg(args, "profile_id"), stringArg(args, "status"), intArg(args, "limit", 10), intArg(args, "max_pages", 5), boolArg(args, "requalify", false))
}

func (a *App) toolCandidatesDefer(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	candidate, err := setCandidateDecision(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "id"), "deferred", stringArg(args, "reason"))
	return map[string]any{"candidate": candidate}, err
}

func (a *App) toolCandidatesReject(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	reason := stringArg(args, "reason")
	candidate, err := setCandidateDecision(ctx.AppDB(), ctx.CurrentProject(), id, "rejected", reason)
	if err != nil {
		return nil, err
	}
	var exclusion *Exclusion
	if boolArg(args, "exclude_company", false) {
		kind, value := "company", candidate.CompanyName
		if candidate.CompanyDomain != "" {
			kind, value = "domain", candidate.CompanyDomain
		}
		exclusion, err = addExclusion(ctx.AppDB(), ctx.CurrentProject(), kind, value, reason)
		if err != nil {
			return nil, err
		}
	}
	ctx.EmitWithProject("prospecting.candidate.rejected", candidate.ProjectID, candidateEvent(candidate))
	return map[string]any{"candidate": candidate, "exclusion": exclusion}, nil
}

func (a *App) toolCandidatesPurgeRejected(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if !boolArg(args, "confirm", false) {
		return nil, errors.New("confirm=true is required to permanently delete rejected candidates")
	}
	deleted, err := purgeRejectedCandidates(ctx.AppDB(), ctx.CurrentProject())
	return map[string]any{"deleted": deleted, "status": "rejected"}, err
}

func (a *App) toolCandidatesAccept(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return acceptCandidate(ctx, int64Arg(args, "id"), args["list_ids"])
}

func (a *App) toolExclusionsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	exclusions, err := listExclusions(ctx.AppDB(), ctx.CurrentProject(), stringArg(args, "kind"), intArg(args, "limit", 200))
	return map[string]any{"exclusions": exclusions, "count": len(exclusions)}, err
}

func (a *App) toolExclusionsRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	err := removeExclusion(ctx.AppDB(), ctx.CurrentProject(), id)
	return map[string]any{"removed": err == nil, "id": id}, err
}

func candidateInputFromArgs(args map[string]any) (candidateInput, error) {
	input := candidateInput{
		CompanyName: stringArg(args, "company_name"), CompanyDomain: stringArg(args, "company_domain"), Website: stringArg(args, "website"),
		PersonFirstName: stringArg(args, "person_first_name"), PersonLastName: stringArg(args, "person_last_name"), PersonDisplayName: stringArg(args, "person_display_name"),
		JobTitle: stringArg(args, "job_title"), Email: stringArg(args, "email"), Phone: stringArg(args, "phone"), Summary: stringArg(args, "summary"),
		Source: defaultString(stringArg(args, "source"), "manual"), SourceURL: stringArg(args, "source_url"),
	}
	if input.CompanyName == "" {
		return input, errors.New("company_name required")
	}
	if input.Email != "" && normalizeEmail(input.Email) == "" {
		return input, errors.New("email is invalid")
	}
	if input.Phone != "" && normalizePhone(input.Phone) == "" {
		return input, errors.New("phone is invalid")
	}
	return input, nil
}

func requestCtx(r *http.Request) *sdk.AppCtx {
	if globalCtx == nil {
		return nil
	}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		return globalCtx.WithProject(pid)
	}
	return globalCtx
}

func schemaObject(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	out := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func mergeSchemas(sets ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, set := range sets {
		for key, value := range set {
			out[key] = value
		}
	}
	return out
}

func sString() map[string]any      { return map[string]any{"type": "string"} }
func sInteger() map[string]any     { return map[string]any{"type": "integer"} }
func sBoolean() map[string]any     { return map[string]any{"type": "boolean"} }
func sStringArray() map[string]any { return map[string]any{"type": "array", "items": sString()} }

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error, status int) {
	if errors.Is(err, sql.ErrNoRows) {
		status = http.StatusNotFound
	}
	http.Error(w, err.Error(), status)
}

func parseID(raw string) int64 {
	id, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return id
}
