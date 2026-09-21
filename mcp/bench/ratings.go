package main

import (
	"errors"
	"sort"
	"strings"
)

const categoryBalancedRatingVersion = "2026-09.category-balanced-v3"

// RatingPolicy is an immutable, auditable view over stored benchmark evidence.
// It never mutates the score sealed into a Result. Instead, the rating board
// cites the source scoring contract and derives a second score at read time.
type RatingPolicy struct {
	Name                       string   `json:"name"`
	Version                    string   `json:"version"`
	Digest                     string   `json:"digest"`
	SourceProfileDigest        string   `json:"source_profile_digest"`
	ObjectiveCategories        []string `json:"objective_categories"`
	ObjectiveCorrectnessWeight float64  `json:"objective_correctness_weight"`
	ObjectiveEfficiencyWeight  float64  `json:"objective_efficiency_weight"`
	SubjectiveScore            string   `json:"subjective_score"`
	EvidenceSelection          string   `json:"evidence_selection"`
	Aggregation                string   `json:"aggregation"`
}

func categoryBalancedRatingPolicy() (RatingPolicy, error) {
	source := agenticQualityV2()
	digest, err := profileDigest(source)
	if err != nil {
		return RatingPolicy{}, err
	}
	policy := RatingPolicy{
		Name:                       "Category-balanced model rating v3",
		Version:                    categoryBalancedRatingVersion,
		SourceProfileDigest:        digest,
		ObjectiveCategories:        []string{"analytics", "coding"},
		ObjectiveCorrectnessWeight: 0.8,
		ObjectiveEfficiencyWeight:  0.2,
		SubjectiveScore:            "source agentic-quality-v2 score",
		EvidenceSelection:          "latest admitted result per model, pack, scenario, and trial",
		Aggregation:                "equal mean across categories after averaging evidence within each category",
	}
	contract := struct {
		Version             string   `json:"version"`
		SourceProfileDigest string   `json:"source_profile_digest"`
		ObjectiveCategories []string `json:"objective_categories"`
		CorrectnessWeight   float64  `json:"correctness_weight"`
		EfficiencyWeight    float64  `json:"efficiency_weight"`
		SubjectiveScore     string   `json:"subjective_score"`
		EvidenceSelection   string   `json:"evidence_selection"`
		Aggregation         string   `json:"aggregation"`
	}{
		policy.Version, policy.SourceProfileDigest, policy.ObjectiveCategories,
		policy.ObjectiveCorrectnessWeight, policy.ObjectiveEfficiencyWeight,
		policy.SubjectiveScore, policy.EvidenceSelection, policy.Aggregation,
	}
	policy.Digest, err = canonicalDigest(contract)
	return policy, err
}

type ratingCategoryScore struct {
	Category     string  `json:"category"`
	AverageScore float64 `json:"average_score"`
	Evidence     int     `json:"evidence"`
}

type ratingRow struct {
	Label             string                `json:"label"`
	Provider          string                `json:"provider,omitempty"`
	Model             string                `json:"model,omitempty"`
	AverageScore      float64               `json:"average_score"`
	Runs              int                   `json:"runs"`
	Passed            int                   `json:"passed"`
	PassRate          float64               `json:"pass_rate"`
	AverageDurationMS float64               `json:"average_duration_ms"`
	AverageTokens     float64               `json:"average_tokens"`
	Scenarios         int                   `json:"scenarios"`
	Packs             int                   `json:"packs"`
	Categories        []ratingCategoryScore `json:"categories"`
	Components        map[string]float64    `json:"components"`
}

func resultModel(result Result) (provider, model, key, label string) {
	provider, model = result.Target.Provider, result.Target.Model
	if provider == "" {
		provider = result.Metrics.Provider
	}
	if model == "" {
		model = result.Metrics.Model
	}
	provider, model = strings.TrimSpace(provider), strings.TrimSpace(model)
	key = strings.ToLower(provider + "/" + model)
	label = strings.TrimPrefix(provider+"/"+model, "/")
	return
}

func isObjectiveRatingCategory(category string, policy RatingPolicy) bool {
	category = normalizeTaxonomyValue(category)
	for _, candidate := range policy.ObjectiveCategories {
		if category == candidate {
			return true
		}
	}
	return false
}

func derivedRatingScore(result resultWithPack, policy RatingPolicy) (float64, bool) {
	if !isObjectiveRatingCategory(result.PackCategory, policy) {
		return result.Score.Score, true
	}
	if result.Evaluation.CorrectnessScore == nil {
		return 0, false
	}
	correctness := clamp(*result.Evaluation.CorrectnessScore, 0, 100)
	efficiency := clamp(result.Score.EfficiencyScore, 0, 100)
	return round1(policy.ObjectiveCorrectnessWeight*correctness + policy.ObjectiveEfficiencyWeight*efficiency), true
}

// modelRatings replays immutable v2 evidence through the category-balanced v3
// policy. Replacements do not erase failed attempts: the raw result remains in
// Bench, while the latest-result selection rule makes the displayed record
// deterministic and prevents a superseded timeout from being averaged twice.
func (s *service) modelRatings(category string) (map[string]any, error) {
	policy, err := categoryBalancedRatingPolicy()
	if err != nil {
		return nil, err
	}
	category = normalizeTaxonomyValue(category)
	results, err := s.db.listAdmittedResults(policy.SourceProfileDigest, category)
	if err != nil {
		return nil, err
	}

	type evidenceKey struct {
		Model, Pack, Scenario string
		Trial                 int
	}
	latest := map[evidenceKey]resultWithPack{}
	incompatible := 0
	for _, result := range results {
		_, _, modelKey, _ := resultModel(result.Result)
		if modelKey == "/" {
			incompatible++
			continue
		}
		key := evidenceKey{modelKey, result.PackDigest, result.ScenarioID, result.Trial}
		previous, exists := latest[key]
		if !exists || result.CreatedAt.After(previous.CreatedAt) ||
			(result.CreatedAt.Equal(previous.CreatedAt) && result.ID > previous.ID) {
			latest[key] = result
		}
	}

	type categoryAcc struct {
		total float64
		count int
	}
	type ratingAcc struct {
		row        ratingRow
		categories map[string]*categoryAcc
		packs      map[string]struct{}
		scenarios  map[string]struct{}
	}
	groups := map[string]*ratingAcc{}
	packMap := map[string]map[string]any{}
	for _, result := range latest {
		score, compatible := derivedRatingScore(result, policy)
		if !compatible {
			incompatible++
			continue
		}
		provider, model, modelKey, label := resultModel(result.Result)
		group := groups[modelKey]
		if group == nil {
			group = &ratingAcc{
				row:        ratingRow{Label: label, Provider: provider, Model: model},
				categories: map[string]*categoryAcc{}, packs: map[string]struct{}{}, scenarios: map[string]struct{}{},
			}
			groups[modelKey] = group
		}
		cat := normalizeTaxonomyValue(result.PackCategory)
		if group.categories[cat] == nil {
			group.categories[cat] = &categoryAcc{}
		}
		group.categories[cat].total += score
		group.categories[cat].count++
		group.row.Runs++
		if result.Passed {
			group.row.Passed++
		}
		group.row.AverageDurationMS += float64(result.Metrics.DurationMS)
		group.row.AverageTokens += float64(result.Metrics.TokensTotal)
		group.packs[result.PackDigest] = struct{}{}
		group.scenarios[result.PackDigest+"/"+result.ScenarioID] = struct{}{}
		if packMap[result.PackDigest] == nil {
			packMap[result.PackDigest] = map[string]any{
				"digest": result.PackDigest, "name": result.PackName,
				"category": result.PackCategory, "version": result.PackVersion,
			}
		}
	}

	rows := make([]ratingRow, 0, len(groups))
	for _, group := range groups {
		categoryTotal := 0.0
		categories := make([]string, 0, len(group.categories))
		for name := range group.categories {
			categories = append(categories, name)
		}
		sort.Strings(categories)
		for _, name := range categories {
			acc := group.categories[name]
			average := round1(acc.total / float64(acc.count))
			group.row.Categories = append(group.row.Categories, ratingCategoryScore{name, average, acc.count})
			categoryTotal += average
		}
		group.row.AverageScore = round1(categoryTotal / float64(len(group.row.Categories)))
		group.row.Components = map[string]float64{}
		for _, categoryScore := range group.row.Categories {
			group.row.Components[categoryScore.Category] = round1(categoryScore.AverageScore / float64(len(group.row.Categories)))
		}
		group.row.PassRate = round3(float64(group.row.Passed) / float64(group.row.Runs))
		group.row.AverageDurationMS = round1(group.row.AverageDurationMS / float64(group.row.Runs))
		group.row.AverageTokens = round1(group.row.AverageTokens / float64(group.row.Runs))
		group.row.Packs, group.row.Scenarios = len(group.packs), len(group.scenarios)
		rows = append(rows, group.row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].AverageScore != rows[j].AverageScore {
			return rows[i].AverageScore > rows[j].AverageScore
		}
		return rows[i].Label < rows[j].Label
	})

	packs := make([]map[string]any, 0, len(packMap))
	for _, pack := range packMap {
		packs = append(packs, pack)
	}
	sort.Slice(packs, func(i, j int) bool { return packs[i]["digest"].(string) < packs[j]["digest"].(string) })
	comparable := len(rows) > 0
	for _, row := range rows {
		if row.Packs != len(packs) {
			comparable = false
			break
		}
	}
	if len(results) > 0 && len(rows) == 0 {
		return nil, errors.New("stored results do not contain the correctness evidence required by the rating policy")
	}
	return map[string]any{
		"category": category, "rating_policy": policy, "scoring_version": policy.Version,
		"packs": packs, "rows": rows, "comparable": comparable,
		"source_results": len(results), "selected_results": len(latest), "incompatible_results": incompatible,
	}, nil
}
