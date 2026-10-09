package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type setupPlatform struct {
	*studioFake
	repo, deployment, environment map[string]any
	creates                       map[string]int
	lose                          string
	invalidReceipt                bool
}

func (f *setupPlatform) CallAppResult(app, tool string, in map[string]any, out any) error {
	if in["_project_id"] != "test-proj" {
		return errors.New("missing trusted project")
	}
	var value any
	switch app + "." + tool {
	case "code.repos_create":
		f.creates["repository"]++
		f.repo = map[string]any{"id": int64(10), "name": in["name"], "slug": in["slug"], "description": in["description"], "project_id": "test-proj"}
		value = map[string]any{"repository": f.repo}
		if f.lose == "repository" {
			return errors.New("lost repository reply")
		}
	case "code.repos_get":
		if f.repo != nil && f.repo["slug"] == in["slug"] {
			value = map[string]any{"repository": f.repo}
		} else {
			return f.studioFake.CallAppResult(app, tool, in, out)
		}
	case "code.repos_export":
		value = map[string]any{"snapshot_id": in["snapshot_id"], "source_revision": "rev-1"}
	case "deploy.deploy_init":
		f.creates["deployment"]++
		f.deployment = map[string]any{"id": int64(20), "name": in["name"], "description": in["description"], "project_id": "test-proj", "source_kind": "code", "source_ref": in["source_ref"], "target_kind": in["target_kind"], "environment": "production", "target_config_json": in["target_config_json"], "source_extra_json": in["source_extra_json"], "build_backend": in["build_backend"]}
		value = map[string]any{"deployment": f.deployment}
		if f.lose == "deployment" {
			return errors.New("lost deployment reply")
		}
		if f.invalidReceipt {
			value = map[string]any{}
		}
	case "deploy.deploy_get":
		if f.deployment == nil || number(in["id"]) != 20 {
			return f.studioFake.CallAppResult(app, tool, in, out)
		}
		d := jsonObject(contentJSON(f.deployment))
		d["environment"] = in["environment"]
		envs := []any{map[string]any{"id": int64(21), "name": "production"}}
		if f.environment != nil {
			envs = append(envs, f.environment)
		}
		value = map[string]any{"deployment": d, "environments": envs}
	case "deploy.deploy_env_create":
		f.creates["environment"]++
		f.environment = map[string]any{"id": int64(22), "name": in["environment"], "description": in["description"]}
		value = map[string]any{"environment": f.environment}
		if f.lose == "environment" {
			return errors.New("lost environment reply")
		}
	default:
		return f.studioFake.CallAppResult(app, tool, in, out)
	}
	b, _ := json.Marshal(value)
	return json.Unmarshal(b, out)
}
func newSetupFixture(t *testing.T) (*sdk.AppCtx, *setupPlatform, GameScope) {
	ctx, f, s := newStudioFixture(t)
	p := &setupPlatform{studioFake: f, creates: map[string]int{}}
	ctx = tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(p))
	if e := initializeGames(ctx); e != nil {
		t.Fatal(e)
	}
	if e := initializeStudio(ctx); e != nil {
		t.Fatal(e)
	}
	out, e := gameAction(ctx, "create", map[string]any{"slug": "moon", "name": "Moon"})
	if e != nil {
		t.Fatal(e)
	}
	s = out.(map[string]any)["game"].(*Game).Scope()
	return ctx, p, s
}
func setupInput() map[string]any {
	return map[string]any{"repo_slug": "new-moon", "repo_name": "Moonhorde", "create_repo": true, "deployment_name": "moonhorde-desktop", "environment": "production", "platform": "desktop", "recipe_id": "command-artifact", "recipe_version": "1", "runner_backend": "local"}
}
func TestSetupResumesUnknownCreationWithoutDuplicates(t *testing.T) {
	for _, stage := range []string{"repository", "deployment", "environment"} {
		t.Run(stage, func(t *testing.T) {
			ctx, f, s := newSetupFixture(t)
			in := setupInput()
			if stage == "environment" {
				in["environment"] = "testing"
				in["create_environment"] = true
			}
			f.lose = stage
			args := map[string]any{"request_key": "setup-one", "setup": in}
			first, e := studioSetup(ctx, s, args)
			if e != nil {
				t.Fatal(e)
			}
			if object(first)["status"] != "unknown" {
				t.Fatal(first)
			}
			if _, e = studioSetup(ctx, s, args); e != nil {
				t.Fatal(e)
			}
			if f.creates[stage] != 1 {
				t.Fatal("uncertain mutation repeated")
			}
			f.lose = ""
			if _, e = studioSetupReconcile(ctx, s, map[string]any{"request_key": "setup-one", "deployment_id": int64(20), "confirm": true}); e != nil {
				t.Fatal(e)
			}
			final, e := studioSetup(ctx, s, args)
			if e != nil || object(final)["status"] != "complete" {
				t.Fatal(final, e)
			}
			if _, e = studioSetup(ctx, s, args); e != nil {
				t.Fatal(e)
			}
			for k, n := range f.creates {
				if n != 1 {
					t.Fatalf("%s duplicated %d", k, n)
				}
			}
			links, e := linksList(ctx, s, "target")
			if e != nil || len(links) != 1 {
				t.Fatal("target not linked", e)
			}
			in["deployment_name"] = "different"
			if _, e = studioSetup(ctx, s, args); e == nil {
				t.Fatal("changed setup reused key")
			}
		})
	}
}
func TestSetupReconciliationRejectsForeignReceipt(t *testing.T) {
	ctx, f, s := newSetupFixture(t)
	f.lose = "deployment"
	args := map[string]any{"request_key": "uncertain", "setup": setupInput()}
	_, e := studioSetup(ctx, s, args)
	if e != nil {
		t.Fatal(e)
	}
	f.deployment["description"] = "other setup"
	if _, e = studioSetupReconcile(ctx, s, map[string]any{"request_key": "uncertain", "deployment_id": int64(20), "confirm": true}); e == nil {
		t.Fatal("foreign receipt accepted")
	}
	if _, e = studioSetupReconcile(ctx, s, map[string]any{"request_key": "uncertain", "resolution": "not_created", "confirm": true}); e == nil {
		t.Fatal("unexplained retry accepted")
	}
}
func TestSetupRecipeAndDependencyValidation(t *testing.T) {
	ctx, _, s := newSetupFixture(t)
	in := setupInput()
	in["platform"] = "ios"
	in["recipe_id"] = "kiln-ios"
	in["bundle_id"] = "com.moonhorde.game"
	in["scheme"] = "Moonhorde"
	in["version_name"] = "0.4.1"
	if _, e := setupDeployConfig(ctx, s, in); e == nil || !strings.Contains(e.Error(), "pinned sibling") {
		t.Fatal(e)
	}
	in["dependencies"] = []any{map[string]any{"slug": "moon", "path": "engine", "snapshot_id": "pin-1"}}
	cfg, e := setupDeployConfig(ctx, s, in)
	if e != nil {
		t.Fatal(e)
	}
	target := jsonObject(cfg["target_config_json"])
	pipeline := object(target["pipeline"])
	if txt(array(pipeline["outputs"])[0]) != "Moonhorde.ipa" {
		t.Fatal(pipeline)
	}
	if strings.Contains(contentJSON(pipeline), "{{") {
		t.Fatal("unresolved recipe parameters")
	}
	in["dependencies"] = []any{map[string]any{"slug": "moon", "path": "../engine", "snapshot_id": "pin-1"}}
	if _, e = setupDeployConfig(ctx, s, in); e == nil {
		t.Fatal("escaping dependency accepted")
	}
}
func TestRecipeVersionsAreImmutable(t *testing.T) {
	ctx, _, s := newSetupFixture(t)
	r, e := studioRecipe(ctx, s, "command-artifact", "1")
	if e != nil {
		t.Fatal(e)
	}
	r["id"] = "custom-game"
	if _, e = recipeSave(ctx, s, map[string]any{"recipe": r}); e != nil {
		t.Fatal(e)
	}
	r["build_cmd"] = "changed"
	if _, e = recipeSave(ctx, s, map[string]any{"recipe": r}); e == nil {
		t.Fatal("recipe version overwritten")
	}
}
func TestReadinessDistinguishesConfigurationFromEvidence(t *testing.T) {
	ctx, _, s := newStudioFixture(t)
	link := linkStudio(t, ctx, s)
	p := map[string]any{"outputs": []any{"game.txt"}, "tests": []any{map[string]any{"name": "smoke", "command": []any{"sh", "-c", "test -s game.txt"}}}}
	cfg := map[string]any{"pipeline": p, "release_policy": map[string]any{"version": "1", "channels": map[string]any{"internal": map[string]any{"required_tests": []any{"smoke"}}}}}
	build := map[string]any{"id": int64(12), "deployment_id": int64(2), "environment_id": int64(3), "status": "succeeded", "artifact_manifest_json": contentJSON(map[string]any{"pipeline": map[string]any{"tests": []any{"smoke"}, "config_sha256": pipelineDigest(p), "artifact_sha256": "digest", "completed_at": "2026-10-09T10:00:00Z"}})}
	out := map[string]any{"deployment": map[string]any{"target_config_json": contentJSON(cfg)}, "builds": []any{build}}
	args := map[string]any{"build_id": int64(12), "channel": "internal"}
	r := studioReadiness(ctx, s, link, out, args)
	checks := r["checks"].([]readinessCheck)
	find := func(id string) readinessCheck {
		for _, c := range checks {
			if c.ID == id {
				return c
			}
		}
		return readinessCheck{}
	}
	if find("tests").Status != "ready" || find("tests").EvidenceAt == "" {
		t.Fatal(checks)
	}
	p["outputs"] = []any{"other.txt"}
	out["deployment"].(map[string]any)["target_config_json"] = contentJSON(cfg)
	r = studioReadiness(ctx, s, link, out, args)
	checks = r["checks"].([]readinessCheck)
	if find("tests").Status != "blocked" {
		t.Fatal("stale evidence accepted")
	}
	args["build_id"] = int64(0)
	r = studioReadiness(ctx, s, link, out, args)
	checks = r["checks"].([]readinessCheck)
	if find("tests").Status != "unchecked" {
		t.Fatal("unselected build marked ready")
	}
}

func TestSetupReceiptExcludesDeployCredentials(t *testing.T) {
	receipt := safeSetupReceipt(map[string]any{"deployment": map[string]any{"id": 20, "name": "moon", "project_id": "test-proj", "target_config_json": "private", "build_backend_config_json": "private", "env_json": "private"}})
	if number(object(receipt["deployment"])["id"]) != 20 || strings.Contains(contentJSON(receipt), "private") {
		t.Fatal(receipt)
	}
}
