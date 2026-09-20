package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type service struct {
	ctx *sdk.AppCtx
	db  store
}

var errSealed = errors.New("sealed packs are immutable: fork it into a draft, or seal a new version")

const (
	JudgePromptVersion = "goal-evidence-v1"
	JudgeRubricVersion = "required-goals-v1"
	judgeDisabledValue = "disabled"
	// automaticEnvironmentID is a versioned, Bench-owned Environment
	// definition. A new id must be used if its isolation contract changes so
	// sealed packs keep naming the exact world they were authored against.
	automaticEnvironmentID = "env_bench_isolated_v1"
)

// ---- pack authoring ----

func (s *service) savePack(input *Pack, creating bool) (*Pack, error) {
	if strings.TrimSpace(input.Name) == "" {
		return nil, errors.New("name is required")
	}
	now := time.Now().UTC()
	if creating {
		judgeModel := strings.TrimSpace(input.JudgeModel)
		if strings.EqualFold(judgeModel, judgeDisabledValue) {
			judgeModel = ""
		}
		pack := &Pack{
			ID: newID("pack"), Name: input.Name, Description: input.Description,
			Category: normalizeTaxonomyValue(input.Category),
			State:    PackStateDraft, Scenarios: normalizeScenarioTaxonomy(input.Scenarios),
			ProfileDigest: input.ProfileDigest, JudgeModel: judgeModel, Revision: 1,
			CreatedAt: now, UpdatedAt: now,
		}
		if pack.ProfileDigest != "" {
			profile, err := s.db.getProfileByDigest(pack.ProfileDigest)
			if err != nil {
				return nil, err
			}
			if profile == nil || profile.State != ProfileStateSealed {
				return nil, errors.New("no sealed scoring profile with that digest")
			}
		}
		if pack.Scenarios == nil {
			pack.Scenarios = []Scenario{}
		}
		if err := s.db.savePack(pack); err != nil {
			return nil, err
		}
		return pack, nil
	}

	existing, err := s.requireDraft(input.ID)
	if err != nil {
		return nil, err
	}
	existing.Name, existing.Description, existing.UpdatedAt = input.Name, input.Description, now
	// Category did not exist before v0.6.0. Preserve it when an older client
	// updates another field without sending category at all.
	if input.Category != "" || existing.Category == "" {
		existing.Category = normalizeTaxonomyValue(input.Category)
	}
	if input.Scenarios != nil {
		existing.Scenarios = normalizeScenarioTaxonomy(input.Scenarios)
	}
	if input.ProfileDigest != "" {
		if p, err := s.db.getProfileByDigest(input.ProfileDigest); err != nil {
			return nil, err
		} else if p == nil {
			return nil, errors.New("no sealed scoring profile with that digest")
		}
		existing.ProfileDigest = input.ProfileDigest
	}
	// JudgeModel was added in v0.7. Preserve it when an older client updates a
	// different field. The literal "disabled" explicitly clears the judge.
	if value := strings.TrimSpace(input.JudgeModel); value != "" {
		if strings.EqualFold(value, judgeDisabledValue) {
			existing.JudgeModel = ""
		} else {
			existing.JudgeModel = value
		}
	}
	if err := s.db.savePack(existing); err != nil {
		return nil, err
	}
	return s.db.getPack(existing.ID)
}

func (s *service) requireDraft(id string) (*Pack, error) {
	pack, err := s.db.getPack(id)
	if err != nil {
		return nil, err
	}
	if pack == nil {
		return nil, errors.New("pack not found")
	}
	if pack.State != PackStateDraft {
		return nil, errSealed
	}
	return pack, nil
}

func (s *service) putScenario(packID string, scenario Scenario) (*Pack, error) {
	pack, err := s.requireDraft(packID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(scenario.Name) == "" {
		return nil, errors.New("scenario name is required")
	}
	if strings.TrimSpace(scenario.Prompt) == "" {
		return nil, errors.New("scenario prompt is required")
	}
	if scenario.ID == "" {
		scenario.ID = slugify(scenario.Name)
	}
	if scenario.Weight <= 0 {
		scenario.Weight = 1
	}
	if scenario.Checks == nil {
		scenario.Checks = []Check{}
	}
	tagsProvided := scenario.Tags != nil
	scenario.Tags = normalizeTaxonomyValues(scenario.Tags)
	replaced := false
	for i := range pack.Scenarios {
		if pack.Scenarios[i].ID == scenario.ID {
			// Tags are new in v0.6.0. An older client replacing a scenario does
			// not know to echo them; an explicit [] still clears them.
			if !tagsProvided {
				scenario.Tags = pack.Scenarios[i].Tags
			}
			pack.Scenarios[i], replaced = scenario, true
			break
		}
	}
	if !replaced {
		pack.Scenarios = append(pack.Scenarios, scenario)
	}
	pack.UpdatedAt = time.Now().UTC()
	if err := s.db.savePack(pack); err != nil {
		return nil, err
	}
	return s.db.getPack(pack.ID)
}

func (s *service) deleteScenario(packID, scenarioID string) (*Pack, error) {
	pack, err := s.requireDraft(packID)
	if err != nil {
		return nil, err
	}
	kept := make([]Scenario, 0, len(pack.Scenarios))
	for _, scenario := range pack.Scenarios {
		if scenario.ID != scenarioID {
			kept = append(kept, scenario)
		}
	}
	if len(kept) == len(pack.Scenarios) {
		return nil, errors.New("scenario not found")
	}
	pack.Scenarios, pack.UpdatedAt = kept, time.Now().UTC()
	if err := s.db.savePack(pack); err != nil {
		return nil, err
	}
	return s.db.getPack(pack.ID)
}

func (s *service) deletePack(id string) error {
	pack, err := s.db.getPack(id)
	if err != nil {
		return err
	}
	if pack == nil {
		return errors.New("pack not found")
	}
	count, err := s.db.countRunsForPack(id)
	if err != nil {
		return err
	}
	// A sealed pack with runs is the definition those scores refer to.
	// Deleting it would orphan every number ever published against it.
	if count > 0 {
		return fmt.Errorf("pack has %d run(s); its definition must outlive them", count)
	}
	return s.db.deletePack(id)
}

// seal copies a draft into a new immutable pack row. The draft stays editable:
// the next seal mints another version rather than moving this one.
func (s *service) seal(draftID, version string) (*Pack, error) {
	draft, err := s.requireDraft(draftID)
	if err != nil {
		return nil, err
	}
	if len(draft.Scenarios) == 0 {
		return nil, errors.New("cannot seal a pack with no scenarios")
	}
	for _, scenario := range draft.Scenarios {
		if err := validateScenarioContent(scenario); err != nil {
			return nil, err
		}
	}
	judgeModel, err := s.resolveJudgeModel(draft.JudgeModel)
	if err != nil {
		return nil, err
	}
	if judgeModel != "" {
		for _, scenario := range draft.Scenarios {
			if len(scenario.Goals) == 0 {
				return nil, fmt.Errorf("scenario %q needs at least one goal when judge_model is set", scenario.ID)
			}
		}
	}

	if strings.TrimSpace(version) == "" {
		sealed, err := s.db.sealedVersions(draft.ID)
		if err != nil {
			return nil, err
		}
		version = fmt.Sprintf("%d.0.0", sealed+1)
	}

	// Pin the scoring contract at seal time: a sealed pack's results must keep
	// meaning the same thing even if the default profile later changes.
	profileDigest := draft.ProfileDigest
	if profileDigest == "" {
		if judgeModel != "" {
			profile := judgedV1()
			if profile.Digest, err = profileDigestFor(profile); err != nil {
				return nil, err
			}
			profileDigest = profile.Digest
		} else if profileDigest, err = s.defaultProfileDigest(); err != nil {
			return nil, err
		}
	}
	profile, err := s.resolveProfile(profileDigest)
	if err != nil {
		return nil, err
	}
	if judgeModel != "" && !profileUsesJudge(profile) {
		return nil, errors.New("judged packs require a scoring profile with a judge_score quality component")
	}
	scenarios, err := s.pinAutomaticEnvironment(draft.Scenarios)
	if err != nil {
		return nil, err
	}
	for _, scenario := range scenarios {
		if err := validateScenario(scenario); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	pack := &Pack{
		ID: newID("pack"), Name: draft.Name, Description: draft.Description, Category: draft.Category,
		State: PackStateSealed, Version: version, ScoringVersion: profile.Version,
		ProfileDigest: profile.Digest, JudgeModel: judgeModel,
		SourcePackID: draft.ID, Scenarios: normalizeScenarios(scenarios),
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	digest, err := packDigest(pack)
	if err != nil {
		return nil, err
	}
	pack.Digest = digest

	// An identical definition is the same benchmark. Returning the existing
	// sealed row keeps one digest to one pack, so results stay joinable.
	if existing, err := s.db.getPackByDigest(digest); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}

	if err := s.db.savePack(pack); err != nil {
		return nil, err
	}
	s.ctx.Emit("bench.pack.sealed", map[string]any{
		"pack_id": pack.ID, "name": pack.Name, "version": pack.Version,
		"digest": pack.Digest, "scoring_version": pack.ScoringVersion,
		"category": pack.Category, "scenarios": len(pack.Scenarios),
	})
	return pack, nil
}

func (s *service) fork(sourceID, name string) (*Pack, error) {
	source, err := s.db.getPack(sourceID)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, errors.New("pack not found")
	}
	if strings.TrimSpace(name) == "" {
		name = source.Name + " (draft)"
	}
	now := time.Now().UTC()
	draft := &Pack{
		ID: newID("pack"), Name: name, Description: source.Description, Category: source.Category,
		State: PackStateDraft, Scenarios: source.Scenarios, ProfileDigest: source.ProfileDigest,
		JudgeModel: source.JudgeModel, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.savePack(draft); err != nil {
		return nil, err
	}
	return draft, nil
}

func validateScenario(scenario Scenario) error {
	if err := validateScenarioContent(scenario); err != nil {
		return err
	}
	if scenario.EnvironmentID == "" && scenario.SnapshotID == "" {
		return fmt.Errorf("scenario %q pins no environment or snapshot, so its world is not reproducible", scenario.ID)
	}
	return nil
}

func validateScenarioContent(scenario Scenario) error {
	if scenario.ID == "" || scenario.Name == "" {
		return errors.New("every scenario needs an id and a name")
	}
	if strings.TrimSpace(scenario.Prompt) == "" {
		return fmt.Errorf("scenario %q has no prompt", scenario.ID)
	}
	budget := scenario.Budget
	if budget.DurationMS <= 0 || budget.Turns <= 0 || (budget.TokensTotal <= 0 && budget.CostUSD <= 0) {
		return fmt.Errorf("scenario %q needs duration, turn, and token or cost budgets to be scoreable", scenario.ID)
	}
	return nil
}

// pinAutomaticEnvironment makes the common no-fixture case zero-config while
// preserving Bench's sealed-world invariant. Environments stores this durable,
// stopped definition; Evals creates a fresh isolated run from it for every
// trial and still keeps transient agents out of the project's Agents list.
func (s *service) pinAutomaticEnvironment(input []Scenario) ([]Scenario, error) {
	scenarios := append([]Scenario(nil), input...)
	needsDefault := false
	for _, scenario := range scenarios {
		if scenario.EnvironmentID == "" && scenario.SnapshotID == "" {
			needsDefault = true
			break
		}
	}
	if !needsDefault {
		return scenarios, nil
	}
	request := map[string]any{
		"id":            automaticEnvironmentID,
		"name":          "Bench automatic isolation",
		"description":   "Bench-managed isolated world for scenarios without apps, seeds, fixtures, or an explicit Environment.",
		"desired_state": "stopped",
		"spec": map[string]any{
			"version":          1,
			"ttl_seconds":      86400,
			"network_mode":     "block",
			"integration_mode": "mock",
		},
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := s.ctx.PlatformAPI().CallAppResult("environments", "environment_create", request, &created); err != nil {
		return nil, fmt.Errorf("create automatic isolated environment: %w", err)
	}
	if created.ID == "" {
		return nil, errors.New("create automatic isolated environment: Environments returned no id")
	}
	for i := range scenarios {
		if scenarios[i].EnvironmentID == "" && scenarios[i].SnapshotID == "" {
			scenarios[i].EnvironmentID = created.ID
		}
	}
	return scenarios, nil
}

// normalizeScenarios sorts scenarios and fills defaults so that two packs with
// the same content always hash the same regardless of authoring order.
func normalizeScenarios(scenarios []Scenario) []Scenario {
	out := make([]Scenario, len(scenarios))
	copy(out, scenarios)
	for i := range out {
		if out[i].Weight <= 0 {
			out[i].Weight = 1
		}
		if out[i].Checks == nil {
			out[i].Checks = []Check{}
		}
		if out[i].Goals == nil {
			out[i].Goals = []string{}
		}
		out[i].Tags = normalizeTaxonomyValues(out[i].Tags)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

// packDigest hashes only the definition — never ids, timestamps or revisions —
// so the digest identifies the benchmark rather than the row holding it.
func packDigest(pack *Pack) (string, error) {
	return canonicalDigest(struct {
		Name           string     `json:"name"`
		Description    string     `json:"description"`
		Category       string     `json:"category,omitempty"`
		ScoringVersion string     `json:"scoring_version"`
		JudgeModel     string     `json:"judge_model,omitempty"`
		Scenarios      []Scenario `json:"scenarios"`
	}{pack.Name, pack.Description, pack.Category, pack.ScoringVersion, pack.JudgeModel, normalizeScenarios(pack.Scenarios)})
}

func profileDigestFor(profile *Profile) (string, error) {
	if profile.Digest != "" {
		return profile.Digest, nil
	}
	return profileDigest(profile)
}

func profileUsesJudge(profile *Profile) bool {
	if profile == nil {
		return false
	}
	for _, component := range profile.Components {
		if component.Kind == KindQuality && component.Metric == "judge_score" {
			return true
		}
	}
	return false
}

// resolveJudgeModel canonicalizes a pack's selection against the same live
// catalog Evals uses. A missing model fails sealing before any benchmark is
// queued; no silent provider/model substitution is allowed.
func (s *service) resolveJudgeModel(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || strings.EqualFold(requested, judgeDisabledValue) {
		return "", nil
	}
	var catalog struct {
		Models []struct {
			ModelID      string `json:"model_id"`
			GatewayModel string `json:"gateway_model"`
		} `json:"models"`
	}
	if err := s.ctx.PlatformAPI().CallAppResult("evals", "eval_catalog", map[string]any{}, &catalog); err != nil {
		return "", fmt.Errorf("resolve judge_model %q: %w", requested, err)
	}
	matches := map[string]struct{}{}
	for _, model := range catalog.Models {
		canonical := strings.TrimSpace(model.GatewayModel)
		if canonical == "" {
			continue
		}
		if requested == canonical {
			return canonical, nil
		}
		if requested == strings.TrimSpace(model.ModelID) {
			matches[canonical] = struct{}{}
		}
	}
	if len(matches) == 1 {
		for canonical := range matches {
			return canonical, nil
		}
	}
	if len(matches) > 1 {
		values := make([]string, 0, len(matches))
		for canonical := range matches {
			values = append(values, canonical)
		}
		sort.Strings(values)
		return "", fmt.Errorf("judge_model %q is ambiguous; choose one of: %s", requested, strings.Join(values, ", "))
	}
	return "", fmt.Errorf("judge_model %q is not available in eval_catalog.models[].gateway_model", requested)
}

func scenarioDigests(scenarios []Scenario) map[string]string {
	digests := map[string]string{}
	for _, scenario := range scenarios {
		if digest, err := canonicalDigest(scenario); err == nil {
			digests[scenario.ID] = digest
		}
	}
	return digests
}

func slugify(value string) string {
	var b strings.Builder
	previousDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			previousDash = false
		default:
			if !previousDash && b.Len() > 0 {
				b.WriteByte('-')
				previousDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func normalizeTaxonomyValue(value string) string { return slugify(value) }

func normalizeTaxonomyValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := normalizeTaxonomyValue(value)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeScenarioTaxonomy(scenarios []Scenario) []Scenario {
	if scenarios == nil {
		return nil
	}
	out := make([]Scenario, len(scenarios))
	copy(out, scenarios)
	for i := range out {
		out[i].Tags = normalizeTaxonomyValues(out[i].Tags)
	}
	return out
}

// ---- scoring profiles ----

// ensureBuiltinProfiles installs the shipped contracts and stamps any history
// written before profiles existed. Built-ins are re-derived on every mount so a
// corrected default reaches existing installs; their digests are stable, so
// re-deriving never orphans results.
func (s *service) ensureBuiltinProfiles() error {
	var verified string
	for _, p := range builtinProfiles() {
		digest, err := profileDigest(p)
		if err != nil {
			return err
		}
		p.Digest = digest
		if p.Version == ScoringVersion {
			verified = digest
		}
		existing, err := s.db.getProfileByDigest(digest)
		if err != nil {
			return err
		}
		if existing != nil {
			continue
		}
		now := time.Now().UTC()
		p.ID = "profile_builtin_" + slugify(p.Name)
		p.Revision, p.CreatedAt, p.UpdatedAt = 1, now, now
		if err := s.db.saveProfile(p); err != nil {
			return err
		}
	}
	if verified == "" {
		return errors.New("built-in verified profile missing a digest")
	}
	n, err := s.db.backfillRunProfiles(verified)
	if err != nil {
		return err
	}
	if n > 0 {
		s.ctx.Logger().Info("bench: stamped pre-profile history", "runs", n, "profile", short(verified))
	}
	return nil
}

func (s *service) defaultProfileDigest() (string, error) {
	p := verifiedV1()
	return profileDigest(p)
}

// resolveProfile returns the contract a pack is scored under, falling back to
// verified-v1 for packs sealed before profiles existed.
func (s *service) resolveProfile(digest string) (*Profile, error) {
	if digest != "" {
		if p, err := s.db.getProfileByDigest(digest); err != nil {
			return nil, err
		} else if p != nil {
			return p, nil
		}
	}
	p := verifiedV1()
	d, err := profileDigest(p)
	if err != nil {
		return nil, err
	}
	p.Digest = d
	return p, nil
}

func (s *service) saveProfile(input *Profile, creating bool) (*Profile, error) {
	now := time.Now().UTC()
	if creating {
		p := &Profile{
			ID: newID("profile"), Name: input.Name, Description: input.Description,
			State: ProfileStateDraft, OnFailure: orKey(input.OnFailure, OnFailureZero),
			Components: input.Components, Revision: 1, CreatedAt: now, UpdatedAt: now,
		}
		if p.Components == nil {
			p.Components = []ProfileComponent{}
		}
		if strings.TrimSpace(p.Name) == "" {
			return nil, errors.New("name is required")
		}
		if err := s.db.saveProfile(p); err != nil {
			return nil, err
		}
		return p, nil
	}
	existing, err := s.requireDraftProfile(input.ID)
	if err != nil {
		return nil, err
	}
	existing.Name, existing.Description, existing.UpdatedAt = input.Name, input.Description, now
	if input.OnFailure != "" {
		existing.OnFailure = input.OnFailure
	}
	if input.Components != nil {
		existing.Components = input.Components
	}
	if err := s.db.saveProfile(existing); err != nil {
		return nil, err
	}
	return s.db.getProfile(existing.ID)
}

func (s *service) requireDraftProfile(id string) (*Profile, error) {
	p, err := s.db.getProfile(id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("profile not found")
	}
	if p.Builtin {
		return nil, errors.New("built-in profiles are immutable: fork one to change it")
	}
	if p.State != ProfileStateDraft {
		return nil, errors.New("sealed profiles are immutable: fork it, or seal a new version")
	}
	return p, nil
}

// sealProfile freezes a draft into a content-hashed contract. Identical
// contracts resolve to one row, so the same scoring means the same digest.
func (s *service) sealProfile(draftID, version string) (*Profile, error) {
	draft, err := s.requireDraftProfile(draftID)
	if err != nil {
		return nil, err
	}
	sealed := &Profile{
		ID: newID("profile"), Name: draft.Name, Description: draft.Description,
		State: ProfileStateSealed, SourceID: draft.ID, OnFailure: draft.OnFailure,
		Components: draft.Components, Revision: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := validateProfile(sealed); err != nil {
		return nil, err
	}
	if strings.TrimSpace(version) == "" {
		version = time.Now().UTC().Format("2006-01") + "." + slugify(draft.Name)
	}
	sealed.Version = version
	digest, err := profileDigest(sealed)
	if err != nil {
		return nil, err
	}
	sealed.Digest = digest
	if existing, err := s.db.getProfileByDigest(digest); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}
	if err := s.db.saveProfile(sealed); err != nil {
		return nil, err
	}
	s.ctx.Emit("bench.profile.sealed", map[string]any{
		"profile_id": sealed.ID, "name": sealed.Name, "version": sealed.Version,
		"digest": sealed.Digest, "max_score": sealed.maxScore(),
	})
	return sealed, nil
}

func (s *service) forkProfile(sourceID, name string) (*Profile, error) {
	src, err := s.db.getProfile(sourceID)
	if err != nil {
		return nil, err
	}
	if src == nil {
		return nil, errors.New("profile not found")
	}
	if strings.TrimSpace(name) == "" {
		name = src.Name + " (draft)"
	}
	now := time.Now().UTC()
	draft := &Profile{
		ID: newID("profile"), Name: name, Description: src.Description,
		State: ProfileStateDraft, OnFailure: src.OnFailure, Components: src.Components,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.saveProfile(draft); err != nil {
		return nil, err
	}
	return draft, nil
}

func (s *service) deleteProfile(id string) error {
	p, err := s.db.getProfile(id)
	if err != nil {
		return err
	}
	if p == nil {
		return errors.New("profile not found")
	}
	if p.Builtin {
		return errors.New("built-in profiles cannot be deleted")
	}
	n, err := s.db.countPacksUsingProfile(p.Digest)
	if err != nil {
		return err
	}
	// A profile with packs is the contract their scores refer to.
	if n > 0 {
		return fmt.Errorf("%d pack(s) are scored under this profile; its definition must outlive them", n)
	}
	return s.db.deleteProfile(id)
}

// previewProfile rescores a pack's existing admitted results under a candidate
// contract, so the effect of a change is visible before it is sealed.
func (s *service) previewProfile(packDigest string, candidate *Profile) (map[string]any, error) {
	pack, err := s.db.getPackByDigest(packDigest)
	if err != nil {
		return nil, err
	}
	if pack == nil {
		return nil, errors.New("no sealed pack with that digest")
	}
	if err := validateProfile(candidate); err != nil {
		return nil, err
	}
	results, err := s.db.listAdmittedResults(pack.ProfileDigest, "")
	if err != nil {
		return nil, err
	}
	type row struct {
		Label    string  `json:"label"`
		Runs     int     `json:"runs"`
		Current  float64 `json:"current_average"`
		Proposed float64 `json:"proposed_average"`
		Delta    float64 `json:"delta"`
	}
	acc := map[string]*row{}
	order := []string{}
	for _, r := range results {
		if r.PackDigest != packDigest {
			continue
		}
		scenario := pack.scenario(r.ScenarioID)
		if scenario == nil {
			continue
		}
		identity := r.Target.key()
		if acc[identity] == nil {
			acc[identity] = &row{Label: r.Target.label()}
			order = append(order, identity)
		}
		a := acc[identity]
		a.Runs++
		a.Current += r.Score.Score
		a.Proposed += scoreWithProfile(r.Passed, r.Metrics, scenario.Budget, candidate).Score
	}
	rows := make([]row, 0, len(acc))
	for _, k := range order {
		a := acc[k]
		if a.Runs == 0 {
			continue
		}
		a.Current = round1(a.Current / float64(a.Runs))
		a.Proposed = round1(a.Proposed / float64(a.Runs))
		a.Delta = round1(a.Proposed - a.Current)
		rows = append(rows, *a)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Proposed > rows[j].Proposed })
	return map[string]any{
		"pack":      map[string]any{"name": pack.Name, "version": pack.Version, "digest": pack.Digest},
		"max_score": candidate.maxScore(),
		"rows":      rows,
	}, nil
}

// ---- catalog ----

func (s *service) catalog() (map[string]any, error) {
	// Evals already aggregates agents, models, and Environments for its own
	// target picker. Reusing it keeps one catalog shape across both apps and
	// spares bench a dependency on llm just to list models.
	catalog := map[string]any{}
	if err := s.ctx.PlatformAPI().CallAppResult("evals", "eval_catalog", map[string]any{}, &catalog); err != nil {
		return nil, fmt.Errorf("evals catalog: %w", err)
	}
	if catalog == nil {
		catalog = map[string]any{}
	}
	// Snapshots are a bench-specific way to pin a scenario's world, so they are
	// fetched directly rather than relying on what Evals happens to surface.
	var snapshots []map[string]any
	if err := s.ctx.PlatformAPI().CallAppResult("environments", "environment_snapshot_list", map[string]any{}, &snapshots); err == nil {
		catalog["snapshots"] = snapshots
	}
	catalog["scoring_version"] = ScoringVersion
	catalog["scoring_formula"] = ScoringFormula
	catalog["scoring_weights"] = ScoreWeights
	return catalog, nil
}

// ---- baselines ----

func (s *service) setBaseline(benchRunID, scenarioID string, targetIndex int, label string) (*Baseline, error) {
	run, err := s.db.getRun(benchRunID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, errors.New("run not found")
	}
	if run.Status != RunStatusCompleted {
		return nil, errors.New("only a completed run can be pinned as a baseline")
	}

	matching := []Result{}
	for _, result := range run.Results {
		if result.ScenarioID == scenarioID && result.TargetIndex == targetIndex && result.Admission != AdmissionInvalid {
			matching = append(matching, result)
		}
	}
	if len(matching) == 0 {
		return nil, errors.New("no admitted results for that scenario and target")
	}

	baseline := &Baseline{
		ID: newID("baseline"), PackDigest: run.PackDigest, ScenarioID: scenarioID,
		Target: matching[0].Target, SourceRunID: run.ID, CreatedAt: time.Now().UTC(),
	}
	if label == "" {
		label = matching[0].Target.label()
	}
	baseline.Label = label

	passed, score := 0, 0.0
	for _, result := range matching {
		if result.Passed {
			passed++
		}
		score += result.Score.Score
		baseline.Metrics = result.Metrics
	}
	baseline.PassRate = round3(float64(passed) / float64(len(matching)))
	baseline.Score = round1(score / float64(len(matching)))
	if err := s.db.saveBaseline(baseline); err != nil {
		return nil, err
	}
	return baseline, nil
}

type baselineDelta struct {
	ScenarioID    string  `json:"scenario_id"`
	TargetIndex   int     `json:"target_index"`
	Label         string  `json:"label"`
	BaselineFrom  string  `json:"baseline_label"`
	Score         float64 `json:"score"`
	BaselineScore float64 `json:"baseline_score"`
	ScoreDelta    float64 `json:"score_delta"`
	PassRate      float64 `json:"pass_rate"`
	BaselinePass  float64 `json:"baseline_pass_rate"`
	PassDelta     float64 `json:"pass_rate_delta"`
	Verdict       string  `json:"verdict"`
}

func (s *service) compareToBaselines(benchRunID string) (map[string]any, error) {
	run, err := s.db.getRun(benchRunID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, errors.New("run not found")
	}
	baselines, err := s.db.listBaselines(run.PackDigest)
	if err != nil {
		return nil, err
	}
	index := map[string]Baseline{}
	for _, baseline := range baselines {
		index[baseline.ScenarioID] = baseline
	}

	type bucket struct {
		scores []float64
		passed int
		total  int
		target Target
	}
	buckets := map[string]*bucket{}
	for _, result := range run.Results {
		if result.Admission == AdmissionInvalid {
			continue
		}
		key := fmt.Sprintf("%s|%d", result.ScenarioID, result.TargetIndex)
		if buckets[key] == nil {
			buckets[key] = &bucket{target: result.Target}
		}
		b := buckets[key]
		b.scores = append(b.scores, result.Score.Score)
		b.total++
		if result.Passed {
			b.passed++
		}
	}

	deltas := []baselineDelta{}
	for key, b := range buckets {
		parts := strings.SplitN(key, "|", 2)
		scenarioID := parts[0]
		baseline, ok := index[scenarioID]
		if !ok {
			continue
		}
		total := 0.0
		for _, score := range b.scores {
			total += score
		}
		delta := baselineDelta{
			ScenarioID: scenarioID, Label: b.target.label(), BaselineFrom: baseline.Label,
			Score: round1(total / float64(b.total)), BaselineScore: baseline.Score,
			PassRate: round3(float64(b.passed) / float64(b.total)), BaselinePass: baseline.PassRate,
		}
		delta.ScoreDelta = round1(delta.Score - delta.BaselineScore)
		delta.PassDelta = round3(delta.PassRate - delta.BaselinePass)
		switch {
		case delta.PassDelta > 0 || (delta.PassDelta == 0 && delta.ScoreDelta > 0):
			delta.Verdict = "ahead"
		case delta.PassDelta < 0 || delta.ScoreDelta < 0:
			delta.Verdict = "behind"
		default:
			delta.Verdict = "even"
		}
		deltas = append(deltas, delta)
	}
	sort.Slice(deltas, func(a, b int) bool { return deltas[a].ScenarioID < deltas[b].ScenarioID })
	return map[string]any{"run_id": run.ID, "pack_digest": run.PackDigest, "deltas": deltas}, nil
}

// ---- leaderboard ----

// ScoreComponents averages each part of the scoring contract, so a reader can
// see *why* a target scores what it does rather than only the total.
type ScoreComponents struct {
	Success    float64 `json:"success"`
	Judge      float64 `json:"judge,omitempty"`
	Duration   float64 `json:"duration"`
	Cost       float64 `json:"cost"`
	Turns      float64 `json:"turns"`
	ToolErrors float64 `json:"tool_errors"`
}

type leaderboardRow struct {
	Label             string          `json:"label"`
	Provider          string          `json:"provider,omitempty"`
	Model             string          `json:"model,omitempty"`
	Runs              int             `json:"runs"`
	Passed            int             `json:"passed"`
	PassRate          float64         `json:"pass_rate"`
	AverageScore      float64         `json:"average_score"`
	AverageDurationMS float64         `json:"average_duration_ms"`
	AverageTokens     float64         `json:"average_tokens"`
	AverageCostUSD    float64         `json:"average_cost_usd"`
	Scenarios         int             `json:"scenarios"`
	Packs             int             `json:"packs"`
	MixedCostBasis    bool            `json:"mixed_cost_basis"`
	Components        ScoreComponents `json:"components"`
}

// scenarioRow is one target's record on one scenario, for the per-scenario
// breakdown under a pack's leaderboard.
type scenarioRow struct {
	ScenarioID   string   `json:"scenario_id"`
	ScenarioName string   `json:"scenario_name"`
	ScenarioTags []string `json:"scenario_tags,omitempty"`
	Label        string   `json:"label"`
	Runs         int      `json:"runs"`
	Passed       int      `json:"passed"`
	PassRate     float64  `json:"pass_rate"`
	AverageScore float64  `json:"average_score"`
}

// aggregate folds admitted results into ranked rows keyed by the complete
// target setup. Two directives on the same model must remain separate evidence.
func aggregate(results []resultWithPack) []leaderboardRow {
	type group struct {
		row       leaderboardRow
		scenarios map[string]struct{}
		packs     map[string]struct{}
		bases     map[string]struct{}
	}
	groups := map[string]*group{}
	order := []string{}

	for _, result := range results {
		identity := result.Target.key()
		g := groups[identity]
		if g == nil {
			g = &group{
				row:       leaderboardRow{Label: result.Target.label(), Provider: result.Target.Provider, Model: result.Target.Model},
				scenarios: map[string]struct{}{}, packs: map[string]struct{}{}, bases: map[string]struct{}{},
			}
			groups[identity] = g
			order = append(order, identity)
		}
		g.row.Runs++
		if result.Passed {
			g.row.Passed++
		}
		g.row.AverageScore += result.Score.Score
		g.row.AverageDurationMS += float64(result.Metrics.DurationMS)
		g.row.AverageTokens += float64(result.Metrics.TokensTotal)
		g.row.AverageCostUSD += result.Metrics.CostUSD
		g.row.Components.Success += result.Score.SuccessPoints
		g.row.Components.Judge += result.Score.JudgePoints
		g.row.Components.Duration += result.Score.DurationPoints
		g.row.Components.Cost += result.Score.CostPoints
		g.row.Components.Turns += result.Score.TurnPoints
		g.row.Components.ToolErrors += result.Score.ToolErrorPoints
		g.scenarios[result.ScenarioID] = struct{}{}
		if result.PackDigest != "" {
			g.packs[result.PackDigest] = struct{}{}
		}
		if result.Score.CostBasis != "" {
			g.bases[result.Score.CostBasis] = struct{}{}
		}
	}

	rows := make([]leaderboardRow, 0, len(groups))
	for _, key := range order {
		g := groups[key]
		n := float64(g.row.Runs)
		if n == 0 {
			continue
		}
		g.row.PassRate = round3(float64(g.row.Passed) / n)
		g.row.AverageScore = round1(g.row.AverageScore / n)
		g.row.AverageDurationMS = round1(g.row.AverageDurationMS / n)
		g.row.AverageTokens = round1(g.row.AverageTokens / n)
		g.row.AverageCostUSD = round3(g.row.AverageCostUSD / n)
		g.row.Components = ScoreComponents{
			Success:    round1(g.row.Components.Success / n),
			Judge:      round1(g.row.Components.Judge / n),
			Duration:   round1(g.row.Components.Duration / n),
			Cost:       round1(g.row.Components.Cost / n),
			Turns:      round1(g.row.Components.Turns / n),
			ToolErrors: round1(g.row.Components.ToolErrors / n),
		}
		g.row.Scenarios = len(g.scenarios)
		g.row.Packs = len(g.packs)
		g.row.MixedCostBasis = len(g.bases) > 1
		rows = append(rows, g.row)
	}
	// Pass rate is the benchmark's headline; score only breaks ties.
	sort.Slice(rows, func(a, b int) bool {
		if rows[a].PassRate != rows[b].PassRate {
			return rows[a].PassRate > rows[b].PassRate
		}
		return rows[a].AverageScore > rows[b].AverageScore
	})
	return rows
}

// byScenario breaks each target's record down per scenario, which is where an
// aggregate score hides a target that is strong on one task and weak on another.
func byScenario(results []resultWithPack) []scenarioRow {
	type key struct{ scenario, identity string }
	acc := map[key]*scenarioRow{}
	order := []key{}
	for _, result := range results {
		k := key{result.ScenarioID, result.Target.key()}
		if acc[k] == nil {
			acc[k] = &scenarioRow{ScenarioID: result.ScenarioID, ScenarioName: result.ScenarioName,
				ScenarioTags: result.ScenarioTags, Label: result.Target.label()}
			order = append(order, k)
		}
		row := acc[k]
		row.Runs++
		if result.Passed {
			row.Passed++
		}
		row.AverageScore += result.Score.Score
	}
	rows := make([]scenarioRow, 0, len(acc))
	for _, k := range order {
		row := acc[k]
		row.PassRate = round3(float64(row.Passed) / float64(row.Runs))
		row.AverageScore = round1(row.AverageScore / float64(row.Runs))
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(a, b int) bool {
		if rows[a].ScenarioID != rows[b].ScenarioID {
			return rows[a].ScenarioID < rows[b].ScenarioID
		}
		return rows[a].AverageScore > rows[b].AverageScore
	})
	return rows
}

// leaderboard ranks every admitted result for one sealed digest under that
// pack's scoring version. Comparability is enforced by the join, not convention.
func (s *service) leaderboard(packDigest string) (map[string]any, error) {
	pack, err := s.db.getPackByDigest(packDigest)
	if err != nil {
		return nil, err
	}
	if pack == nil {
		return nil, errors.New("no sealed pack with that digest")
	}
	all, err := s.db.listAdmittedResults(pack.ProfileDigest, "")
	if err != nil {
		return nil, err
	}
	scoped := make([]resultWithPack, 0, len(all))
	for _, result := range all {
		if result.PackDigest == packDigest {
			scoped = append(scoped, result)
		}
	}
	return map[string]any{
		"pack":            map[string]any{"id": pack.ID, "name": pack.Name, "category": pack.Category, "version": pack.Version, "digest": pack.Digest},
		"scoring_version": pack.ScoringVersion,
		"profile_digest":  pack.ProfileDigest,
		"rows":            aggregate(scoped),
		"by_scenario":     byScenario(scoped),
		"scenarios":       len(pack.Scenarios),
	}, nil
}

// globalLeaderboard ranks targets across every sealed pack under one scoring
// version. Targets that ran different packs are still listed, with their
// coverage reported and comparable=false, rather than being averaged together
// as though they had faced the same work.
func (s *service) globalLeaderboard(profileDigest, category string) (map[string]any, error) {
	category = normalizeTaxonomyValue(category)
	versions, err := s.db.scoringVersions(category)
	if err != nil {
		return nil, err
	}
	var profile *Profile
	if profileDigest == "" {
		if profileDigest, err = s.defaultProfileDigest(); err != nil {
			return nil, err
		}
		profile, err = s.resolveProfile(profileDigest)
	} else {
		profile, err = s.db.getProfileByDigest(profileDigest)
	}
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, errors.New("scoring profile not found")
	}
	results, err := s.db.listAdmittedResults(profileDigest, category)
	if err != nil {
		return nil, err
	}

	packs := map[string]map[string]any{}
	for _, result := range results {
		if packs[result.PackDigest] == nil {
			packs[result.PackDigest] = map[string]any{
				"digest": result.PackDigest, "name": result.PackName,
				"category": result.PackCategory, "version": result.PackVersion,
			}
		}
	}
	packList := make([]map[string]any, 0, len(packs))
	for _, p := range packs {
		packList = append(packList, p)
	}
	sort.Slice(packList, func(a, b int) bool {
		return packList[a]["digest"].(string) < packList[b]["digest"].(string)
	})

	rows := aggregate(results)
	// Every target must have faced the same packs for the ranking to be a fair
	// comparison; say so plainly instead of letting the reader assume it.
	comparable := true
	for _, row := range rows {
		if row.Packs != len(packList) {
			comparable = false
			break
		}
	}

	return map[string]any{
		"category":         category,
		"scoring_version":  profile.Version,
		"profile_digest":   profileDigest,
		"profile_name":     profile.Name,
		"max_score":        profile.maxScore(),
		"scoring_versions": versions,
		"packs":            packList,
		"rows":             rows,
		"by_scenario":      byScenario(results),
		"comparable":       comparable,
	}, nil
}
