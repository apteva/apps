package main

import (
	"errors"
	"fmt"
	"strings"
)

var storeScopeOrder = []string{
	"version", "localizations", "media", "review", "classification", "privacy", "distribution", "compliance",
}

type storeScopeSet map[string]bool

func allStoreScopeSet() storeScopeSet {
	out := storeScopeSet{}
	for _, scope := range storeScopeOrder {
		out[scope] = true
	}
	return out
}

func normalizeStoreScopes(input []string) (storeScopeSet, error) {
	if len(input) == 0 {
		return allStoreScopeSet(), nil
	}
	out := storeScopeSet{}
	for _, raw := range input {
		scope := normalizeStoreScope(raw)
		if scope == "" {
			return nil, fmt.Errorf("unsupported store scope %q", raw)
		}
		out[scope] = true
	}
	return out, nil
}

func normalizeStoreScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "version":
		return "version"
	case "listing", "localization", "localizations":
		return "localizations"
	case "media":
		return "media"
	case "review":
		return "review"
	case "classification":
		return "classification"
	case "privacy":
		return "privacy"
	case "compliance", "manual":
		return "compliance"
	case "distribution":
		return "distribution"
	default:
		return ""
	}
}

func (s storeScopeSet) has(scope string) bool { return s[scope] }

func (s storeScopeSet) any(scopes ...string) bool {
	for _, scope := range scopes {
		if s.has(scope) {
			return true
		}
	}
	return false
}

func findingBlocksStoreScope(finding StoreFinding, scope string) bool {
	if finding.Severity != "error" {
		return false
	}
	findingScope := normalizeStoreScope(finding.Scope)
	if findingScope == scope {
		return true
	}
	return findingScope == "version" && (scope == "localizations" || scope == "media" || scope == "review")
}

func (a *App) applyStoreConfigScoped(d *Deployment, build *Build, strict bool, request StoreApplyRequest) (*StoreApplyResult, error) {
	scopes, err := normalizeStoreScopes(request.Scopes)
	if err != nil {
		return nil, err
	}
	cfg, doc, err := a.mobileStoreConfig(d)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, errors.New("store listing is not configured")
	}
	if strings.TrimSpace(request.ReviewDemoPassword) != "" {
		doc.Review.DemoPassword = request.ReviewDemoPassword
		doc.Review.DemoPasswordSet = true
	}
	if fresh, _, observeErr := a.observeStoreConfig(d); observeErr == nil && fresh != nil {
		cfg = fresh
	}
	preflight := validateStoreDocument(a.dataDir, d, build, cfg, doc, strict)
	result := &StoreApplyResult{Status: "applied", AppliedScopes: []string{}, Blocked: []StoreApplyIssue{}, Failed: []StoreApplyIssue{}}
	if len(request.Scopes) == 0 && cfg.AppliedHash != "" && cfg.AppliedHash == cfg.DesiredHash && preflight.Ready && strings.TrimSpace(request.ReviewDemoPassword) == "" {
		result.Status = "no_op"
		result.Applied = true
		result.Config = cfg
		return result, nil
	}

	blocked := map[string]StoreApplyIssue{}
	for _, scope := range storeScopeOrder {
		if !scopes.has(scope) {
			continue
		}
		for _, finding := range preflight.Findings {
			if findingBlocksStoreScope(finding, scope) {
				blocked[scope] = StoreApplyIssue{Scope: scope, Code: finding.Code, Message: finding.Message}
				break
			}
		}
	}
	if len(blocked) > 0 && !request.AllowPartial {
		_ = dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, "blocked", "", mustJSON(preflight), "", "store preflight failed")
		return nil, fmt.Errorf("store preflight failed with %d error(s)", preflight.Errors)
	}

	_ = dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, "applying", "", mustJSON(preflight), "", "")
	for _, scope := range storeScopeOrder {
		if !scopes.has(scope) {
			continue
		}
		if issue, ok := blocked[scope]; ok {
			result.Blocked = append(result.Blocked, issue)
			continue
		}
		one := storeScopeSet{scope: true}
		var applyErr error
		switch d.TargetKind {
		case "ios":
			_, applyErr = a.applyAppleStoreConfigScopes(d, doc, one)
		case "android":
			_, applyErr = a.applyGoogleStoreConfigScopes(d, doc, one)
		default:
			applyErr = fmt.Errorf("unsupported store platform %q", d.TargetKind)
		}
		if applyErr != nil {
			result.Failed = append(result.Failed, StoreApplyIssue{Scope: scope, Message: applyErr.Error()})
			if !request.AllowPartial {
				_ = dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, "failed", "", mustJSON(preflight), "", applyErr.Error())
				return nil, applyErr
			}
			continue
		}
		result.AppliedScopes = append(result.AppliedScopes, scope)
		result.Applied = true
	}

	if strings.TrimSpace(request.ReviewDemoPassword) != "" && containsString(result.AppliedScopes, "review") {
		doc.Review.DemoPassword = ""
		doc.Review.DemoPasswordSet = true
		cfg, err = dbUpsertMobileStoreConfig(globalCtx.AppDB(), d, doc)
		if err != nil {
			return nil, err
		}
	}
	observed, observeErr := func() (map[string]any, error) {
		if d.TargetKind == "ios" {
			return a.observeAppleStoreConfig(d, doc)
		}
		return a.observeGoogleStoreConfig(d, doc)
	}()
	if observed == nil {
		observed = map[string]any{}
	}
	observed["last_apply"] = result
	observed["applied_at"] = nowUTC()
	observed["desired_hash"] = cfg.DesiredHash
	if observeErr != nil {
		result.Failed = append(result.Failed, StoreApplyIssue{Scope: "verification", Message: observeErr.Error()})
	}

	verifiedCfg := *cfg
	verifiedCfg.ObservedJSON = mustJSON(observed)
	verified := validateStoreDocument(a.dataDir, d, build, &verifiedCfg, doc, strict)
	allSelected := len(scopes) == len(storeScopeOrder)
	status, appliedHash, lastError := "partial", "", ""
	if len(result.Failed) > 0 {
		status = "failed"
		lastError = result.Failed[0].Message
	} else if len(result.Blocked) > 0 {
		status = "partial"
	} else if allSelected && verified.Ready {
		status = "applied"
		appliedHash = cfg.DesiredHash
	} else if len(result.AppliedScopes) == 0 {
		status = "blocked"
	}
	result.Status = status
	if err := dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, status, mustJSON(observed), mustJSON(verified), appliedHash, lastError); err != nil {
		return nil, err
	}
	result.Config, err = dbGetMobileStoreConfig(globalCtx.AppDB(), d.ID, d.EnvironmentID, d.TargetKind)
	return result, err
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
