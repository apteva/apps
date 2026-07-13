package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type service struct {
	ctx      *sdk.AppCtx
	db       store
	runnerMu sync.Mutex
}

func (s *service) saveSuite(item *Suite, creating bool) (*Suite, error) {
	item.ID, item.Name = strings.TrimSpace(item.ID), strings.TrimSpace(item.Name)
	if item.ID == "" {
		item.ID = "suite_" + token(10)
	}
	if !validID(item.ID) {
		return nil, errors.New("invalid suite id")
	}
	if item.Name == "" {
		return nil, errors.New("suite name required")
	}
	if item.RequiredPassRate < 0 || item.RequiredPassRate > 1 {
		return nil, errors.New("required_pass_rate must be between 0 and 1")
	}
	if item.ScheduleMinutes < 0 {
		return nil, errors.New("schedule_minutes must not be negative")
	}
	if creating {
		item.Enabled = true
		if item.RequiredPassRate == 0 {
			item.RequiredPassRate = 1
		}
	}
	if item.ScheduleMinutes > 0 && len(item.ContinuousTargets) == 0 {
		return nil, errors.New("continuous suites require at least one target")
	}
	if err := s.db.saveSuite(item); err != nil {
		return nil, err
	}
	return s.db.getSuite(item.ID)
}

func (s *service) saveCase(item *Case, creating bool) (*Case, error) {
	item.ID, item.SuiteID, item.Name, item.Prompt = strings.TrimSpace(item.ID), strings.TrimSpace(item.SuiteID), strings.TrimSpace(item.Name), strings.TrimSpace(item.Prompt)
	if item.ID == "" {
		item.ID = "case_" + token(10)
	}
	if !validID(item.ID) {
		return nil, errors.New("invalid case id")
	}
	if item.SuiteID == "" || item.Name == "" || item.Prompt == "" {
		return nil, errors.New("suite_id, name, and prompt required")
	}
	if suite, err := s.db.getSuite(item.SuiteID); err != nil {
		return nil, err
	} else if suite == nil {
		return nil, errors.New("suite not found")
	}
	goals := make([]string, 0, len(item.Goals))
	for _, goal := range item.Goals {
		if value := strings.TrimSpace(goal); value != "" {
			goals = append(goals, value)
		}
	}
	item.Goals = goals
	if len(item.Goals) == 0 && len(item.Assertions) == 0 {
		return nil, errors.New("case requires a goal or deterministic assertion")
	}
	if item.TimeoutSeconds < 0 || item.TimeoutSeconds > 1800 {
		return nil, errors.New("timeout_seconds must be at most 1800")
	}
	if item.MaxTurns < 0 || item.MaxTurns > 100 {
		return nil, errors.New("max_turns must be at most 100")
	}
	if creating {
		item.Enabled = true
	}
	if err := s.db.saveCase(item); err != nil {
		return nil, err
	}
	return s.db.getCase(item.ID)
}

func (s *service) createExperiment(suiteID, name, trigger string, targets []Target, repetitions, baseline int, judgeModel string) (*Experiment, error) {
	suite, err := s.db.getSuite(strings.TrimSpace(suiteID))
	if err != nil || suite == nil {
		if err == nil {
			err = errors.New("suite not found")
		}
		return nil, err
	}
	if len(targets) == 0 {
		return nil, errors.New("at least one target required")
	}
	if repetitions <= 0 {
		repetitions = 1
	}
	if repetitions > 20 {
		return nil, errors.New("repetitions must be at most 20")
	}
	if baseline < 0 || baseline >= len(targets) {
		baseline = 0
	}
	if trigger == "" {
		trigger = "manual"
	}
	if name == "" {
		name = suite.Name + " - " + time.Now().UTC().Format("2006-01-02 15:04")
	}
	if judgeModel == "" {
		judgeModel = suite.JudgeModel
	}

	agents, _ := s.ctx.RuntimeAPI().ListRuntimeCatalogAgents(s.ctx.CurrentProject())
	byID := map[int64]sdk.RuntimeCatalogAgent{}
	for _, agent := range agents {
		byID[agent.ID] = agent
	}
	for i := range targets {
		if targets[i].AgentID <= 0 {
			return nil, fmt.Errorf("target %d: agent_id required", i)
		}
		agent, ok := byID[targets[i].AgentID]
		if !ok {
			return nil, fmt.Errorf("target %d: agent not found", i)
		}
		targets[i].AgentName, targets[i].Directive, targets[i].DirectiveETag = agent.Name, agent.Directive, agent.DirectiveETag
		if targets[i].Provider == "" && strings.Contains(targets[i].Model, "/") {
			parts := strings.SplitN(targets[i].Model, "/", 2)
			targets[i].Provider, targets[i].Model = parts[0], parts[1]
		}
	}
	cases := []Case{}
	for _, item := range suite.Cases {
		if !item.Enabled {
			continue
		}
		if item.EnvironmentID == "" {
			item.EnvironmentID = suite.EnvironmentID
		}
		cases = append(cases, item)
	}
	exp := &Experiment{ID: "exp_" + token(12), SuiteID: suite.ID, SuiteRevision: suite.Revision, Name: strings.TrimSpace(name), TriggerType: trigger, Status: "queued", Targets: targets, Repetitions: repetitions, JudgeModel: judgeModel, BaselineTarget: baseline, CreatedAt: time.Now().UTC()}
	if err := s.db.createExperiment(exp, cases); err != nil {
		return nil, err
	}
	s.ctx.Emit("eval.experiment.created", map[string]any{"experiment_id": exp.ID, "suite_id": suite.ID, "trigger_type": trigger})
	return s.db.getExperiment(exp.ID)
}

func (s *service) catalog() (map[string]any, error) {
	var environmentCatalog map[string]any
	if err := s.ctx.PlatformAPI().CallAppResult("environments", "environment_catalog", map[string]any{}, &environmentCatalog); err != nil {
		return nil, err
	}
	var environments []EnvironmentDefinition
	if err := s.ctx.PlatformAPI().CallAppResult("environments", "environment_list", map[string]any{}, &environments); err != nil {
		return nil, err
	}
	var models LLMModels
	if err := s.ctx.PlatformAPI().CallAppResult("llm", "llm_models_list", map[string]any{}, &models); err != nil {
		return nil, err
	}
	if environmentCatalog == nil {
		environmentCatalog = map[string]any{}
	}
	environmentCatalog["environments"] = environments
	environmentCatalog["models"] = models.Models
	return environmentCatalog, nil
}

func (s *service) createEnvironment(input map[string]any) (*EnvironmentDefinition, error) {
	var created EnvironmentDefinition
	if err := s.ctx.PlatformAPI().CallAppResult("environments", "environment_create", input, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

func (s *service) schedule(ctx context.Context) error {
	if !s.runnerMu.TryLock() {
		return nil
	}
	defer s.runnerMu.Unlock()
	now := time.Now().UTC()
	suites, err := s.db.dueSuites(now)
	if err != nil {
		return err
	}
	for _, suite := range suites {
		if len(suite.ContinuousTargets) == 0 {
			_ = s.db.scheduleNext(suite, now)
			continue
		}
		_, createErr := s.createExperiment(suite.ID, suite.Name+" continuous", "schedule", suite.ContinuousTargets, 1, 0, suite.JudgeModel)
		if createErr != nil {
			s.ctx.Logger().Error("queue continuous eval", "suite_id", suite.ID, "error", createErr)
		}
		_ = s.db.scheduleNext(suite, now)
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func (s *service) runNext(ctx context.Context) error {
	if !s.runnerMu.TryLock() {
		return nil
	}
	defer s.runnerMu.Unlock()
	run, err := s.db.claimRun()
	if err != nil || run == nil {
		return err
	}
	return s.executeRun(ctx, run)
}

func (s *service) applySuggestion(id string) (*sdk.RuntimeCatalogAgent, error) {
	item, err := s.db.getSuggestion(id)
	if err != nil || item == nil {
		if err == nil {
			err = errors.New("suggestion not found")
		}
		return nil, err
	}
	if item.Status != "proposed" {
		return nil, errors.New("suggestion is not pending")
	}
	updated, err := s.ctx.RuntimeAPI().UpdateAgentDirective(item.AgentID, sdk.AgentDirectiveUpdateRequest{Directive: item.Directive, ExpectedETag: item.ExpectedETag, Reason: "accepted eval suggestion from run " + item.RunID})
	if err != nil {
		return nil, err
	}
	if err := s.db.markSuggestionApplied(id); err != nil {
		return nil, err
	}
	return updated, nil
}

func token(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}

func validID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && char != '-' && char != '_' && char != '.' {
			return false
		}
	}
	return true
}
