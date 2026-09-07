package main

// Studio owns game associations, never the downstream build or publisher state.
import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const studioSchema = `
CREATE TABLE IF NOT EXISTS game_studio_links (
 project_id TEXT NOT NULL, game_id TEXT NOT NULL, kind TEXT NOT NULL, id TEXT NOT NULL,
 data TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,game_id,kind,id), FOREIGN KEY(project_id,game_id) REFERENCES games(project_id,id));
CREATE UNIQUE INDEX IF NOT EXISTS game_target_owner ON game_studio_links(project_id,json_extract(data,'$.install_id'),json_extract(data,'$.deployment_id'),json_extract(data,'$.environment')) WHERE kind='target';
CREATE TABLE IF NOT EXISTS game_delivery_requests (
 project_id TEXT NOT NULL, game_id TEXT NOT NULL, target_id TEXT NOT NULL, request_key TEXT NOT NULL,
 action TEXT NOT NULL, fingerprint TEXT NOT NULL, status TEXT NOT NULL, result TEXT NOT NULL DEFAULT '{}',
 error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,game_id,request_key), FOREIGN KEY(project_id,game_id) REFERENCES games(project_id,id));
CREATE UNIQUE INDEX IF NOT EXISTS game_delivery_pending ON game_delivery_requests(project_id,game_id,target_id) WHERE status IN ('dispatching','unknown');
CREATE TABLE IF NOT EXISTS game_metric_syncs (
 project_id TEXT NOT NULL, game_id TEXT NOT NULL, source_id TEXT NOT NULL,
 last_success TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', next_attempt TEXT NOT NULL DEFAULT '',
 lease_until TEXT NOT NULL DEFAULT '', lease_token TEXT NOT NULL DEFAULT '', failures INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(project_id,game_id,source_id), FOREIGN KEY(project_id,game_id) REFERENCES games(project_id,id));
CREATE TABLE IF NOT EXISTS game_telemetry_receipts (
 project_id TEXT NOT NULL, game_id TEXT NOT NULL, player_id INTEGER NOT NULL, event_id TEXT NOT NULL,
 fingerprint TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,game_id,player_id,event_id));
CREATE INDEX IF NOT EXISTS game_telemetry_retention ON game_telemetry_receipts(project_id,created_at);
CREATE INDEX IF NOT EXISTS game_outbox_game_analytics ON game_outbox(project_id,game_id,analytics);
CREATE TABLE IF NOT EXISTS game_studio_identity (id INTEGER PRIMARY KEY CHECK(id=1), namespace TEXT NOT NULL);
`

func initializeStudio(ctx *sdk.AppCtx) error {
	if _, err := ctx.AppDB().Exec(studioSchema); err != nil {
		return err
	}
	_, err := ctx.AppDB().Exec(`INSERT OR IGNORE INTO game_studio_identity(id,namespace) VALUES(1,?)`, randomID())
	return err
}
func studioHash(v any) string { b, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(b)) }
func studioScope(ctx *sdk.AppCtx, args map[string]any) (GameScope, error) {
	if stringArg(args, "game_id", "") == "" {
		return GameScope{}, errors.New("game_id required")
	}
	p, e := resolveProjectFromArgs(args)
	if e != nil {
		return GameScope{}, e
	}
	g, e := getGame(ctx.AppDB(), p, txt(args["game_id"]))
	if e != nil {
		return GameScope{}, e
	}
	return g.Scope(), nil
}
func studioCall(ctx *sdk.AppCtx, scope GameScope, app, tool string, in map[string]any) (map[string]any, error) {
	if in == nil {
		in = map[string]any{}
	}
	in["_project_id"] = scope.ProjectID
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult(app, tool, in, &out); err != nil {
		return nil, err
	}
	return out, nil
}
func studioBinding(ctx *sdk.AppCtx, role string) (int64, error) {
	b := ctx.IntegrationFor(role)
	if b == nil || b.Kind != "app" || b.InstallID <= 0 {
		return 0, fmt.Errorf("connect the %s app in Games settings", role)
	}
	return b.InstallID, nil
}
func linkPut(ctx *sdk.AppCtx, s GameScope, kind, id string, data any) error {
	b, e := json.Marshal(data)
	if e != nil {
		return e
	}
	_, e = ctx.AppDB().Exec(`INSERT INTO game_studio_links VALUES(?,?,?,?,?,?) ON CONFLICT(project_id,game_id,kind,id) DO UPDATE SET data=excluded.data,updated_at=excluded.updated_at`, s.ProjectID, s.GameID, kind, id, string(b), nowRFC())
	return e
}
func linkGet(ctx *sdk.AppCtx, s GameScope, kind, id string) (map[string]any, error) {
	var raw string
	e := ctx.AppDB().QueryRow(`SELECT data FROM game_studio_links WHERE project_id=? AND game_id=? AND kind=? AND id=?`, s.ProjectID, s.GameID, kind, id).Scan(&raw)
	if e != nil {
		return nil, fmt.Errorf("%s link not found for this game", kind)
	}
	var v map[string]any
	e = json.Unmarshal([]byte(raw), &v)
	return v, e
}
func linksList(ctx *sdk.AppCtx, s GameScope, kind string) ([]map[string]any, error) {
	rows, e := ctx.AppDB().Query(`SELECT data FROM game_studio_links WHERE project_id=? AND game_id=? AND kind=? ORDER BY id LIMIT 100`, s.ProjectID, s.GameID, kind)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		var v map[string]any
		if e = json.Unmarshal([]byte(raw), &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func records(v any) []map[string]any {
	out := []map[string]any{}
	if a, ok := v.([]any); ok {
		for _, x := range a {
			if m := object(x); m != nil {
				out = append(out, m)
			}
		}
	}
	return out
}
func number(v any) int64 {
	switch x := v.(type) {
	case float64:
		if x == float64(int64(x)) {
			return int64(x)
		}
	case int64:
		return x
	case int:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	}
	return 0
}
func txt(v any) string { s, _ := v.(string); return s }
func requiredText(args map[string]any, key string) (string, error) {
	s := strings.TrimSpace(txt(args[key]))
	if s == "" || len(s) > 256 {
		return "", fmt.Errorf("%s required (maximum 256 characters)", key)
	}
	return s, nil
}

func studioRepo(ctx *sdk.AppCtx, s GameScope, slug string) (map[string]any, error) {
	out, e := studioCall(ctx, s, "code", "repos_get", map[string]any{"slug": slug})
	if e != nil {
		return nil, e
	}
	r := object(out["repository"])
	if r == nil || number(r["id"]) <= 0 || txt(r["project_id"]) != s.ProjectID || txt(r["archived_at"]) != "" {
		return nil, errors.New("repository not active in this project")
	}
	return r, nil
}
func studioSourceSet(ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	install, e := studioBinding(ctx, "code")
	if e != nil {
		return nil, e
	}
	slug, e := requiredText(args, "repo_slug")
	if e != nil {
		return nil, e
	}
	r, e := studioRepo(ctx, s, slug)
	if e != nil {
		return nil, e
	}
	id := fmt.Sprint(number(r["id"]))
	link := map[string]any{"id": id, "repo_id": r["id"], "repo_slug": r["slug"], "name": r["name"], "install_id": install, "role": stringArg(args, "role", "client")}
	return link, linkPut(ctx, s, "source", id, link)
}
func studioSource(ctx *sdk.AppCtx, s GameScope, id string) (map[string]any, error) {
	link, e := linkGet(ctx, s, "source", id)
	if e != nil {
		return nil, e
	}
	install, e := studioBinding(ctx, "code")
	if e != nil {
		return nil, e
	}
	if install != number(link["install_id"]) {
		return nil, errors.New("Code installation changed; reattach repository")
	}
	r, e := studioRepo(ctx, s, txt(link["repo_slug"]))
	if e != nil {
		return nil, e
	}
	if number(r["id"]) != number(link["repo_id"]) {
		return nil, errors.New("repository identity changed; reattach source")
	}
	return link, nil
}
func studioDeployment(ctx *sdk.AppCtx, s GameScope, id int64, environment string) (map[string]any, error) {
	out, e := studioCall(ctx, s, "deploy", "deploy_get", map[string]any{"id": id, "environment": environment})
	if e != nil {
		return nil, e
	}
	d := object(out["deployment"])
	if d == nil || number(d["id"]) != id || txt(d["project_id"]) != s.ProjectID || txt(d["archived_at"]) != "" || txt(d["environment"]) != environment {
		return nil, errors.New("deployment/environment not active in this project")
	}
	found := false
	for _, env := range records(out["environments"]) {
		if txt(env["name"]) == environment && txt(env["archived_at"]) == "" {
			out["environment_id"] = env["id"]
			found = true
		}
	}
	if !found {
		return nil, errors.New("environment identity unavailable")
	}
	return out, nil
}
func studioTargetSet(ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	install, e := studioBinding(ctx, "deploy")
	if e != nil {
		return nil, e
	}
	source, e := studioSource(ctx, s, txt(args["source_id"]))
	if e != nil {
		return nil, e
	}
	id := number(args["deployment_id"])
	env := stringArg(args, "environment", "production")
	out, e := studioDeployment(ctx, s, id, env)
	if e != nil {
		return nil, e
	}
	d := object(out["deployment"])
	if d["source_kind"] != "code" || d["source_ref"] != source["repo_slug"] {
		return nil, errors.New("target must use the linked Code repository")
	}
	platform := txt(args["platform"])
	kind := txt(d["target_kind"])
	if !((platform == "android" && kind == "android") || (platform == "ios" && kind == "ios") || ((platform == "steam" || platform == "desktop") && kind == "artifact")) {
		return nil, errors.New("platform does not match Deploy target kind")
	}
	// One deployment/environment has one game owner, even if repositories are shared.
	var owners int
	e = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM game_studio_links WHERE project_id=? AND kind='target' AND game_id<>? AND json_extract(data,'$.install_id')=? AND json_extract(data,'$.deployment_id')=? AND json_extract(data,'$.environment')=?`, s.ProjectID, s.GameID, install, id, env).Scan(&owners)
	if e != nil {
		return nil, e
	}
	if owners > 0 {
		return nil, errors.New("target is already linked to another game")
	}
	key := fmt.Sprintf("%d:%s", id, env)
	link := map[string]any{"id": key, "deployment_id": id, "environment": env, "environment_id": out["environment_id"], "install_id": install, "source_id": source["id"], "platform": platform, "name": d["name"]}
	return link, linkPut(ctx, s, "target", key, link)
}
func studioTarget(ctx *sdk.AppCtx, s GameScope, id string, verify ...bool) (map[string]any, map[string]any, error) {
	link, e := linkGet(ctx, s, "target", id)
	if e != nil {
		return nil, nil, e
	}
	install, e := studioBinding(ctx, "deploy")
	if e != nil {
		return nil, nil, e
	}
	if install != number(link["install_id"]) {
		return nil, nil, errors.New("Deploy installation changed; reattach target")
	}
	strict := len(verify) == 0 || verify[0]
	var source map[string]any
	if strict {
		source, e = studioSource(ctx, s, txt(link["source_id"]))
	} else {
		source, e = linkGet(ctx, s, "source", txt(link["source_id"]))
	}
	if e != nil {
		return nil, nil, e
	}
	out, e := studioDeployment(ctx, s, number(link["deployment_id"]), txt(link["environment"]))
	if e != nil {
		return nil, nil, e
	}
	d := object(out["deployment"])
	if number(out["environment_id"]) != number(link["environment_id"]) {
		return nil, nil, errors.New("target source/environment changed; reattach after checking configuration")
	}
	if d["source_kind"] != "code" || d["source_ref"] != source["repo_slug"] {
		if strict {
			return nil, nil, errors.New("target source changed; reattach after checking configuration")
		}
		out["link_warning"] = "Target source changed; reattach before delivery"
	}
	return link, out, nil
}
func targetArgs(link map[string]any) map[string]any {
	return map[string]any{"id": link["deployment_id"], "environment": link["environment"]}
}
func studioHistory(ctx *sdk.AppCtx, s GameScope, limit, offset int) ([]map[string]any, error) {
	rows, e := ctx.AppDB().Query(`SELECT target_id,request_key,action,status,result,error,created_at FROM game_delivery_requests WHERE project_id=? AND game_id=? ORDER BY created_at DESC,request_key LIMIT ? OFFSET ?`, s.ProjectID, s.GameID, limit, offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var target, key, action, status, raw, err, created string
		if e = rows.Scan(&target, &key, &action, &status, &raw, &err, &created); e != nil {
			return nil, e
		}
		var result any
		_ = json.Unmarshal([]byte(raw), &result)
		out = append(out, map[string]any{"target_id": target, "request_key": key, "action": action, "status": status, "result": result, "error": err, "created_at": created})
	}
	return out, rows.Err()
}
func studioDispatch(ctx *sdk.AppCtx, s GameScope, action string, args map[string]any) (any, error) {
	key, e := requiredText(args, "request_key")
	if e != nil {
		return nil, e
	}
	targetID := txt(args["target_id"])
	// Replay before dependency calls; a dependency outage must not lose an existing receipt.
	fingerprint := studioHash(map[string]any{"action": action, "target_id": targetID, "build_id": args["build_id"], "release_id": args["release_id"], "channel": args["channel"], "fraction": args["fraction"], "rollout_fraction": args["rollout_fraction"], "submit_for_review": args["submit_for_review"], "beta_group_id": args["beta_group_id"], "release_notes": args["release_notes"]})
	var oldFP, status, raw string
	e = ctx.AppDB().QueryRow(`SELECT fingerprint,status,result FROM game_delivery_requests WHERE project_id=? AND game_id=? AND request_key=?`, s.ProjectID, s.GameID, key).Scan(&oldFP, &status, &raw)
	if e == nil {
		if oldFP != fingerprint {
			return nil, errors.New("request key already used for different inputs")
		}
		var result any
		_ = json.Unmarshal([]byte(raw), &result)
		return map[string]any{"status": status, "result": result, "request_key": key}, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	link, out, e := studioTarget(ctx, s, targetID)
	if e != nil {
		return nil, e
	}
	in := targetArgs(link)
	tool := "deploy_build"
	if action == "release" {
		tool = "deploy_release"
		if !ownedRemoteRecord(out, "builds", number(args["build_id"]), link) {
			return nil, errors.New("select a retained build from this game's target")
		}
		in["build_id"] = args["build_id"]
		in["channel"] = args["channel"]
	}
	if action == "promote" {
		tool = "deploy_promote"
		if !ownedRemoteRecord(out, "releases", number(args["release_id"]), link) {
			return nil, errors.New("select a retained release from this game's target")
		}
		in["release_id"] = args["release_id"]
		in["target_channel"] = args["channel"]
		in["target_environment"] = link["environment"]
	}
	if action == "halt" || action == "rollout" {
		if !ownedRemoteRecord(out, "releases", number(args["release_id"]), link) {
			return nil, errors.New("select a release from this game's target")
		}
		if link["platform"] != "android" && link["platform"] != "ios" {
			return nil, errors.New("rollout/halt controls are only available for mobile targets")
		}
		if action == "rollout" && link["platform"] != "android" {
			return nil, errors.New("staged rollout requires Android")
		}
		if action == "halt" && link["platform"] == "ios" {
			for _, r := range records(out["releases"]) {
				if number(r["id"]) == number(args["release_id"]) && r["channel"] != "internal" && r["channel"] != "external" {
					return nil, errors.New("only TestFlight builds can be expired here; this does not roll back public iOS apps")
				}
			}
		}
		tool = "deploy_" + action
		in["release_id"] = args["release_id"]
		if action == "rollout" {
			fraction, ok := floatArg(args, "fraction")
			if !ok || fraction <= 0 || fraction > 1 {
				return nil, errors.New("fraction must be >0 and <=1")
			}
			in["fraction"] = fraction
		}
	}
	if action == "release" || action == "promote" {
		for _, key := range []string{"rollout_fraction", "submit_for_review", "beta_group_id", "release_notes"} {
			if v, ok := args[key]; ok {
				in[key] = v
			}
		}
	}
	if (action == "release" || action == "promote") && txt(args["channel"]) == "" {
		return nil, errors.New("channel required")
	}
	_, e = ctx.AppDB().Exec(`INSERT INTO game_delivery_requests(project_id,game_id,target_id,request_key,action,fingerprint,status,created_at) VALUES(?,?,?,?,?,?,'dispatching',?)`, s.ProjectID, s.GameID, targetID, key, action, fingerprint, nowRFC())
	if e != nil {
		return nil, errors.New("request already exists or target has an unresolved dispatch; inspect delivery history")
	}
	result, callErr := studioCall(ctx, s, "deploy", tool, in)
	if callErr == nil {
		kind := "release"
		if action == "build" {
			kind = "build"
		}
		if number(object(result[kind])["id"]) <= 0 {
			callErr = errors.New("Deploy returned no operation receipt; reconcile before retrying")
		}
	}
	status = "accepted"
	detail := ""
	if callErr != nil {
		status = "unknown"
		detail = callErr.Error()
	}
	b, _ := json.Marshal(result)
	if result == nil {
		b = []byte("{}")
	}
	if _, e = ctx.AppDB().Exec(`UPDATE game_delivery_requests SET status=?,result=?,error=? WHERE project_id=? AND game_id=? AND request_key=?`, status, string(b), detail, s.ProjectID, s.GameID, key); e != nil {
		return nil, e
	}
	return map[string]any{"status": status, "request_key": key, "result": result, "error": detail}, nil
}
func ownedRemoteRecord(out map[string]any, kind string, id int64, link map[string]any) bool {
	if id <= 0 {
		return false
	}
	for _, r := range records(out[kind]) {
		if number(r["id"]) == id && number(r["deployment_id"]) == number(link["deployment_id"]) && number(r["environment_id"]) == number(link["environment_id"]) {
			return true
		}
	}
	return false
}
func studioReconcile(ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	key := txt(args["request_key"])
	var target, action, status string
	e := ctx.AppDB().QueryRow(`SELECT target_id,action,status FROM game_delivery_requests WHERE project_id=? AND game_id=? AND request_key=?`, s.ProjectID, s.GameID, key).Scan(&target, &action, &status)
	if e != nil {
		return nil, e
	}
	if status != "unknown" && status != "dispatching" {
		return nil, errors.New("request is not awaiting reconciliation")
	}
	if args["resolution"] == "not_created" {
		if args["confirm"] != true || len(strings.TrimSpace(txt(args["reason"]))) < 10 {
			return nil, errors.New("confirm=true and a reason describing verification that no remote operation was created are required")
		}
		result := map[string]any{"resolution": "not_created", "reason": args["reason"]}
		b, _ := json.Marshal(result)
		_, e = ctx.AppDB().Exec(`UPDATE game_delivery_requests SET status='not_created',result=?,error='' WHERE project_id=? AND game_id=? AND request_key=?`, string(b), s.ProjectID, s.GameID, key)
		return result, e
	}
	link, out, e := studioTarget(ctx, s, target, false)
	if e != nil {
		return nil, e
	}
	kind := "releases"
	id := number(args["release_id"])
	if action == "build" {
		kind = "builds"
		id = number(args["build_id"])
	}
	if !ownedRemoteRecord(out, kind, id, link) {
		return nil, errors.New("reconciliation requires the verified provider build/release ID from this target")
	}
	if args["confirm"] != true {
		return nil, errors.New("confirm the selected ID is the result of this request")
	}
	result := map[string]any{"reconciled_id": id, "kind": kind}
	b, _ := json.Marshal(result)
	_, e = ctx.AppDB().Exec(`UPDATE game_delivery_requests SET status='reconciled',result=?,error='' WHERE project_id=? AND game_id=? AND request_key=?`, string(b), s.ProjectID, s.GameID, key)
	return result, e
}

func studioAction(ctx *sdk.AppCtx, action string, args map[string]any) (any, error) {
	if action == "portfolio" {
		return studioPortfolio(ctx, args)
	}
	s, e := studioScope(ctx, args)
	if e != nil {
		return nil, e
	}
	switch action {
	case "source_set", "target_set", "build", "release", "promote", "halt", "rollout", "reconcile", "metric_source_set", "metrics_sync", "store_update":
		if e = checkActiveGame(ctx.AppDB(), s); e != nil {
			return nil, e
		}
	}
	switch action {
	case "source_set":
		return studioSourceSet(ctx, s, args)
	case "sources":
		return linksList(ctx, s, "source")
	case "target_set":
		return studioTargetSet(ctx, s, args)
	case "targets":
		return linksList(ctx, s, "target")
	case "history":
		return studioHistory(ctx, s, boundedArg(args, "limit", 25, 1, 100), boundedArg(args, "offset", 0, 0, 100000))
	case "build", "release", "promote", "halt", "rollout":
		return studioDispatch(ctx, s, action, args)
	case "reconcile":
		return studioReconcile(ctx, s, args)
	case "release_plan", "release_status", "store_get", "store_update", "release_sync", "logs":
		link, out, e := studioTarget(ctx, s, txt(args["target_id"]), false)
		if e != nil {
			return nil, e
		}
		in := targetArgs(link)
		if action == "logs" {
			kind := "builds"
			key := "build_id"
			if number(args[key]) == 0 {
				kind = "releases"
				key = "release_id"
			}
			if !ownedRemoteRecord(out, kind, number(args[key]), link) {
				return nil, errors.New("log record does not belong to this target")
			}
			in[key] = args[key]
			in["tail"] = 200
			return studioCall(ctx, s, "deploy", "deploy_logs", in)
		}
		if action == "release_status" {
			summary := map[string]any{"refreshed_at": nowRFC(), "release_status": "No release"}
			if builds := records(out["builds"]); len(builds) > 0 {
				summary["build_id"] = builds[0]["id"]
				summary["build_status"] = builds[0]["status"]
			}
			if releases := records(out["releases"]); len(releases) > 0 {
				summary["release_id"] = releases[0]["id"]
				summary["release_status"] = releases[0]["status"]
			}
			link["summary"] = summary
			if e = linkPut(ctx, s, "target", txt(link["id"]), link); e != nil {
				return nil, e
			}
			return out, nil
		}
		if action == "release_plan" {
			out["requirements"] = []string{"Code >=0.10.0", "Deploy >=0.26.0", "configured toolchain and final-artifact tests"}
			if link["platform"] == "android" || link["platform"] == "ios" {
				plan, err := studioCall(ctx, s, "deploy", "deploy_store_preflight", in)
				out["store_preflight"] = plan
				if err != nil {
					out["store_error"] = err.Error()
				}
			}
			return out, nil
		}
		if action == "release_sync" {
			if !ownedRemoteRecord(out, "releases", number(args["release_id"]), link) {
				return nil, errors.New("release not found for this target")
			}
			in["release_id"] = args["release_id"]
			return studioCall(ctx, s, "deploy", "deploy_release_sync", in)
		}
		if action == "store_update" {
			in["desired_json"] = args["store_config_json"]
		}
		return studioCall(ctx, s, "deploy", "deploy_"+action, in)
	case "metric_source_set":
		return metricSourceSet(ctx, s, args)
	case "metric_sources":
		return metricSources(ctx, s)
	case "metrics_sync":
		return syncMetricSource(ctx, s, txt(args["source_id"]), boundedArg(args, "days", 7, 1, 31))
	case "metrics_query":
		return gameMetricsQuery(ctx, s, args)
	case "discovery":
		app := txt(args["app"])
		if app == "code" || app == "deploy" {
			if _, e = studioBinding(ctx, app); e != nil {
				return nil, e
			}
			tool := "repos_list"
			if app == "deploy" {
				tool = "deploy_list"
			}
			return studioCall(ctx, s, app, tool, nil)
		}
		return metricDiscover(ctx, s, args)
	}
	return nil, errors.New("unknown studio action")
}
func boundedArg(args map[string]any, key string, def, low, high int) int {
	if _, ok := args[key]; !ok {
		return def
	}
	n := int(number(args[key]))
	return max(low, min(high, n))
}
func studioPortfolio(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	p, e := resolveProjectFromArgs(args)
	if e != nil {
		return nil, e
	}
	gs, e := listGames(ctx.AppDB(), p)
	if e != nil {
		return nil, e
	}
	offset := boundedArg(args, "offset", 0, 0, len(gs))
	limit := boundedArg(args, "limit", 25, 1, 100)
	out := []map[string]any{}
	for _, g := range gs[offset:min(len(gs), offset+limit)] {
		sources, e := linksList(ctx, g.Scope(), "source")
		if e != nil {
			return nil, e
		}
		targets, e := linksList(ctx, g.Scope(), "target")
		if e != nil {
			return nil, e
		}
		metrics, e := metricSources(ctx, g.Scope())
		if e != nil {
			return nil, e
		}
		history, e := studioHistory(ctx, g.Scope(), 5, 0)
		if e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"game": g, "sources": sources, "targets": targets, "metric_sources": metrics, "deliveries": history})
	}
	return map[string]any{"games": out, "total": len(gs), "offset": offset, "limit": limit}, nil
}
func (a *App) handleStudio(w http.ResponseWriter, r *http.Request) {
	args := map[string]any{}
	action := r.PathValue("action")
	if r.Method == "GET" && !(action == "portfolio" || action == "sources" || action == "targets" || action == "history" || action == "release_status" || action == "release_plan" || action == "store_get" || action == "metric_sources" || action == "metrics_query" || action == "discovery" || action == "logs") {
		httpErr(w, 405, "use POST for this action")
		return
	}
	if r.Method == "POST" {
		if e := decodeBody(w, r, &args); e != nil {
			httpErr(w, 400, e.Error())
			return
		}
		if args == nil {
			httpErr(w, 400, "JSON object required")
			return
		}
	}
	p, e := resolveProjectFromRequest(r)
	if e != nil {
		httpErr(w, 400, e.Error())
		return
	}
	args["_project_id"] = p
	args["game_id"] = r.PathValue("game_id")
	for _, k := range []string{"target_id", "source_id", "app"} {
		if v := r.URL.Query().Get(k); v != "" {
			args[k] = v
		}
	}
	for _, k := range []string{"limit", "offset"} {
		if v, e := strconv.Atoi(r.URL.Query().Get(k)); e == nil {
			args[k] = v
		}
	}
	out, e := studioAction(getAppCtx(r), r.PathValue("action"), args)
	if e != nil {
		httpErr(w, 400, e.Error())
		return
	}
	httpJSON(w, map[string]any{"data": out})
}
func studioTools() []sdk.Tool {
	specs := []struct{ name, action, description string }{
		{"games_source_set", "source_set", "Link an existing Code repository to this game."}, {"games_sources_list", "sources", "List this game's source links."},
		{"games_target_set", "target_set", "Link an existing Deploy environment to the game's source."}, {"games_targets_list", "targets", "List this game's platform targets."},
		{"games_release_plan", "release_plan", "Read target configuration, build evidence and store readiness."}, {"games_build", "build", "Request a build once using request_key; unknown results require reconciliation."},
		{"games_release", "release", "Publish an exact build through Deploy policy; channel and request_key required."}, {"games_promote", "promote", "Promote an exact release through Deploy policy."},
		{"games_halt", "halt", "Halt an Android rollout or expire a TestFlight build; requires a request_key."}, {"games_rollout", "rollout", "Change an Android rollout fraction through Deploy policy; requires a request_key."}, {"games_logs", "logs", "Read recent build/release logs for a linked target."},
		{"games_release_status", "release_status", "Read builds, releases and availability for a linked target."}, {"games_release_sync", "release_sync", "Refresh provider observations for an exact linked release."},
		{"games_delivery_history", "history", "Page through game delivery requests."}, {"games_delivery_reconcile", "reconcile", "Resolve an ambiguous request with a verified ID and confirm=true."},
		{"games_store_get", "store_get", "Read canonical Deploy store metadata."}, {"games_store_update", "store_update", "Explicitly update canonical Deploy store metadata."},
		{"games_metric_source_set", "metric_source_set", "Connect an authorized reporting source to this game."}, {"games_metric_sources", "metric_sources", "Read reporting sources and refresh status."},
		{"games_metrics_sync", "metrics_sync", "Import up to 31 days with deduplication."}, {"games_metrics_query", "metrics_query", "Query Analytics with enforced game scope."},
		{"games_studio_discover", "discovery", "Discover bound repositories, deployments or provider inventory."}, {"games_portfolio", "portfolio", "Read a paginated portfolio without calling external providers."},
	}
	out := []sdk.Tool{}
	for _, s := range specs {
		spec := s
		props := map[string]any{}
		for _, k := range []string{"game_id", "repo_slug", "role", "source_id", "target_id", "environment", "platform", "request_key", "channel", "app", "provider", "external_id", "account_id", "stream_id", "currency", "timezone", "family", "topic", "store_config_json", "resolution", "reason", "page_token", "beta_group_id"} {
			props[k] = map[string]any{"type": "string"}
		}
		for _, k := range []string{"deployment_id", "build_id", "release_id", "connection_id", "days", "limit", "offset", "since", "until"} {
			props[k] = map[string]any{"type": "integer"}
		}
		props["confirm"] = map[string]any{"type": "boolean"}
		props["fraction"] = map[string]any{"type": "number"}
		props["rollout_fraction"] = map[string]any{"type": "number"}
		props["submit_for_review"] = map[string]any{"type": "boolean"}
		props["release_notes"] = map[string]any{"type": "object"}
		props["config"] = map[string]any{"type": "object"}
		props["where"] = map[string]any{"type": "object"}
		req := []string{"game_id"}
		if s.action == "portfolio" {
			req = nil
		}
		out = append(out, sdk.Tool{Name: spec.name, Description: spec.description, InputSchema: schemaObject(props, req), Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) { return studioAction(ctx, spec.action, args) }})
	}
	return out
}
