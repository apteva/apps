package main

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func buildSearchQuery(profile *TargetProfile) string {
	parts := []string{}
	if len(profile.Industries) > 0 {
		parts = append(parts, groupedQuery(profile.Industries))
	}
	if len(profile.Locations) > 0 {
		parts = append(parts, groupedQuery(profile.Locations))
	}
	if len(profile.Keywords) > 0 {
		parts = append(parts, groupedQuery(profile.Keywords))
	}
	if len(parts) == 0 {
		parts = append(parts, quoteQuery(profile.Name))
	}
	parts = append(parts, "company")
	return strings.Join(parts, " ")
}

func groupedQuery(values []string) string {
	items := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			items = append(items, quoteQuery(value))
		}
	}
	if len(items) == 0 {
		return ""
	}
	if len(items) == 1 {
		return items[0]
	}
	return "(" + strings.Join(items, " OR ") + ")"
}

func quoteQuery(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), `"`, "")
	if strings.Contains(value, " ") {
		return `"` + value + `"`
	}
	return value
}

func runDiscovery(ctx *sdk.AppCtx, profileID int64, query string, limit int) (map[string]any, error) {
	if ctx == nil || ctx.AppDB() == nil {
		return nil, errors.New("prospecting context unavailable")
	}
	pid := ctx.CurrentProject()
	profile, err := getProfile(ctx.AppDB(), pid, profileID)
	if err != nil {
		return nil, err
	}
	if profile == nil || profile.Status != "active" {
		return nil, errors.New("active target profile not found")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		query = buildSearchQuery(profile)
	}
	limit = clamp(limit, 1, 50)
	run, err := startSearchRun(ctx.AppDB(), pid, profileID, query, limit)
	if err != nil {
		return nil, err
	}

	var webOut webSearchOutput
	callErr := ctx.PlatformAPI().CallAppResult("web", "web_search", map[string]any{
		"query":     query,
		"limit":     limit,
		"visit_top": false,
		"store":     true,
	}, &webOut)
	if callErr != nil {
		_ = finishSearchRun(ctx.AppDB(), pid, run.ID, "failed", 0, callErr.Error())
		return nil, fmt.Errorf("web search: %w", callErr)
	}
	if webOut.Blocked {
		errText := defaultString(webOut.Error, "search provider blocked the request")
		_ = finishSearchRun(ctx.AppDB(), pid, run.ID, "failed", 0, errText)
		return nil, errors.New(errText)
	}

	created := make([]Candidate, 0, len(webOut.Results))
	duplicates := 0
	excluded := 0
	for _, result := range webOut.Results {
		website, domain := normalizeWebsite(result.URL)
		if website == "" || domain == "" {
			continue
		}
		input := candidateInput{
			ProfileID:     profileID,
			RunID:         &run.ID,
			CompanyName:   cleanCompanyTitle(result.Title, domain),
			CompanyDomain: domain,
			Website:       website,
			Summary:       result.Snippet,
			Source:        "web_search",
			SourceURL:     result.URL,
			CanonicalKey:  canonicalCandidateKey(domain, website, result.Title, result.URL),
		}
		blocked, err := isExcluded(ctx.AppDB(), pid, input)
		if err != nil {
			_ = finishSearchRun(ctx.AppDB(), pid, run.ID, "failed", len(created), err.Error())
			return nil, err
		}
		if blocked {
			excluded++
			continue
		}
		candidate, wasCreated, err := insertCandidate(ctx.AppDB(), pid, input, profile)
		if err != nil {
			_ = finishSearchRun(ctx.AppDB(), pid, run.ID, "failed", len(created), err.Error())
			return nil, err
		}
		if !wasCreated {
			duplicates++
			continue
		}
		_ = addEvidence(ctx.AppDB(), pid, Evidence{
			CandidateID: candidate.ID,
			SourceKind:  "web_search",
			Title:       result.Title,
			URL:         result.URL,
			Excerpt:     result.Snippet,
			RetrievedAt: defaultString(result.FetchedAt, nowUTC()),
		})
		if rescored, scoreErr := rescoreCandidate(ctx.AppDB(), pid, candidate.ID); scoreErr == nil {
			candidate = rescored
		}
		created = append(created, *candidate)
		ctx.EmitWithProject("prospecting.candidate.created", pid, candidateEvent(candidate))
	}
	if err := finishSearchRun(ctx.AppDB(), pid, run.ID, "completed", len(created), ""); err != nil {
		return nil, err
	}
	run, _ = getSearchRun(ctx.AppDB(), pid, run.ID)
	ctx.EmitWithProject("prospecting.search.completed", pid, map[string]any{
		"run_id": run.ID, "profile_id": profileID, "query": query, "created": len(created), "duplicates": duplicates, "excluded": excluded,
	})
	return map[string]any{
		"run":        run,
		"candidates": created,
		"created":    len(created),
		"duplicates": duplicates,
		"excluded":   excluded,
	}, nil
}

func researchCandidate(ctx *sdk.AppCtx, id int64, question string) (map[string]any, error) {
	if ctx == nil || ctx.AppDB() == nil {
		return nil, errors.New("prospecting context unavailable")
	}
	pid := ctx.CurrentProject()
	candidate, err := getCandidate(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if candidate == nil {
		return nil, sql.ErrNoRows
	}
	profile, err := getProfile(ctx.AppDB(), pid, candidate.ProfileID)
	if err != nil || profile == nil {
		return nil, fmt.Errorf("load target profile: %w", err)
	}
	question = strings.TrimSpace(question)
	if question == "" {
		question = fmt.Sprintf("Research %s (%s) as a potential customer for the target profile %s. Find what the company does, its location, size signals, relevant buying signals, and people with these roles: %s.",
			candidate.CompanyName, candidate.CompanyDomain, profile.Name, strings.Join(profile.TargetTitles, ", "))
	}
	queries := []string{candidate.CompanyName + " " + candidate.CompanyDomain}
	if candidate.CompanyDomain != "" {
		queries = append(queries, "site:"+candidate.CompanyDomain+" about team careers contact")
	}
	if len(profile.TargetTitles) > 0 {
		queries = append(queries, candidate.CompanyName+" "+strings.Join(profile.TargetTitles, " OR "))
	}

	if _, err := ctx.AppDB().Exec(`UPDATE candidates SET status='researching',updated_at=? WHERE project_id=? AND id=?`, nowUTC(), pid, id); err != nil {
		return nil, err
	}
	var out webResearchOutput
	if err := ctx.PlatformAPI().CallAppResult("web", "web_research", map[string]any{
		"question":    question,
		"queries":     queries,
		"max_results": 8,
		"max_sources": 6,
		"snapshots":   false,
		"store":       true,
	}, &out); err != nil {
		_, _ = ctx.AppDB().Exec(`UPDATE candidates SET status='ready',updated_at=? WHERE project_id=? AND id=?`, nowUTC(), pid, id)
		return nil, fmt.Errorf("web research: %w", err)
	}

	artifacts := map[string]*int64{}
	for _, source := range out.Sources {
		if source.Artifact != nil && source.Artifact.ID > 0 {
			v := source.Artifact.ID
			artifacts[source.URL] = &v
			if source.FinalURL != "" {
				artifacts[source.FinalURL] = &v
			}
		}
	}
	for _, citation := range out.Citations {
		_ = addEvidence(ctx.AppDB(), pid, Evidence{
			CandidateID: id,
			SourceKind:  "web_research",
			Title:       citation.Title,
			URL:         citation.URL,
			Excerpt:     citation.Excerpt,
			ArtifactID:  artifacts[citation.URL],
			RetrievedAt: nowUTC(),
		})
	}
	evidence, err := listEvidence(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	summary := researchSummary(candidate, out)
	updated, err := setCandidateResearch(ctx.AppDB(), pid, candidate, summary, len(evidence))
	if err != nil {
		return nil, err
	}
	ctx.EmitWithProject("prospecting.candidate.researched", pid, candidateEvent(updated))
	return map[string]any{"candidate": updated, "evidence": evidence, "research": out}, nil
}

func researchSummary(candidate *Candidate, out webResearchOutput) string {
	parts := make([]string, 0, 4)
	for _, citation := range out.Citations {
		if excerpt := strings.TrimSpace(citation.Excerpt); excerpt != "" {
			parts = append(parts, excerpt)
		}
		if len(parts) == 4 {
			break
		}
	}
	if len(parts) == 0 && strings.TrimSpace(out.Answer) != "" {
		parts = append(parts, out.Answer)
	}
	if len(parts) == 0 {
		return candidate.Summary
	}
	return strings.Join(parts, " ")
}

func rescoreCandidate(db *sql.DB, pid string, id int64) (*Candidate, error) {
	c, err := getCandidate(db, pid, id)
	if err != nil || c == nil {
		return c, err
	}
	p, err := getProfile(db, pid, c.ProfileID)
	if err != nil || p == nil {
		return nil, err
	}
	count, _ := countEvidence(db, pid, id)
	fit, confidence, reasons := scoreCandidate(p, c, count)
	_, err = db.Exec(`UPDATE candidates SET fit_score=?,confidence_score=?,score_reasons_json=?,updated_at=? WHERE project_id=? AND id=?`,
		fit, confidence, mustJSON(reasons), nowUTC(), pid, id)
	if err != nil {
		return nil, err
	}
	return getCandidate(db, pid, id)
}

func scoreCandidate(profile *TargetProfile, candidate *Candidate, evidenceCount int) (int, int, []string) {
	text := strings.ToLower(strings.Join([]string{
		candidate.CompanyName, candidate.CompanyDomain, candidate.Website, candidate.Summary, candidate.JobTitle,
	}, " "))
	fit := 0
	reasons := []string{}
	if candidate.CompanyDomain != "" && candidate.Website != "" {
		fit += 20
		reasons = append(reasons, "+20 identifiable company website")
	}
	fit += criterionScore(text, profile.Industries, 25, 10, "industry", &reasons)
	fit += criterionScore(text, profile.Locations, 15, 5, "location", &reasons)
	fit += criterionScore(text, profile.Keywords, 20, 10, "keyword", &reasons)
	if len(profile.TargetTitles) == 0 {
		fit += 10
		reasons = append(reasons, "+10 no target-title constraint")
	} else if containsAny(strings.ToLower(candidate.JobTitle), profile.TargetTitles) {
		fit += 15
		reasons = append(reasons, "+15 target decision-maker role matches")
	} else if candidate.PersonDisplayName != "" {
		fit += 5
		reasons = append(reasons, "+5 person identified; target role not confirmed")
	}
	if candidate.Email != "" || candidate.Phone != "" {
		fit += 10
		reasons = append(reasons, "+10 professional contact channel available")
	}
	fit = clamp(fit, 0, 100)

	confidence := 0
	if candidate.CompanyDomain != "" {
		confidence += 15
	}
	if candidate.Website != "" {
		confidence += 15
	}
	if candidate.Summary != "" {
		confidence += 15
	}
	confidence += clamp(evidenceCount*10, 0, 30)
	if candidate.PersonDisplayName != "" {
		confidence += 10
	}
	if candidate.JobTitle != "" {
		confidence += 5
	}
	if candidate.Email != "" {
		confidence += 7
	}
	if candidate.Phone != "" {
		confidence += 3
	}
	confidence = clamp(confidence, 0, 100)
	reasons = append(reasons, fmt.Sprintf("confidence %d/100 from identity, evidence, and contact completeness", confidence))
	return fit, confidence, reasons
}

func criterionScore(text string, terms []string, matchScore, neutralScore int, label string, reasons *[]string) int {
	if len(terms) == 0 {
		*reasons = append(*reasons, fmt.Sprintf("+%d no %s constraint", neutralScore, label))
		return neutralScore
	}
	if containsAny(text, terms) {
		*reasons = append(*reasons, fmt.Sprintf("+%d %s evidence matches target", matchScore, label))
		return matchScore
	}
	return 0
}

func containsAny(text string, values []string) bool {
	text = strings.ToLower(text)
	for _, value := range values {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" && strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func acceptCandidate(ctx *sdk.AppCtx, id int64, listIDs any) (map[string]any, error) {
	if ctx == nil || ctx.AppDB() == nil {
		return nil, errors.New("prospecting context unavailable")
	}
	pid := ctx.CurrentProject()
	if existing, err := getHandoff(ctx.AppDB(), pid, id); err != nil {
		return nil, err
	} else if existing != nil {
		candidate, _ := getCandidate(ctx.AppDB(), pid, id)
		return map[string]any{"candidate": candidate, "handoff": existing, "idempotent": true}, nil
	}
	candidate, err := getCandidate(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if candidate == nil {
		return nil, sql.ErrNoRows
	}
	if candidate.Status == "rejected" {
		return nil, errors.New("rejected candidate must be restored before acceptance")
	}
	kind, value := "email", candidate.Email
	if value == "" {
		kind, value = "phone", candidate.Phone
	}
	if value == "" {
		return nil, errors.New("candidate needs a valid email or phone before CRM handoff")
	}
	profile, err := getProfile(ctx.AppDB(), pid, candidate.ProfileID)
	if err != nil || profile == nil {
		return nil, fmt.Errorf("load target profile: %w", err)
	}
	defaults := map[string]any{
		"first_name":   candidate.PersonFirstName,
		"last_name":    candidate.PersonLastName,
		"display_name": defaultString(candidate.PersonDisplayName, candidate.CompanyName),
		"company":      candidate.CompanyName,
		"job_title":    candidate.JobTitle,
		"tags":         []string{"prospecting", "prospecting:" + slugTag(profile.Name)},
	}
	input := map[string]any{
		"kind":     kind,
		"value":    value,
		"defaults": defaults,
		"source":   "prospecting",
	}
	if listIDs != nil {
		input["list_ids"] = listIDs
	}
	var crmOut crmUpsertOutput
	if err := ctx.PlatformAPI().CallAppResult("crm", "contacts_upsert_by_channel", input, &crmOut); err != nil {
		return nil, fmt.Errorf("CRM handoff: %w", err)
	}
	if crmOut.Contact.ID <= 0 {
		return nil, errors.New("CRM handoff returned no contact id")
	}
	warning := ""
	var ignored map[string]any
	activityBody := fmt.Sprintf("Accepted from Prospecting target profile %q. Fit %d/100, confidence %d/100. %s",
		profile.Name, candidate.FitScore, candidate.ConfidenceScore, truncate(candidate.Summary, 800))
	if err := ctx.PlatformAPI().CallAppResult("crm", "contacts_log_activity", map[string]any{
		"contact_id": crmOut.Contact.ID,
		"kind":       "note",
		"body":       strings.TrimSpace(activityBody),
		"source":     "prospecting",
	}, &ignored); err != nil {
		warning = "contact was created, but qualification activity could not be logged: " + err.Error()
	}
	handoff, err := saveHandoff(ctx.AppDB(), pid, candidate.ID, crmOut.Contact.ID, kind, value, crmOut.WasCreated, warning)
	if err != nil {
		return nil, err
	}
	candidate, _ = getCandidate(ctx.AppDB(), pid, id)
	ctx.EmitWithProject("prospecting.candidate.accepted", pid, map[string]any{
		"candidate_id": id, "crm_contact_id": crmOut.Contact.ID, "was_created": crmOut.WasCreated,
	})
	return map[string]any{"candidate": candidate, "handoff": handoff, "idempotent": false}, nil
}

func slugTag(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func candidateEvent(candidate *Candidate) map[string]any {
	if candidate == nil {
		return nil
	}
	return map[string]any{
		"id": candidate.ID, "profile_id": candidate.ProfileID, "company_name": candidate.CompanyName,
		"company_domain": candidate.CompanyDomain, "status": candidate.Status, "fit_score": candidate.FitScore,
		"confidence_score": candidate.ConfidenceScore,
	}
}

func sortCandidatesByFit(candidates []Candidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].FitScore == candidates[j].FitScore {
			return candidates[i].ConfidenceScore > candidates[j].ConfidenceScore
		}
		return candidates[i].FitScore > candidates[j].FitScore
	})
}
