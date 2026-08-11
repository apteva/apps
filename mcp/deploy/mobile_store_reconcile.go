package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var storeScopeOrder = []string{
	"version", "localizations", "media", "review", "classification", "privacy", "distribution", "testing", "compliance",
}

type storeScopeSet map[string]bool

type mediaKindSet map[string]bool

type storePreflightError struct {
	Preflight StorePreflight
}

func (e *storePreflightError) Error() string {
	return fmt.Sprintf("store preflight failed with %d error(s)", e.Preflight.Errors)
}

func newStorePreflightError(preflight StorePreflight) error {
	return &storePreflightError{Preflight: preflight}
}

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
	case "testing", "testers", "test_access":
		return "testing"
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

func (s mediaKindSet) has(kind string) bool { return len(s) == 0 || s[kind] }

func normalizeMediaKinds(platform string, input []string) (mediaKindSet, error) {
	out := mediaKindSet{}
	for _, raw := range input {
		kind := normalizeMediaKind(raw)
		if kind == "" || !mediaKindSupported(platform, kind) {
			return nil, fmt.Errorf("unsupported %s media kind %q", platform, raw)
		}
		out[kind] = true
	}
	return out, nil
}

func normalizeMediaKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "icon", "app_icon", "store_icon":
		return "icon"
	case "feature_graphic", "feature_graphics":
		return "feature_graphic"
	case "phone_screenshot", "phone_screenshots":
		return "phone_screenshot"
	case "tablet_screenshot", "tablet_screenshots":
		return "tablet_screenshot"
	case "tv_screenshot", "tv_screenshots":
		return "tv_screenshot"
	case "wear_screenshot", "wear_screenshots":
		return "wear_screenshot"
	case "automotive_screenshot", "automotive_screenshots":
		return "automotive_screenshot"
	case "app_preview", "app_previews":
		return "app_preview"
	case "review_attachment", "review_attachments":
		return "review_attachment"
	default:
		return ""
	}
}

func mediaKindSupported(platform, kind string) bool {
	if platform == "android" {
		return kind == "icon" || kind == "feature_graphic" || strings.HasSuffix(kind, "_screenshot")
	}
	if platform == "ios" {
		return kind == "phone_screenshot" || kind == "tablet_screenshot" || kind == "app_preview" || kind == "review_attachment"
	}
	return false
}

func mediaApplyCandidates(platform string, doc StoreDocument, preflight StorePreflight) []string {
	set := mediaKindSet{}
	for _, asset := range doc.Assets {
		if kind := normalizeMediaKind(asset.Kind); mediaKindSupported(platform, kind) {
			set[kind] = true
		}
	}
	for _, finding := range preflight.Findings {
		if normalizeStoreScope(finding.Scope) == "media" && finding.MediaKind != "" {
			set[finding.MediaKind] = true
		}
	}
	out := make([]string, 0, len(set))
	for kind := range set {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

func mediaKindBlockingIssue(preflight StorePreflight, kind string) *StoreApplyIssue {
	for _, finding := range preflight.Findings {
		if finding.Severity != "error" {
			continue
		}
		if strings.HasPrefix(finding.Code, "provider.") && finding.Automatable {
			continue
		}
		if normalizeStoreScope(finding.Scope) == "media" {
			if finding.MediaKind != "" && finding.MediaKind != kind {
				continue
			}
		} else if !findingBlocksStoreScope(finding, "media") {
			continue
		}
		return &StoreApplyIssue{
			Scope: "media", MediaKind: kind, AssetID: finding.AssetID, Locale: finding.Locale,
			Code: finding.Code, Message: finding.Message,
		}
	}
	return nil
}

func storeDocumentForMediaKind(doc StoreDocument, kind string) StoreDocument {
	filtered := doc
	filtered.Assets = make([]StoreAsset, 0, len(doc.Assets))
	for _, asset := range doc.Assets {
		if normalizeMediaKind(asset.Kind) == kind {
			filtered.Assets = append(filtered.Assets, asset)
		}
	}
	return filtered
}

func findingBlocksStoreScope(finding StoreFinding, scope string) bool {
	if finding.Severity != "error" {
		return false
	}
	if strings.HasPrefix(finding.Code, "provider.") && finding.Automatable {
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
	mediaKinds, err := normalizeMediaKinds(d.TargetKind, request.MediaKinds)
	if err != nil {
		return nil, err
	}
	if len(mediaKinds) > 0 {
		if len(request.Scopes) == 0 {
			scopes = storeScopeSet{"media": true}
		} else if !scopes.has("media") {
			return nil, errors.New("media_kinds requires the media scope")
		}
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
	appendProviderReadinessFindings(&preflight, d, cfg)
	result := &StoreApplyResult{
		Status: "applied", AppliedScopes: []string{}, AppliedAssets: []string{}, ScopeResults: []StoreScopeResult{},
		ProviderValidations: map[string]providerValidationEvidence{}, Blocked: []StoreApplyIssue{}, Failed: []StoreApplyIssue{},
	}
	if len(request.Scopes) == 0 && len(request.MediaKinds) == 0 && cfg.AppliedHash != "" && cfg.AppliedHash == cfg.DesiredHash && preflight.Ready && strings.TrimSpace(request.ReviewDemoPassword) == "" {
		result.Status = "no_op"
		result.Applied = true
		result.Config = cfg
		return result, nil
	}

	blocked := map[string]StoreApplyIssue{}
	partialMedia := scopes.has("media") && (request.AllowPartial || len(mediaKinds) > 0)
	for _, scope := range storeScopeOrder {
		if !scopes.has(scope) {
			continue
		}
		if scope == "media" && partialMedia {
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
		return nil, newStorePreflightError(preflight)
	}
	googleBatch := storeScopeSet{}
	if d.TargetKind == "android" {
		for _, scope := range []string{"localizations", "review", "privacy"} {
			if scopes.has(scope) {
				if _, isBlocked := blocked[scope]; !isBlocked {
					googleBatch[scope] = true
				}
			}
		}
		if scopes.has("media") && !partialMedia {
			if _, isBlocked := blocked["media"]; !isBlocked {
				googleBatch["media"] = true
			}
		}
	}
	googleBatchApplied := false

	_ = dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, "applying", "", mustJSON(preflight), "", "")
	for _, scope := range storeScopeOrder {
		if !scopes.has(scope) {
			continue
		}
		if issue, ok := blocked[scope]; ok {
			result.Blocked = append(result.Blocked, issue)
			addStoreScopeResult(result, scope, "blocked", issue.Message)
			continue
		}
		if googleBatch.has(scope) && len(googleBatch) > 0 {
			if googleBatchApplied {
				continue
			}
			googleBatchApplied = true
			providerResult, applyErr := a.applyGoogleStoreConfigScopesWithMediaKinds(d, doc, googleBatch, nil)
			if applyErr != nil {
				for _, batchedScope := range storeScopeOrder {
					if googleBatch[batchedScope] {
						result.Failed = append(result.Failed, StoreApplyIssue{Scope: batchedScope, Message: applyErr.Error()})
						addStoreScopeResult(result, batchedScope, "failed", applyErr.Error())
					}
				}
				if !request.AllowPartial {
					_ = dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, "failed", "", mustJSON(preflight), "", applyErr.Error())
					return nil, applyErr
				}
				continue
			}
			mergeStoreProviderValidations(result.ProviderValidations, providerResult)
			var privacyErr error
			if googleBatch["privacy"] && strings.TrimSpace(doc.Privacy.DataSafetyCSV) != "" {
				bound, boundErr := boundIntegration("play_store")
				target, targetErr := parseMobileTargetConfig(d.TargetConfigJSON)
				switch {
				case boundErr != nil:
					privacyErr = boundErr
				case targetErr != nil:
					privacyErr = targetErr
				default:
					var evidence providerValidationEvidence
					evidence, privacyErr = applyGoogleDataSafety(bound, target.PackageName, doc)
					if privacyErr == nil && evidence.Status != "" {
						result.ProviderValidations["google_data_safety"] = evidence
					}
				}
			}
			for _, batchedScope := range storeScopeOrder {
				if googleBatch[batchedScope] {
					if batchedScope == "privacy" && privacyErr != nil {
						result.Failed = append(result.Failed, StoreApplyIssue{Scope: batchedScope, Message: privacyErr.Error()})
						addStoreScopeResult(result, batchedScope, "failed", "The Play edit committed, but Data Safety failed: "+privacyErr.Error())
						continue
					}
					result.AppliedScopes = append(result.AppliedScopes, batchedScope)
					addStoreScopeResult(result, batchedScope, "applied", "Committed in one validated Google Play edit.")
				}
			}
			result.Applied = true
			if privacyErr != nil && !request.AllowPartial {
				_ = dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, "failed", "", mustJSON(preflight), "", privacyErr.Error())
				return nil, privacyErr
			}
			continue
		}
		if scope == "media" && partialMedia {
			candidates := make([]string, 0, len(mediaKinds))
			if len(mediaKinds) == 0 {
				candidates = mediaApplyCandidates(d.TargetKind, doc, preflight)
			} else {
				for kind := range mediaKinds {
					candidates = append(candidates, kind)
				}
				sort.Strings(candidates)
			}
			blockedKinds := map[string]StoreApplyIssue{}
			for _, kind := range candidates {
				if issue := mediaKindBlockingIssue(preflight, kind); issue != nil {
					blockedKinds[kind] = *issue
					continue
				}
				kindDoc := storeDocumentForMediaKind(doc, kind)
				if len(kindDoc.Assets) == 0 {
					blockedKinds[kind] = StoreApplyIssue{
						Scope: "media", MediaKind: kind, Code: "media.kind_empty",
						Message: fmt.Sprintf("No %s assets are configured.", strings.ReplaceAll(kind, "_", " ")),
					}
				}
			}
			if len(blockedKinds) > 0 && !request.AllowPartial {
				_ = dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, "blocked", "", mustJSON(preflight), "", "store media preflight failed")
				return nil, newStorePreflightError(preflight)
			}
			for _, kind := range candidates {
				if issue, blocked := blockedKinds[kind]; blocked {
					result.Blocked = append(result.Blocked, issue)
					continue
				}
				kindDoc := storeDocumentForMediaKind(doc, kind)
				selected := mediaKindSet{kind: true}
				var applyErr error
				switch d.TargetKind {
				case "ios":
					_, applyErr = a.applyAppleStoreConfigScopesWithMediaKinds(d, kindDoc, storeScopeSet{"media": true}, selected)
				case "android":
					_, applyErr = a.applyGoogleStoreConfigScopesWithMediaKinds(d, kindDoc, storeScopeSet{"media": true}, selected)
				default:
					applyErr = fmt.Errorf("unsupported store platform %q", d.TargetKind)
				}
				if applyErr != nil {
					result.Failed = append(result.Failed, StoreApplyIssue{Scope: "media", MediaKind: kind, Message: applyErr.Error()})
					if !request.AllowPartial {
						_ = dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, "failed", "", mustJSON(preflight), "", applyErr.Error())
						return nil, applyErr
					}
					continue
				}
				result.AppliedAssets = append(result.AppliedAssets, kind)
			}
			if len(result.AppliedAssets) > 0 {
				result.AppliedScopes = append(result.AppliedScopes, "media")
				addStoreScopeResult(result, "media", "applied", "Applied ready media kinds independently.")
				result.Applied = true
			} else if len(blockedKinds) > 0 {
				addStoreScopeResult(result, "media", "blocked", "No selected media kind was ready to apply.")
			}
			continue
		}
		if d.TargetKind == "android" {
			switch scope {
			case "version":
				addStoreScopeResult(result, scope, "verified", "Version compatibility is validated from the build artifact and release track.")
				continue
			case "classification", "distribution", "compliance":
				addStoreScopeResult(result, scope, "verified", "This scope is provider-read or manually confirmed; no Google edit was committed.")
				continue
			case "testing":
				if len(doc.Testing.Channels) == 0 {
					addStoreScopeResult(result, scope, "not_configured", "No Google Play testing audience is configured.")
					continue
				}
			}
		}
		one := storeScopeSet{scope: true}
		var applyErr error
		if scope == "testing" {
			applyErr = a.applyDesiredTesting(d, doc)
		} else {
			switch d.TargetKind {
			case "ios":
				_, applyErr = a.applyAppleStoreConfigScopes(d, doc, one)
			case "android":
				_, applyErr = a.applyGoogleStoreConfigScopes(d, doc, one)
			default:
				applyErr = fmt.Errorf("unsupported store platform %q", d.TargetKind)
			}
		}
		if applyErr != nil {
			result.Failed = append(result.Failed, StoreApplyIssue{Scope: scope, Message: applyErr.Error()})
			addStoreScopeResult(result, scope, "failed", applyErr.Error())
			if !request.AllowPartial {
				_ = dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, "failed", "", mustJSON(preflight), "", applyErr.Error())
				return nil, applyErr
			}
			continue
		}
		result.AppliedScopes = append(result.AppliedScopes, scope)
		addStoreScopeResult(result, scope, "applied", "Provider operation completed.")
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
		return a.observeGoogleStoreConfig(d, doc, cfg)
	}()
	if observed == nil {
		observed = map[string]any{}
	}
	preserveStoreObservationState(observed, cfg.ObservedJSON)
	mergeObservedProviderValidations(observed, result.ProviderValidations)
	if d.TargetKind == "android" {
		refreshGoogleEvidenceReadiness(observed, doc)
	}
	if len(doc.Testing.Channels) > 0 {
		testing, testingErr := a.observeDesiredTesting(d, doc)
		if testingErr != nil {
			if observeErr == nil {
				observeErr = testingErr
			}
		} else {
			observed["testing"] = testing
		}
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
	appendProviderReadinessFindings(&verified, d, &verifiedCfg)
	allSelected := len(scopes) == len(storeScopeOrder) && len(mediaKinds) == 0
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
		status = "no_op"
		for _, scopeResult := range result.ScopeResults {
			if scopeResult.Status == "blocked" || scopeResult.Status == "failed" {
				status = "blocked"
				break
			}
		}
	}
	result.Status = status
	dbStatus := status
	if dbStatus == "no_op" {
		dbStatus = "partial"
		if verified.Ready {
			dbStatus = "ready"
		}
	}
	if err := dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, dbStatus, mustJSON(observed), mustJSON(verified), appliedHash, lastError); err != nil {
		return nil, err
	}
	result.Config, err = dbGetMobileStoreConfig(globalCtx.AppDB(), d.ID, d.EnvironmentID, d.TargetKind)
	return result, err
}

func addStoreScopeResult(result *StoreApplyResult, scope, status, message string) {
	if result == nil {
		return
	}
	for i := range result.ScopeResults {
		if result.ScopeResults[i].Scope == scope {
			result.ScopeResults[i] = StoreScopeResult{Scope: scope, Status: status, Message: message}
			return
		}
	}
	result.ScopeResults = append(result.ScopeResults, StoreScopeResult{Scope: scope, Status: status, Message: message})
}

func mergeStoreProviderValidations(target map[string]providerValidationEvidence, providerResult map[string]any) {
	if target == nil || providerResult == nil {
		return
	}
	if values, ok := providerResult["provider_validations"].(map[string]providerValidationEvidence); ok {
		for key, value := range values {
			target[key] = value
		}
	}
}

func mergeObservedProviderValidations(observed map[string]any, additions map[string]providerValidationEvidence) {
	if observed == nil || len(additions) == 0 {
		return
	}
	values, _ := observed["provider_validations"].(map[string]any)
	if values == nil {
		values = map[string]any{}
	}
	for key, value := range additions {
		values[key] = value
	}
	observed["provider_validations"] = values
}

func refreshGoogleEvidenceReadiness(observed map[string]any, doc StoreDocument) {
	readiness, _ := observed["readiness"].(map[string]any)
	if readiness == nil {
		readiness = map[string]any{}
		observed["readiness"] = readiness
	}
	validations, _ := observed["provider_validations"].(map[string]any)
	accepted := false
	if raw, ok := validations["google_data_safety"]; ok {
		body, _ := json.Marshal(raw)
		var evidence providerValidationEvidence
		accepted = json.Unmarshal(body, &evidence) == nil && evidence.Status == "accepted" && evidence.VersionName == doc.VersionName
	}
	if doc.Privacy.ManualAttestations["google_data_safety_published"] || accepted {
		readiness["privacy"] = readinessCheck(true, "provider_acknowledgement", "Google acknowledged the Data Safety declaration, or publication was manually confirmed.")
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
