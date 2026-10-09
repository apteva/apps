package main

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

// Recipes are configuration data. Deploy alone interprets and executes pipelines.
//
//go:embed build-recipes.json
var buildRecipesJSON []byte
var setupMu sync.Mutex
var setupSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)

const setupSchema = `CREATE TABLE IF NOT EXISTS game_setup_requests (
 project_id TEXT NOT NULL, game_id TEXT NOT NULL, request_key TEXT NOT NULL,
 fingerprint TEXT NOT NULL, data TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,game_id,request_key), FOREIGN KEY(project_id,game_id) REFERENCES games(project_id,id));
 CREATE TABLE IF NOT EXISTS game_build_recipes (
 project_id TEXT NOT NULL, id TEXT NOT NULL, version TEXT NOT NULL, data TEXT NOT NULL,
 PRIMARY KEY(project_id,id,version));`

func studioRecipes(ctx *sdk.AppCtx, s GameScope) ([]map[string]any, error) {
	var out []map[string]any
	if e := json.Unmarshal(buildRecipesJSON, &out); e != nil {
		return nil, e
	}
	rows, e := ctx.AppDB().Query(`SELECT data FROM game_build_recipes WHERE project_id=? ORDER BY id,version`, s.ProjectID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		var r map[string]any
		if e = json.Unmarshal([]byte(raw), &r); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func studioRecipe(ctx *sdk.AppCtx, s GameScope, id, version string) (map[string]any, error) {
	rs, e := studioRecipes(ctx, s)
	if e != nil {
		return nil, e
	}
	for _, r := range rs {
		if txt(r["id"]) == id && txt(r["version"]) == version {
			return r, nil
		}
	}
	return nil, errors.New("select an exact recipe version")
}
func validateRecipe(r map[string]any) error {
	if !setupSlug.MatchString(txt(r["id"])) || txt(r["version"]) == "" || txt(r["name"]) == "" {
		return errors.New("recipe needs an ID, version and name")
	}
	if !validPlatform(txt(r["platform"])) {
		return errors.New("recipe platform must be ios, android, desktop or steam")
	}
	if (r["platform"] == "ios" || r["platform"] == "android") && r["framework"] != r["platform"] {
		return errors.New("mobile recipe framework must match its platform")
	}
	if len(array(r["outputs"])) == 0 || len(records(r["tests"])) == 0 {
		return errors.New("recipe requires artifact outputs and final-artifact tests")
	}
	validPath := func(v string) bool {
		return v == "" || v == "." || (!path.IsAbs(v) && !strings.Contains(v, "\\") && path.Clean(v) != ".." && !strings.HasPrefix(path.Clean(v), "../"))
	}
	if !validPath(txt(r["build_directory"])) {
		return errors.New("generated project directory must stay inside source")
	}
	for _, v := range array(r["outputs"]) {
		if txt(v) == "" || !validPath(txt(v)) {
			return errors.New("artifact output must be a relative path")
		}
	}
	names := map[string]bool{}
	for _, key := range []string{"prepare", "tests"} {
		for _, st := range records(r[key]) {
			name := txt(st["name"])
			cmd := array(st["command"])
			if name == "" || names[name] || len(cmd) == 0 || txt(cmd[0]) == "" || !validPath(txt(st["directory"])) {
				return errors.New("stages need unique names, commands and relative directories")
			}
			for _, a := range cmd {
				if _, ok := a.(string); !ok {
					return errors.New("stage command arguments must be strings")
				}
			}
			if timeout := number(st["timeout_seconds"]); timeout < 0 || timeout > 86400 {
				return errors.New("stage timeout must be 0–86400 seconds")
			}
			for _, output := range array(st["outputs"]) {
				if txt(output) == "" || !validPath(txt(output)) {
					return errors.New("stage outputs must be relative paths")
				}
			}
			names[name] = true
		}
	}
	if txt(r["framework"]) == "command" && strings.TrimSpace(txt(r["build_cmd"])) == "" {
		return errors.New("command recipe requires a build command")
	}
	return nil
}
func validPlatform(p string) bool {
	return p == "ios" || p == "android" || p == "desktop" || p == "steam"
}
func recipeSave(ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	r := object(args["recipe"])
	if e := validateRecipe(r); e != nil {
		return nil, e
	}
	var builtins []map[string]any
	_ = json.Unmarshal(buildRecipesJSON, &builtins)
	for _, b := range builtins {
		if b["id"] == r["id"] {
			return nil, errors.New("use a new ID to customize a bundled recipe")
		}
	}
	var old string
	e := ctx.AppDB().QueryRow(`SELECT data FROM game_build_recipes WHERE project_id=? AND id=? AND version=?`, s.ProjectID, r["id"], r["version"]).Scan(&old)
	raw := contentJSON(r)
	if e == nil {
		if old != raw {
			return nil, errors.New("recipe versions are immutable; save a new version")
		}
		return r, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	_, e = ctx.AppDB().Exec(`INSERT INTO game_build_recipes VALUES(?,?,?,?)`, s.ProjectID, r["id"], r["version"], raw)
	return r, e
}
func jsonObject(v any) map[string]any {
	if m := object(v); m != nil {
		return m
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(txt(v)), &m)
	if m == nil {
		return map[string]any{}
	}
	return m
}
func appPage(s GameScope, app string) string { return "/apps/" + app + "/page?project=" + s.ProjectID }

// Return runner references, never credentials or backend configuration blobs.
func studioSetupOptions(ctx *sdk.AppCtx, s GameScope) (any, error) {
	rs, e := studioRecipes(ctx, s)
	if e != nil {
		return nil, e
	}
	out := map[string]any{"recipes": rs, "repositories": []any{}, "deployments": []any{}, "runners": []any{}, "issues": []any{}, "code_url": appPage(s, "code"), "deploy_url": appPage(s, "deploy")}
	issues := []any{}
	runners := []any{}
	for _, app := range []string{"code", "deploy"} {
		if _, e = studioBinding(ctx, app); e != nil {
			issues = append(issues, map[string]any{"app": app, "reason": e.Error(), "action_url": "/apps", "blocking": true})
			continue
		}
		tool := "repos_list"
		key := "repositories"
		if app == "deploy" {
			tool = "deploy_list"
			key = "deployments"
		}
		data, err := studioCall(ctx, s, app, tool, nil)
		if err != nil {
			issues = append(issues, map[string]any{"app": app, "reason": err.Error(), "action_url": appPage(s, app), "blocking": true})
			continue
		}
		// Restrict discovery to the trusted project, including legacy deployments.
		safe := []any{}
		for _, d := range records(data[key]) {
			if txt(d["project_id"]) != s.ProjectID {
				continue
			}
			safe = append(safe, map[string]any{"id": d["id"], "name": d["name"], "slug": d["slug"], "target_kind": d["target_kind"]})
			if app != "deploy" {
				continue
			}
			envs, err := studioCall(ctx, s, "deploy", "deploy_env_list", map[string]any{"id": d["id"]})
			if err != nil {
				issues = append(issues, map[string]any{"app": app, "reason": err.Error(), "action_url": appPage(s, app), "blocking": false})
				continue
			}
			choices := []any{}
			for _, env := range records(envs["environments"]) {
				if txt(env["archived_at"]) == "" {
					choices = append(choices, map[string]any{"id": env["id"], "name": env["name"]})
				}
			}
			safe[len(safe)-1].(map[string]any)["environments"] = choices
			for _, env := range records(envs["environments"]) {
				if txt(env["archived_at"]) != "" {
					continue
				}
				detail, err := studioDeployment(ctx, s, number(d["id"]), txt(env["name"]))
				if err != nil {
					continue
				}
				effective := object(detail["deployment"])
				cfg := jsonObject(effective["target_config_json"])
				pipeline := object(cfg["pipeline"])
				os := txt(pipeline["os"])
				runners = append(runners, map[string]any{"id": fmt.Sprintf("%d:%s", number(d["id"]), txt(env["name"])), "deployment_id": d["id"], "environment": env["name"], "name": fmt.Sprintf("%s / %s", txt(d["name"]), txt(env["name"])), "backend": effective["build_backend"], "os": os, "arch": pipeline["arch"], "platform": effective["target_kind"], "compatibility": "declared; execution verified by build evidence"})
			}
		}
		out[key] = safe
	}
	out["runners"] = runners
	out["issues"] = issues
	return out, nil
}

type setupRequest struct {
	Status    string         `json:"status"`
	Stage     string         `json:"stage"`
	Error     string         `json:"error,omitempty"`
	Marker    string         `json:"marker"`
	Input     map[string]any `json:"input"`
	Results   map[string]any `json:"results"`
	UpdatedAt string         `json:"updated_at"`
}

func saveSetup(ctx *sdk.AppCtx, s GameScope, key string, r *setupRequest) error {
	r.UpdatedAt = nowRFC()
	_, e := ctx.AppDB().Exec(`UPDATE game_setup_requests SET data=?,updated_at=? WHERE project_id=? AND game_id=? AND request_key=?`, contentJSON(r), r.UpdatedAt, s.ProjectID, s.GameID, key)
	return e
}
func setupHistory(ctx *sdk.AppCtx, s GameScope) (any, error) {
	rows, e := ctx.AppDB().Query(`SELECT request_key,data FROM game_setup_requests WHERE project_id=? AND game_id=? ORDER BY updated_at DESC LIMIT 25`, s.ProjectID, s.GameID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var key, raw string
		if e = rows.Scan(&key, &raw); e != nil {
			return nil, e
		}
		var r map[string]any
		if e = json.Unmarshal([]byte(raw), &r); e != nil {
			return nil, e
		}
		r["request_key"] = key
		out = append(out, r)
	}
	return out, rows.Err()
}
func setupResult(r *setupRequest, key string) map[string]any {
	return map[string]any{"request_key": key, "status": r.Status, "stage": r.Stage, "error": r.Error, "results": r.Results, "input": r.Input, "updated_at": r.UpdatedAt}
}
func setupRecord(ctx *sdk.AppCtx, s GameScope, key string) (*setupRequest, string, error) {
	var fp, raw string
	e := ctx.AppDB().QueryRow(`SELECT fingerprint,data FROM game_setup_requests WHERE project_id=? AND game_id=? AND request_key=?`, s.ProjectID, s.GameID, key).Scan(&fp, &raw)
	var r setupRequest
	if e != nil {
		return nil, "", e
	}
	e = json.Unmarshal([]byte(raw), &r)
	return &r, fp, e
}

func setupDeployConfig(ctx *sdk.AppCtx, s GameScope, in map[string]any) (map[string]any, error) {
	r, e := studioRecipe(ctx, s, txt(in["recipe_id"]), txt(in["recipe_version"]))
	if e != nil {
		return nil, e
	}
	if e = validateRecipe(r); e != nil {
		return nil, e
	}
	if in["platform"] != r["platform"] {
		return nil, errors.New("recipe platform does not match target")
	}
	var runner map[string]any
	if id := number(in["runner_deployment_id"]); id > 0 {
		d, err := studioDeployment(ctx, s, id, stringArg(in, "runner_environment", "production"))
		if err != nil {
			return nil, err
		}
		runner = object(d["deployment"])
	} else {
		backend := txt(in["runner_backend"])
		if backend != "runner" && backend != "local" {
			return nil, errors.New("select a configured runner or a capsule runner")
		}
		runner = map[string]any{"build_backend": backend, "target_config_json": contentJSON(map[string]any{"pipeline": map[string]any{"os": r["os"], "arch": r["arch"]}})}
		if backend == "runner" {
			u, err := url.Parse(txt(in["runner_url"]))
			if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
				return nil, errors.New("capsule runner requires HTTPS, or HTTP loopback, without embedded credentials")
			}
			runner["build_backend_config_json"] = contentJSON(map[string]any{"runner_url": u.String(), "artifact_mode": "file"})
		}
	}
	rcfg := jsonObject(runner["target_config_json"])
	rp := object(rcfg["pipeline"])
	if os := txt(r["os"]); os != "" && txt(rp["os"]) != os {
		return nil, errors.New("runner environment must declare the recipe's OS in its pipeline configuration")
	}
	if arch := txt(r["arch"]); arch != "" && txt(rp["arch"]) != arch {
		return nil, errors.New("runner architecture does not match recipe")
	}
	backend := stringArg(runner, "build_backend", "local")
	if backend != "local" && backend != "runner" && backend != "codemagic" && backend != "github_actions" {
		return nil, errors.New("unsupported Deploy runner")
	}
	r = object(renderRecipeValue(r, map[string]string{"scheme": txt(in["scheme"]), "bundle_id": txt(in["bundle_id"])}))
	if e := validateRecipe(r); e != nil {
		return nil, e
	}
	if txt(in["platform"]) == "ios" {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]{0,99}$`).MatchString(txt(in["scheme"])) || !regexp.MustCompile(`^[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+$`).MatchString(txt(in["bundle_id"])) {
			return nil, errors.New("enter a valid Xcode scheme and bundle ID")
		}
	}
	cfg := map[string]any{"pipeline": map[string]any{"os": r["os"], "arch": r["arch"], "prepare": r["prepare"], "build_directory": r["build_directory"], "outputs": r["outputs"], "tests": r["tests"]}, "games_recipe": map[string]any{"id": r["id"], "version": r["version"], "sha256": studioHash(r)}}
	// Store policy and signing selections belong to the game target, not the runner template.
	for _, k := range []string{"bundle_id", "package_name", "team_id", "scheme", "version_name", "app_store_app_id", "build_number", "version_code", "version_strategy", "connections", "release_policy"} {
		if v, ok := in[k]; ok && v != nil && v != "" {
			cfg[k] = v
		}
	}
	if cfg["version_strategy"] == nil {
		cfg["version_strategy"] = "auto"
	}
	if txt(in["platform"]) == "ios" && (txt(cfg["bundle_id"]) == "" || txt(cfg["scheme"]) == "" || txt(cfg["version_name"]) == "") {
		return nil, errors.New("iOS requires bundle ID, Xcode scheme and version")
	}
	deps := records(in["dependencies"])
	seen := map[string]bool{"app": true}
	safe := []any{}
	if len(deps) > 8 {
		return nil, errors.New("at most eight sibling dependencies")
	}
	for _, d := range deps {
		p := txt(d["path"])
		if !setupSlug.MatchString(p) || seen[p] || txt(d["snapshot_id"]) == "" {
			return nil, errors.New("dependencies require unique sibling paths and pinned snapshots")
		}
		seen[p] = true
		repo, err := studioRepo(ctx, s, txt(d["slug"]))
		if err != nil {
			return nil, err
		}
		_ = repo
		receipt, err := studioCall(ctx, s, "code", "repos_export", map[string]any{"slug": d["slug"], "snapshot_id": d["snapshot_id"], "metadata_only": true})
		if err != nil {
			return nil, fmt.Errorf("dependency %s: %w", txt(d["slug"]), err)
		}
		if txt(receipt["snapshot_id"]) != txt(d["snapshot_id"]) {
			return nil, errors.New("dependency snapshot identity mismatch")
		}
		safe = append(safe, d)
	}
	if required := txt(r["dependency_path"]); required != "" && !seen[required] {
		return nil, fmt.Errorf("recipe requires pinned sibling %s", required)
	}
	source := map[string]any{"dependencies": safe}
	if pin := txt(in["snapshot_id"]); pin != "" {
		source["snapshot_id"] = pin
	}
	return map[string]any{"framework": r["framework"], "build_cmd": r["build_cmd"], "build_backend": backend, "build_backend_config_json": runner["build_backend_config_json"], "target_config_json": contentJSON(cfg), "source_extra_json": contentJSON(source)}, nil
}

// Setup journals retain resource identities, never Deploy configuration or credentials.
func safeSetupReceipt(out map[string]any) map[string]any {
	safe := map[string]any{}
	for _, kind := range []string{"repository", "deployment", "environment"} {
		if resource := object(out[kind]); resource != nil {
			fields := map[string]any{}
			for _, key := range []string{"id", "project_id", "name", "slug", "description", "source_kind", "source_ref", "target_kind"} {
				if v, ok := resource[key]; ok {
					fields[key] = v
				}
			}
			safe[kind] = fields
		}
	}
	return safe
}

// Each mutation is preceded by a durable intent. Unknown outcomes are never repeated.
func studioSetup(ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	setupMu.Lock()
	defer setupMu.Unlock()
	key, e := requiredText(args, "request_key")
	if e != nil {
		return nil, e
	}
	in := object(args["setup"])
	if in == nil {
		return nil, errors.New("setup required")
	}
	fp := studioHash(in)
	r, old, e := setupRecord(ctx, s, key)
	if e == nil {
		if old != fp {
			return nil, errors.New("setup key already used for different inputs")
		}
		if r.Status == "complete" {
			return setupResult(r, key), nil
		}
		if r.Status == "unknown" || r.Status == "dispatching" {
			return setupResult(r, key), nil
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	} else {
		if !validPlatform(txt(in["platform"])) || !setupSlug.MatchString(txt(in["repo_slug"])) || !setupSlug.MatchString(txt(in["environment"])) {
			return nil, errors.New("select a platform and valid repository/environment names")
		}
		if _, e = studioBinding(ctx, "code"); e != nil {
			return nil, e
		}
		if _, e = studioBinding(ctx, "deploy"); e != nil {
			return nil, e
		}
		if number(in["deployment_id"]) == 0 {
			if !setupSlug.MatchString(txt(in["deployment_name"])) {
				return nil, errors.New("deployment name must contain lowercase letters, numbers and hyphens")
			}
			if _, e = setupDeployConfig(ctx, s, in); e != nil {
				return nil, e
			}
		}
		r = &setupRequest{Status: "pending", Marker: "games-setup:" + randomID(), Input: in, Results: map[string]any{}}
		_, e = ctx.AppDB().Exec(`INSERT INTO game_setup_requests VALUES(?,?,?,?,?,?)`, s.ProjectID, s.GameID, key, fp, contentJSON(r), nowRFC())
		if e != nil {
			return nil, e
		}
	}
	fail := func(stage string, err error) (any, error) {
		r.Status = "blocked"
		r.Stage = stage
		r.Error = err.Error()
		if e := saveSetup(ctx, s, key, r); e != nil {
			return nil, e
		}
		return setupResult(r, key), nil
	}
	mutate := func(stage, app, tool string, input map[string]any) (map[string]any, error) {
		r.Status = "dispatching"
		r.Stage = stage
		r.Error = ""
		if e := saveSetup(ctx, s, key, r); e != nil {
			return nil, e
		}
		out, err := studioCall(ctx, s, app, tool, input)
		if err != nil {
			r.Status = "unknown"
			r.Error = err.Error()
			_ = saveSetup(ctx, s, key, r)
			return nil, err
		}
		r.Results[stage] = safeSetupReceipt(out)
		r.Status = "pending"
		if e := saveSetup(ctx, s, key, r); e != nil {
			return nil, e
		}
		return out, nil
	}
	if r.Results["repository"] == nil {
		if in["create_repo"] == true {
			if _, err := mutate("repository", "code", "repos_create", map[string]any{"slug": in["repo_slug"], "name": in["repo_name"], "framework": "blank", "description": r.Marker}); err != nil {
				return setupResult(r, key), nil
			}
		} else {
			repo, err := studioRepo(ctx, s, txt(in["repo_slug"]))
			if err != nil {
				return fail("repository", err)
			}
			r.Results["repository"] = safeSetupReceipt(map[string]any{"repository": repo})
			if e = saveSetup(ctx, s, key, r); e != nil {
				return nil, e
			}
		}
	}
	source, err := studioSourceSet(ctx, s, map[string]any{"repo_slug": in["repo_slug"]})
	if err != nil {
		return fail("source", err)
	}
	r.Results["source"] = source
	if e = saveSetup(ctx, s, key, r); e != nil {
		return nil, e
	}
	deploymentID := number(in["deployment_id"])
	if deploymentID > 0 {
		existing, err := studioDeployment(ctx, s, deploymentID, "production")
		if err != nil {
			return fail("deployment", err)
		}
		d := object(existing["deployment"])
		kind := txt(in["platform"])
		if kind == "desktop" || kind == "steam" {
			kind = "artifact"
		}
		if d["source_kind"] != "code" || d["source_ref"] != in["repo_slug"] || d["target_kind"] != kind {
			return fail("deployment", errors.New("existing deployment must match selected source and platform"))
		}
	}
	if deploymentID == 0 {
		if r.Results["deployment"] == nil {
			cfg, err := setupDeployConfig(ctx, s, in)
			if err != nil {
				return fail("configuration", err)
			}
			cfg["name"] = in["deployment_name"]
			cfg["description"] = r.Marker
			cfg["source_kind"] = "code"
			cfg["source_ref"] = in["repo_slug"]
			kind := txt(in["platform"])
			if kind == "desktop" || kind == "steam" {
				kind = "artifact"
			}
			cfg["target_kind"] = kind
			if _, err = mutate("deployment", "deploy", "deploy_init", cfg); err != nil {
				return setupResult(r, key), nil
			}
		}
		deploymentID = number(object(object(r.Results["deployment"])["deployment"])["id"])
		if deploymentID <= 0 {
			r.Status = "unknown"
			r.Stage = "deployment"
			r.Error = "Deploy did not return a deployment ID"
			if e = saveSetup(ctx, s, key, r); e != nil {
				return nil, e
			}
			return setupResult(r, key), nil
		}
	}
	env := txt(in["environment"])
	if env != "production" && in["create_environment"] == true && r.Results["environment"] == nil {
		if _, err = mutate("environment", "deploy", "deploy_env_create", map[string]any{"id": deploymentID, "environment": env, "description": r.Marker}); err != nil {
			return setupResult(r, key), nil
		}
	}
	target, err := studioTargetSet(ctx, s, map[string]any{"source_id": object(source)["id"], "deployment_id": deploymentID, "environment": env, "platform": in["platform"]})
	if err != nil {
		return fail("association", err)
	}
	r.Results["target"] = target
	r.Status = "complete"
	r.Stage = "association"
	r.Error = ""
	if e = saveSetup(ctx, s, key, r); e != nil {
		return nil, e
	}
	return setupResult(r, key), nil
}
func studioSetupReconcile(ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	setupMu.Lock()
	defer setupMu.Unlock()
	if args["confirm"] != true {
		return nil, errors.New("confirm required")
	}
	key, e := requiredText(args, "request_key")
	if e != nil {
		return nil, e
	}
	r, _, e := setupRecord(ctx, s, key)
	if e != nil {
		return nil, e
	}
	if r.Status != "unknown" && r.Status != "dispatching" {
		return nil, errors.New("setup has no uncertain mutation")
	}
	if args["resolution"] == "not_created" {
		if strings.TrimSpace(txt(args["reason"])) == "" {
			return nil, errors.New("write the verified reason no resource was created")
		}
		r.Status = "blocked"
		r.Error = txt(args["reason"])
	} else {
		var out map[string]any
		switch r.Stage {
		case "repository":
			repo, err := studioRepo(ctx, s, txt(r.Input["repo_slug"]))
			if err != nil {
				return nil, err
			}
			if txt(repo["description"]) != r.Marker {
				return nil, errors.New("repository does not carry this setup receipt")
			}
			out = map[string]any{"repository": repo}
		case "deployment":
			out, e = studioDeployment(ctx, s, number(args["deployment_id"]), "production")
			if e != nil {
				return nil, e
			}
			d := object(out["deployment"])
			if txt(d["description"]) != r.Marker || d["source_ref"] != r.Input["repo_slug"] {
				return nil, errors.New("deployment does not carry this setup receipt")
			}
		case "environment":
			id := number(r.Input["deployment_id"])
			if id == 0 {
				id = number(object(object(r.Results["deployment"])["deployment"])["id"])
			}
			out, e = studioDeployment(ctx, s, id, txt(r.Input["environment"]))
			if e != nil {
				return nil, e
			}
			matched := false
			for _, env := range records(out["environments"]) {
				if env["name"] == r.Input["environment"] && env["description"] == r.Marker {
					matched = true
				}
			}
			if !matched {
				return nil, errors.New("environment does not carry this setup receipt")
			}
		default:
			return nil, errors.New("unsupported setup stage")
		}
		r.Results[r.Stage] = safeSetupReceipt(out)
		r.Status = "pending"
		r.Error = ""
	}
	if e = saveSetup(ctx, s, key, r); e != nil {
		return nil, e
	}
	return setupResult(r, key), nil
}
func studioPin(ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	if _, e := studioBinding(ctx, "code"); e != nil {
		return nil, e
	}
	if _, e := studioRepo(ctx, s, txt(args["repo_slug"])); e != nil {
		return nil, e
	}
	return studioCall(ctx, s, "code", "repos_export", map[string]any{"slug": args["repo_slug"], "metadata_only": true})
}

func array(v any) []any {
	switch a := v.(type) {
	case []any:
		return a
	case []string:
		out := []any{}
		for _, s := range a {
			out = append(out, s)
		}
		return out
	}
	return nil
}
func renderRecipeValue(v any, params map[string]string) any {
	switch x := v.(type) {
	case string:
		for k, p := range params {
			x = strings.ReplaceAll(x, "{{"+k+"}}", p)
		}
		return x
	case []any:
		for i, y := range x {
			x[i] = renderRecipeValue(y, params)
		}
		return x
	case map[string]any:
		for k, y := range x {
			x[k] = renderRecipeValue(y, params)
		}
		return x
	}
	return v
}

func studioConfigure(ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	setupMu.Lock()
	defer setupMu.Unlock()
	link, out, e := studioTarget(ctx, s, txt(args["target_id"]))
	if e != nil {
		return nil, e
	}
	in := object(args["setup"])
	if in == nil {
		return nil, errors.New("configuration required")
	}
	in["platform"] = link["platform"]
	update, e := setupDeployConfig(ctx, s, in)
	if e != nil {
		return nil, e
	}
	current := jsonObject(object(out["deployment"])["target_config_json"])
	fresh := jsonObject(update["target_config_json"])
	if _, provided := in["version_strategy"]; !provided {
		delete(fresh, "version_strategy")
	}
	for k, v := range fresh {
		current[k] = v
	}
	// Omitted identity selections and policy keep their canonical Deploy values.
	update["target_config_json"] = contentJSON(current)
	update["id"] = link["deployment_id"]
	update["environment"] = link["environment"]
	// Updating configuration sets values and creates no new resources; a retry is safe.
	return studioCall(ctx, s, "deploy", "deploy_update", update)
}
