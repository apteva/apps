package main

import (
	"context"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"testing"
)

func TestTargetStartupPausePolicy(t *testing.T) {
	for _, target := range []Target{
		{AgentID: 7, Provider: "provider", Model: "model"},
		{Draft: &sdk.RuntimeAgentDraft{Name: "Isolated draft", Directive: "Synthetic instructions"}, Provider: "provider", Model: "model"},
	} {
		for _, mode := range []string{"voice", "text", ""} {
			got := targetAgentSpec(target, mode)
			if paused, ok := got["start_paused"].(bool); !ok || paused != (mode != "voice") {
				t.Fatalf("mode=%q startup policy=%+v", mode, got)
			}
			if got["alias"] != "main" || got["provider"] != target.Provider || got["model"] != target.Model {
				t.Fatalf("target selection changed: %+v", got)
			}
			if target.Draft != nil {
				if got["draft"] != target.Draft {
					t.Fatal("draft lost")
				}
				if _, ok := got["source_agent_id"]; ok {
					t.Fatal("draft converted to a live source")
				}
			} else if got["source_agent_id"] != target.AgentID {
				t.Fatal("source changed")
			}
		}
	}
}

type voiceStartupPlatform struct {
	*evalCampaignPlatformStub
	controls int
}

func (p *voiceStartupPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	if app == "environments" && tool == "environment_agent_control" {
		p.controls++
	}
	if app == "environments" && tool == "environment_voice_call" {
		raw := []byte(`{"status":"cancelled","cancellation_reason":"isolated_test_complete","cleanup":{"status":"complete"}}`)
		return json.Unmarshal(raw, out)
	}
	return p.evalCampaignPlatformStub.CallAppResult(app, tool, input, out)
}

func TestVoiceRunnerSpawnsUnpausedWithoutManualResume(t *testing.T) {
	db, run := cancellationRun(t)
	run.CaseSnapshot.Mode = "voice"
	run.CaseSnapshot.Voice = &VoiceCase{CallerGoal: "Synthetic local call"}
	p := &voiceStartupPlatform{evalCampaignPlatformStub: &evalCampaignPlatformStub{t: t}}
	svc := &service{db: db, ctx: sdk.NewAppCtxForTest(nil, db.db, nil, p, nil)}
	if err := svc.executeRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if len(p.spawnInputs) != 1 {
		t.Fatalf("spawns=%+v", p.spawnInputs)
	}
	agent, ok := p.spawnInputs[0]["agent"].(map[string]any)
	if !ok || agent["start_paused"] != false {
		t.Fatalf("voice target startup=%+v", p.spawnInputs)
	}
	if p.controls != 0 || len(p.prompts) != 0 {
		t.Fatalf("unexpected manual resume/opening: controls=%d prompts=%v", p.controls, p.prompts)
	}
	stored, err := db.getRun(run.ID)
	if err != nil || stored.Status != "cancelled" || stored.OverallScore != nil || len(stored.Attempts) != 1 || p.stopped != 1 {
		t.Fatalf("result=%+v stops=%d err=%v", stored, p.stopped, err)
	}
}
