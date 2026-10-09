package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type readinessCheck struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	Kind       string `json:"kind"`
	Action     string `json:"action"`
	ActionURL  string `json:"action_url,omitempty"`
	CheckedAt  string `json:"checked_at"`
	EvidenceAt string `json:"evidence_at,omitempty"`
}
type readinessStage struct {
	Name           string            `json:"name"`
	Command        []string          `json:"command"`
	Directory      string            `json:"directory,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Outputs        []string          `json:"outputs,omitempty"`
}
type readinessPipeline struct {
	OS             string           `json:"os,omitempty"`
	Arch           string           `json:"arch,omitempty"`
	Prepare        []readinessStage `json:"prepare,omitempty"`
	BuildDirectory string           `json:"build_directory,omitempty"`
	Tests          []readinessStage `json:"tests,omitempty"`
	Outputs        []string         `json:"outputs,omitempty"`
}

func pipelineDigest(v any) string {
	var p readinessPipeline
	b, _ := json.Marshal(v)
	if json.Unmarshal(b, &p) != nil {
		return ""
	}
	return studioHash(p)
}

var failedStagePattern = regexp.MustCompile(`(?m)stage ([^\n]+?) (?:failed|missing output)`)

func failingBuildStage(b map[string]any) string {
	err := txt(b["error"])
	if m := failedStagePattern.FindStringSubmatch(err); len(m) > 1 {
		return m[1]
	}
	for _, name := range []string{"xcodebuild archive", "xcodebuild export", "source", "artifact", "signing", "pipeline", "Gradle"} {
		if strings.Contains(err, name) {
			return name
		}
	}
	if err != "" {
		return "Build execution"
	}
	return "Inspect build logs"
}

func studioReadiness(ctx *sdk.AppCtx, s GameScope, link, out, args map[string]any) map[string]any {
	checked := nowRFC()
	checks := []readinessCheck{}
	deployURL := appPage(s, "deploy")
	codeURL := appPage(s, "code")
	add := func(id, name, status, reason, kind, action, url, evidenceAt string) {
		checks = append(checks, readinessCheck{id, name, status, reason, kind, action, url, checked, evidenceAt})
	}
	d := object(out["deployment"])
	cfg := jsonObject(d["target_config_json"])
	pipeline := object(cfg["pipeline"])
	selection := jsonObject(d["source_extra_json"])
	if _, e := studioSource(ctx, s, txt(link["source_id"])); e != nil {
		add("source", "Source repository", "blocked", e.Error(), "configuration", "Repair or reattach source", codeURL, "")
	} else if out["link_warning"] != nil {
		add("source", "Source repository", "blocked", txt(out["link_warning"]), "configuration", "Reattach target", deployURL, "")
	} else {
		add("source", "Source repository", "ready", "Linked Code repository belongs to this game’s project.", "configuration", "Open source", codeURL, "")
	}
	deps := records(selection["dependencies"])
	depStatus := "ready"
	depReason := "No sibling dependencies configured."
	if len(deps) > 0 {
		depReason = fmt.Sprintf("%d pinned sibling dependencies.", len(deps))
		for _, dep := range deps {
			if txt(dep["snapshot_id"]) == "" {
				depStatus = "blocked"
				depReason = "A sibling dependency is unpinned; capture an exact Code snapshot."
				break
			}
			receipt, e := studioCall(ctx, s, "code", "repos_export", map[string]any{"slug": dep["slug"], "snapshot_id": dep["snapshot_id"], "metadata_only": true})
			if e != nil || txt(receipt["snapshot_id"]) != txt(dep["snapshot_id"]) {
				depStatus = "blocked"
				depReason = "Pinned dependency unavailable: " + txt(dep["slug"])
				if e != nil {
					depReason += " — " + e.Error()
				}
				break
			}
		}
	}
	if recipe := object(cfg["games_recipe"]); recipe != nil {
		r, e := studioRecipe(ctx, s, txt(recipe["id"]), txt(recipe["version"]))
		if e == nil && txt(r["dependency_path"]) != "" {
			found := false
			for _, dep := range deps {
				if dep["path"] == r["dependency_path"] {
					found = true
				}
			}
			if !found {
				depStatus = "blocked"
				depReason = "Recipe’s required sibling dependency is missing."
			}
		}
	}
	add("dependencies", "Pinned sibling dependencies", depStatus, depReason, "configuration", "Configure dependency pins", codeURL, "")
	if len(records(pipeline["tests"])) == 0 || len(array(pipeline["outputs"])) == 0 {
		add("pipeline", "Export and build recipe", "blocked", "Declare artifact outputs and required final-artifact tests.", "configuration", "Configure build recipe", deployURL, "")
	} else {
		add("pipeline", "Export and build recipe", "ready", fmt.Sprintf("%d preparation stages, %d artifact tests; generated directory %s.", len(records(pipeline["prepare"])), len(records(pipeline["tests"])), stringArg(pipeline, "build_directory", "source root")), "configuration", "Review recipe", deployURL, "")
	}
	backend := stringArg(d, "build_backend", "local")
	runnerStatus := "unchecked"
	runnerReason := backend + " runner selected; OS, toolchain and pipeline execution need a successful build."
	if backend != "local" && len(jsonObject(d["build_backend_config_json"])) == 0 {
		runnerStatus = "blocked"
		runnerReason = "Remote runner configuration is missing."
	}
	add("runner", "Runner and toolchain", runnerStatus, runnerReason, "configuration", "Configure runner in Deploy", deployURL, "")
	mobile := link["platform"] == "ios" || link["platform"] == "android"
	if mobile {
		status, e := studioCall(ctx, s, "deploy", "deploy_mobile_signing_status", targetArgs(link))
		if e != nil {
			add("signing", "Signing identity", "unchecked", e.Error(), "configuration", "Configure signing in Deploy", deployURL, "")
		} else {
			summary := object(status["signing"])
			if summary["provider_ready"] == true {
				add("signing", "Signing identity", "ready", "Deploy reports the selected identity and provider setup ready.", "configuration", "Review signing identity", deployURL, "")
			} else {
				add("signing", "Signing identity", "blocked", "Deploy signing setup is incomplete or requires provider action.", "configuration", "Complete signing in Deploy", deployURL, "")
			}
		}
		if out["store_error"] != nil {
			add("store", "Store identity and listing", "unchecked", txt(out["store_error"]), "configuration", "Review store configuration", deployURL, "")
		} else {
			preflight := object(out["store_preflight"])
			raw := contentJSON(preflight)
			status := "unchecked"
			reason := "Inspect provider findings in Deploy; local settings alone do not prove store readiness."
			if preflight["ready"] == true || object(preflight["preflight"])["ready"] == true {
				status = "ready"
				reason = "Deploy’s store preflight reports ready."
			} else if strings.Contains(raw, `"blocking":true`) || preflight["ready"] == false || object(preflight["preflight"])["ready"] == false {
				status = "blocked"
				reason = "Deploy reports blocking store findings. Review identity, listing and account access."
			}
			add("store", "Store identity and listing", status, reason, "configuration", "Resolve store findings in Deploy", deployURL, "")
		}
	}
	channel := txt(args["channel"])
	rule := object(object(cfg["release_policy"])["channels"])[channel]
	if channel == "" {
		add("policy", "Release-channel policy", "unchecked", "Choose a channel to check its rule.", "configuration", "Select release channel", "", "")
	} else if rule == nil {
		add("policy", "Release-channel policy", "blocked", "No explicit rule is configured for "+channel+".", "configuration", "Configure channel policy in Deploy", deployURL, "")
	} else {
		reason := "Channel rule configured; Deploy checks approval, source-channel history and waiting periods when publishing."
		if object(rule)["require_approval"] == true {
			reason += " Authorized approval is required."
		}
		add("policy", "Release-channel policy", "ready", reason, "configuration", "Review policy and approvals", deployURL, "")
	}
	var build map[string]any
	for _, b := range records(out["builds"]) {
		if number(b["id"]) == number(args["build_id"]) && ownedRemoteRecord(out, "builds", number(b["id"]), link) {
			build = b
			break
		}
	}
	evidence := map[string]any{}
	if build == nil {
		add("tests", "Selected build’s artifact tests", "unchecked", "Select a retained build. Configured tests are separate from passing evidence.", "evidence", "Build or select a build", "", "")
	} else {
		manifest := jsonObject(build["artifact_manifest_json"])
		ev := object(manifest["pipeline"])
		evidence = map[string]any{"build_id": build["id"], "status": build["status"], "tests": ev["tests"], "completed_at": ev["completed_at"], "artifact_sha256": ev["artifact_sha256"], "download_url": build["artifact_download_url"], "error": build["error"]}
		status := "ready"
		reason := "Selected build reports passing tests against the retained artifact; Deploy verifies its attestation on publication."
		if build["status"] != "succeeded" {
			status = "blocked"
			reason = "Selected build has not succeeded."
			if build["status"] == "failed" {
				evidence["failing_stage"] = failingBuildStage(build)
				reason = "Failed stage: " + txt(evidence["failing_stage"]) + " — " + txt(build["error"])
			}
		} else if ev == nil || txt(ev["completed_at"]) == "" || txt(ev["artifact_sha256"]) == "" {
			status = "unchecked"
			reason = "This build has no retained final-artifact pipeline evidence."
		} else if buildIdentityChanged(cfg, jsonObject(build["target_config_json"])) {
			status = "blocked"
			reason = "Target identity or recipe changed since this build; build again."
		} else if pipelineDigest(pipeline) != txt(ev["config_sha256"]) {
			status = "blocked"
			reason = "Recipe configuration changed since this build; build again."
		} else {
			passed := map[string]bool{}
			for _, t := range array(ev["tests"]) {
				passed[txt(t)] = true
			}
			required := []string{}
			for _, t := range records(pipeline["tests"]) {
				required = append(required, txt(t["name"]))
			}
			for _, t := range array(object(rule)["required_tests"]) {
				required = append(required, txt(t))
			}
			for _, t := range required {
				if !passed[t] {
					status = "blocked"
					reason = "Missing passing artifact test: " + t
					break
				}
			}
		}
		if status == "ready" && stringArg(build, "build_backend", "local") == backend && contentJSON(jsonObject(build["build_backend_config_json"])) == contentJSON(jsonObject(d["build_backend_config_json"])) {
			for i := range checks {
				if checks[i].ID == "runner" {
					checks[i].Status = "ready"
					checks[i].Reason = "Selected build completed on this runner with matching pipeline evidence."
					checks[i].EvidenceAt = txt(ev["completed_at"])
				}
			}
		}
		add("tests", "Selected build’s artifact tests", status, reason, "evidence", "Inspect artifacts, tests and logs", "", txt(ev["completed_at"]))
	}
	return map[string]any{"checks": checks, "checked_at": checked, "build_id": args["build_id"], "channel": channel, "evidence": evidence}
}

func buildIdentityChanged(current, frozen map[string]any) bool {
	for _, key := range []string{"bundle_id", "package_name", "scheme", "team_id", "connections", "games_recipe"} {
		if contentJSON(current[key]) != contentJSON(frozen[key]) {
			return true
		}
	}
	return false
}
