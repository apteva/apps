package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gorilla/websocket"
)

// The opt-in end-to-end test for database_pipeline: Database 0.6.0 on
// AptevaDB behind the GraphQL sidecar, a stand-in Auth /me for verified
// identities, and an independent Go oracle over the generated records.
//
//	GRAPHQL_TEST_DATABASE_DIR=<database 0.6.0>/mcp/database GOWORK=off go test -run TestRealDatabasePipeline -v
//
// GRAPHQL_BENCH_DB_ROWS sets the number of calls (default 50,000).

type pipelineCall struct {
	agent, team, caller string
	answered            bool
	talk                int
	started             time.Time
}

type pipelineSale struct {
	agent, team string
	amount      float64
	sold        time.Time
}

type pipelineFixture struct {
	calls  []pipelineCall
	sales  []pipelineSale
	agents map[string]string // agent -> team
	teams  map[string]string // team -> name
	active map[string]bool
}

func newPipelineFixture(rows int) *pipelineFixture {
	f := &pipelineFixture{agents: map[string]string{}, teams: map[string]string{}, active: map[string]bool{}}
	for team := 1; team <= 4; team++ {
		id := fmt.Sprintf("t%d", team)
		f.teams[id] = "Team " + strconv.Itoa(team)
		f.active[id] = team != 4
	}
	for agent := 0; agent < 40; agent++ {
		f.agents[fmt.Sprintf("a%02d", agent)] = fmt.Sprintf("t%d", agent/10+1)
	}
	// Calls span 28 Sep - 3 Nov 2026 (UTC), across the 25 Oct Paris DST change.
	start := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	span := 36 * 24 * time.Hour
	for i := 0; i < rows; i++ {
		agent := fmt.Sprintf("a%02d", i%40)
		answered := i%4 != 0
		talk := 0
		if answered {
			talk = i % 300
		}
		started := start.Add(time.Duration(int64(span) / int64(rows) * int64(i))).Truncate(time.Second)
		f.calls = append(f.calls, pipelineCall{agent, f.agents[agent], fmt.Sprintf("c%04d", (i*7)%3000), answered, talk, started})
		if i%9 == 0 {
			f.sales = append(f.sales, pipelineSale{agent, f.agents[agent], float64(i%500)/4 + 10, started})
		}
	}
	return f
}

type agentExpect struct {
	calls, callers, answeredCallers, talk, sales int
	revenue                                      float64
}

// expect computes the report for the given teams and window directly.
func (f *pipelineFixture) expect(teams []string, from, to time.Time) (map[string]*agentExpect, *agentExpect, map[string][2]int, map[string]int) {
	in := map[string]bool{}
	for _, team := range teams {
		in[team] = true
	}
	agents := map[string]*agentExpect{}
	total := &agentExpect{}
	callers, answered := map[string]map[string]bool{}, map[string]map[string]bool{}
	allCallers, allAnswered := map[string]bool{}, map[string]bool{}
	dayCalls, dayCallers := map[string]int{}, map[string]map[string]bool{}
	byTeam := map[string]int{}
	paris, _ := time.LoadLocation("Europe/Paris")
	row := func(agent string) *agentExpect {
		if agents[agent] == nil {
			agents[agent] = &agentExpect{}
			callers[agent], answered[agent] = map[string]bool{}, map[string]bool{}
		}
		return agents[agent]
	}
	for _, c := range f.calls {
		if !in[c.team] || c.started.Before(from) || !c.started.Before(to) {
			continue
		}
		r := row(c.agent)
		r.calls++
		total.calls++
		callers[c.agent][c.caller], allCallers[c.caller] = true, true
		if c.answered {
			answered[c.agent][c.caller], allAnswered[c.caller] = true, true
			r.talk += c.talk
			total.talk += c.talk
		}
		day := c.started.In(paris).Format("2006-01-02")
		dayCalls[day]++
		if dayCallers[day] == nil {
			dayCallers[day] = map[string]bool{}
		}
		dayCallers[day][c.caller] = true
		byTeam[f.teams[c.team]]++
	}
	for _, s := range f.sales {
		if !in[s.team] || s.sold.Before(from) || !s.sold.Before(to) {
			continue
		}
		r := row(s.agent)
		r.sales++
		r.revenue += s.amount
		total.sales++
		total.revenue += s.amount
	}
	for agent, r := range agents {
		r.callers, r.answeredCallers = len(callers[agent]), len(answered[agent])
	}
	total.callers, total.answeredCallers = len(allCallers), len(allAnswered)
	days := map[string][2]int{}
	for day, n := range dayCalls {
		days[day] = [2]int{n, len(dayCallers[day])}
	}
	return agents, total, days, byTeam
}

// reportPipelineConfig is the resolver config of Query.report and
// Subscription.liveReport.
func reportPipelineConfig() map[string]any {
	const scope = "$scope.teams"
	params := map[string]any{
		"from":  map[string]any{"from": "$args.from", "type": "datetime", "required": true},
		"to":    map[string]any{"from": "$args.to", "type": "datetime", "required": true},
		"teams": map[string]any{"from": "$args.teamIds", "type": "string_list"},
		"user":  map[string]any{"from": "$identity.subject", "type": "string", "required": true},
	}
	config := map[string]any{"database": "ops", "params": params, "timeout_ms": 30000,
		"access": map[string]any{
			"require_any_permission": []any{"reports:read"},
			"values": map[string]any{"teams": map[string]any{
				"requested":                  "$param.teams",
				"unrestricted_if_permission": "reports:all_teams",
				"default":                    map[string]any{"collection": "teams", "field": "id", "where": map[string]any{"field": "active", "op": "eq", "value": true}},
				"otherwise":                  map[string]any{"collection": "members", "field": "team_id", "where": map[string]any{"field": "user_id", "op": "eq", "value": "$param.user"}},
				"single":                     true,
			}},
		},
	}
	inRange := func(team, field string) map[string]any {
		return map[string]any{"and": []any{
			map[string]any{"field": team, "op": "in", "value": scope},
			map[string]any{"field": field, "op": "gte", "value": "$param.from"},
			map[string]any{"field": field, "op": "lt", "value": "$param.to"},
		}}
	}
	answered := map[string]any{"field": "answered", "op": "eq", "value": true}
	callMetrics := []any{
		map[string]any{"name": "calls", "op": "count"},
		map[string]any{"name": "callers", "op": "count_distinct", "field": "caller"},
		map[string]any{"name": "answered_callers", "op": "count_distinct", "field": "caller", "where": answered},
		map[string]any{"name": "talk_seconds", "op": "sum", "field": "talk_seconds", "where": answered},
	}
	saleMetrics := []any{
		map[string]any{"name": "sales", "op": "count"},
		map[string]any{"name": "revenue", "op": "sum", "field": "amount"},
	}
	config["steps"] = []any{
		map[string]any{"id": "calls", "op": "aggregate", "max_rows": 100, "args": map[string]any{"collection": "calls", "where": inRange("team_id", "started_at"), "groupBy": []any{"agent_id"}, "metrics": callMetrics}},
		map[string]any{"id": "calls_total", "op": "aggregate", "args": map[string]any{"collection": "calls", "where": inRange("team_id", "started_at"), "metrics": callMetrics}},
		map[string]any{"id": "sales", "op": "aggregate", "max_rows": 100, "args": map[string]any{"collection": "sales", "where": inRange("team_id", "sold_at"), "groupBy": []any{"agent_id"}, "metrics": saleMetrics}},
		map[string]any{"id": "sales_total", "op": "aggregate", "args": map[string]any{"collection": "sales", "where": inRange("team_id", "sold_at"), "metrics": saleMetrics}},
		map[string]any{"id": "daily", "op": "aggregate", "max_rows": 60, "args": map[string]any{
			"collection": "calls", "where": inRange("team_id", "started_at"),
			"buckets": []any{map[string]any{"name": "day", "field": "started_at", "unit": "day", "timezone": "Europe/Paris"}},
			"groupBy": []any{"day"}, "orderBy": []any{map[string]any{"field": "day", "direction": "asc"}},
			"metrics": []any{map[string]any{"name": "calls", "op": "count"}, map[string]any{"name": "callers", "op": "count_distinct", "field": "caller"}},
		}},
		// Chained lookups: call -> agent -> team, grouped by the team's name.
		map[string]any{"id": "by_team", "op": "aggregate", "max_rows": 10, "args": map[string]any{
			"collection": "calls", "where": inRange("team_id", "started_at"),
			"lookups": []any{
				map[string]any{"as": "agent", "collection": "agents", "localField": "agent_id"},
				map[string]any{"as": "team", "collection": "teams", "localField": "agent.team_id"},
			},
			"groupBy": []any{"team.name"}, "orderBy": []any{map[string]any{"field": "team.name", "direction": "asc"}},
			"metrics": []any{map[string]any{"name": "calls", "op": "count"}},
		}},
		map[string]any{"id": "latest", "op": "find", "max_rows": 5, "top_n": true, "args": map[string]any{
			"collection": "calls", "where": inRange("team_id", "started_at"),
			"select": []any{"agent_id", "caller", "started_at"}, "orderBy": []any{map[string]any{"field": "started_at", "direction": "desc"}},
		}},
	}
	config["combine"] = map[string]any{"byAgent": map[string]any{
		"join": []any{"calls", "sales"}, "on": []any{"agent_id"}, "missing": "zero",
		"total": map[string]any{"key": "__total__", "steps": map[string]any{"calls": "calls_total", "sales": "sales_total"}},
		"fields": map[string]any{
			"calls":              "calls.calls",
			"callers":            "calls.callers",
			"answered_callers":   "calls.answered_callers",
			"unanswered_callers": map[string]any{"op": "subtract", "args": []any{"calls.callers", "calls.answered_callers"}},
			"answer_rate":        map[string]any{"op": "safe_divide", "args": []any{"calls.answered_callers", "calls.callers"}},
			"talk_seconds":       "calls.talk_seconds",
			"sales":              "sales.sales",
			"revenue":            "sales.revenue",
			"conversion":         map[string]any{"op": "safe_divide", "args": []any{"sales.sales", "calls.answered_callers"}},
		},
	}}
	config["result"] = map[string]any{"agents": "$combine.byAgent", "daily": "$steps.daily", "byTeam": "$steps.by_team", "latest": "$steps.latest", "teams": "$scope.teams"}
	return config
}

const pipelineReportSDL = `scalar JSON
type Query { report(from: String!, to: String!, teamIds: [String!]): Report! }
type Subscription { liveReport(from: String!, to: String!, teamIds: [String!]): Report! }
type Report { agents: [AgentRow!]! daily: [JSON!]! byTeam: [JSON!]! latest: [JSON!]! teams: [String!] }
type AgentRow { agent_id: String! calls: Int! callers: Int! answered_callers: Int! unanswered_callers: Int! answer_rate: Float talk_seconds: Int sales: Int! revenue: Float conversion: Float }`

const pipelineReportSelection = `{ agents { agent_id calls callers answered_callers unanswered_callers answer_rate talk_seconds sales revenue conversion } daily byTeam latest teams }`

func pipelineToken(user string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"u":%q}`, time.Now().Add(time.Hour).Unix(), user)))
	return "e30." + payload + ".sig"
}

func TestRealDatabasePipeline(t *testing.T) {
	databaseDir := os.Getenv("GRAPHQL_TEST_DATABASE_DIR")
	if testing.Short() || databaseDir == "" {
		t.Skip("set GRAPHQL_TEST_DATABASE_DIR to Database 0.6.0 source")
	}
	rows := 50000
	if value, err := strconv.Atoi(os.Getenv("GRAPHQL_BENCH_DB_ROWS")); err == nil && value > 0 {
		rows = value
	}
	const project = "graphql-database-pipeline-test"
	database := tk.SpawnSidecar(t, databaseDir, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	operator := func(tool string, args map[string]any) map[string]any {
		args["database"] = "ops"
		return database.MCPAs(tool, args, 1, "", project)
	}
	operator("db_database_create", map[string]any{"adapter": "aptevadb"})
	schemas := map[string][]any{
		"teams":   {map[string]any{"name": "id", "type": "text"}, map[string]any{"name": "name", "type": "text"}, map[string]any{"name": "active", "type": "boolean"}},
		"agents":  {map[string]any{"name": "id", "type": "text"}, map[string]any{"name": "team_id", "type": "text"}},
		"members": {map[string]any{"name": "user_id", "type": "text"}, map[string]any{"name": "team_id", "type": "text"}},
		"calls": {map[string]any{"name": "agent_id", "type": "text"}, map[string]any{"name": "team_id", "type": "text"}, map[string]any{"name": "caller", "type": "text"},
			map[string]any{"name": "answered", "type": "boolean"}, map[string]any{"name": "talk_seconds", "type": "integer"}, map[string]any{"name": "started_at", "type": "datetime"}},
		"sales": {map[string]any{"name": "agent_id", "type": "text"}, map[string]any{"name": "team_id", "type": "text"}, map[string]any{"name": "amount", "type": "number"}, map[string]any{"name": "sold_at", "type": "datetime"}},
	}
	for name, fields := range schemas {
		args := map[string]any{"collection": name, "fields": fields}
		if name == "teams" || name == "agents" {
			args["primaryKey"] = []any{"id"}
		}
		operator("db_collection_create", args)
	}
	f := newPipelineFixture(rows)
	insert := func(collection string, records []any) {
		for offset := 0; offset < len(records); offset += 1000 {
			operator("db_insert", map[string]any{"collection": collection, "records": records[offset:min(offset+1000, len(records))]})
		}
	}
	var records []any
	for id, name := range f.teams {
		records = append(records, map[string]any{"id": id, "name": name, "active": f.active[id]})
	}
	insert("teams", records)
	records = nil
	for id, team := range f.agents {
		records = append(records, map[string]any{"id": id, "team_id": team})
	}
	insert("agents", records)
	insert("members", []any{
		map[string]any{"user_id": "42", "team_id": "t1"},
		map[string]any{"user_id": "43", "team_id": "t1"}, map[string]any{"user_id": "43", "team_id": "t2"},
	})
	started := time.Now()
	records = make([]any, 0, len(f.calls))
	for _, c := range f.calls {
		records = append(records, map[string]any{"agent_id": c.agent, "team_id": c.team, "caller": c.caller, "answered": c.answered, "talk_seconds": strconv.Itoa(c.talk), "started_at": c.started.Format(time.RFC3339)})
	}
	insert("calls", records)
	records = nil
	for _, s := range f.sales {
		records = append(records, map[string]any{"agent_id": s.agent, "team_id": s.team, "amount": s.amount, "sold_at": s.sold.Format(time.RFC3339)})
	}
	insert("sales", records)
	t.Logf("loaded %d calls and %d sales into AptevaDB in %s", len(f.calls), len(f.sales), time.Since(started).Round(time.Millisecond))
	operator("db_realtime_configure", map[string]any{"enabled": true})
	operator("db_access_grant", map[string]any{"app": "graphql", "access": "read"})

	// Platform stand-in: Auth /me for user tokens, the app bridge for Database.
	bridge := newDatabaseBridge(t, database, project)
	target, _ := url.Parse(bridge.URL)
	forward := httputil.NewSingleHostReverseProxy(target)
	permissions := map[string][]string{"42": {"reports:read"}, "43": {"reports:read"}, "7": {"reports:read", "reports:all_teams"}, "9": {"other"}}
	var batchMu sync.Mutex
	var lastBatch []byte
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/apps/auth/me" {
			parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
			raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims struct {
				U string `json:"u"`
			}
			_ = json.Unmarshal(raw, &claims)
			perms, ok := permissions[claims.U]
			if !ok {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"org": "default", "user": map[string]any{"id": json.Number(claims.U)}, "authorization": map[string]any{"permissions": perms}})
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"tool":"db_batch"`) {
			batchMu.Lock()
			lastBatch = body
			batchMu.Unlock()
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		forward.ServeHTTP(w, r)
	}))
	t.Cleanup(platform.Close)

	graph := spawnGraph(t, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", platform.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "graphql-pipeline-test"))
	graph.MCP("graphql_security_set", map[string]any{"security": map[string]any{"mode": "auth", "tenant_id": "default", "environment": "production", "claims": []string{"permissions"}}})
	graph.MCP("graphql_source_add", map[string]any{"name": "ops", "kind": "database", "config": map[string]any{"database": "ops"}})
	graph.MCP("graphql_resolver_set", map[string]any{"parent_type": "Query", "field_name": "report", "source": "ops", "operation": "database_pipeline", "config": reportPipelineConfig()})
	graph.MCP("graphql_resolver_set", map[string]any{"parent_type": "Subscription", "field_name": "liveReport", "source": "ops", "operation": "database_pipeline", "config": reportPipelineConfig()})
	schema := graph.MCP("graphql_schema_create", map[string]any{"environment": "production", "sdl": pipelineReportSDL})
	graph.MCP("graphql_release_publish", map[string]any{"environment": "production", "schema_version": schema["schema"].(map[string]any)["version"]})

	from, to := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	query := func(user string, teams []string) (int, map[string]any) {
		t.Helper()
		variables := map[string]any{"from": from.Format(time.RFC3339), "to": to.Format(time.RFC3339)}
		if teams != nil {
			variables["teamIds"] = teams
		}
		body, _ := json.Marshal(map[string]any{"query": "query($from: String!, $to: String!, $teamIds: [String!]) { report(from: $from, to: $to, teamIds: $teamIds) " + pipelineReportSelection + " }", "variables": variables})
		req, _ := http.NewRequest("POST", graph.URL()+"/public/graphql/default", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+pipelineToken(user))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, out
	}
	check := func(label string, out map[string]any, teams []string) {
		t.Helper()
		if out["errors"] != nil {
			t.Fatalf("%s: %v", label, out["errors"])
		}
		report := out["data"].(map[string]any)["report"].(map[string]any)
		agents, total, days, byTeam := f.expect(teams, from, to)
		rows := report["agents"].([]any)
		if len(rows) != len(agents)+1 {
			t.Fatalf("%s: %d agent rows, want %d plus total", label, len(rows), len(agents))
		}
		for _, raw := range rows {
			row := raw.(map[string]any)
			want := total
			if id := row["agent_id"].(string); id != "__total__" {
				want = agents[id]
			}
			if want == nil {
				t.Fatalf("%s: unexpected row %v", label, row)
			}
			unanswered := want.callers - want.answeredCallers
			if row["calls"] != float64(want.calls) || row["callers"] != float64(want.callers) || row["answered_callers"] != float64(want.answeredCallers) ||
				row["unanswered_callers"] != float64(unanswered) || row["talk_seconds"] != float64(want.talk) || row["sales"] != float64(want.sales) {
				t.Fatalf("%s: row %v, want %+v", label, row, *want)
			}
			if revenue, _ := row["revenue"].(float64); math.Abs(revenue-want.revenue) > 1e-6 {
				t.Fatalf("%s: revenue %v, want %v", label, row["revenue"], want.revenue)
			}
			if rate, _ := row["answer_rate"].(float64); want.callers > 0 && math.Abs(rate-float64(want.answeredCallers)/float64(want.callers)) > 1e-9 {
				t.Fatalf("%s: answer_rate %v", label, row["answer_rate"])
			}
			if want.answeredCallers == 0 && row["conversion"] != nil {
				t.Fatalf("%s: conversion over zero answered callers should be null: %v", label, row)
			}
		}
		daily := report["daily"].([]any)
		if len(daily) != len(days) {
			t.Fatalf("%s: %d days, want %d", label, len(daily), len(days))
		}
		for _, raw := range daily {
			day := raw.(map[string]any)
			want := days[day["day"].(string)]
			if day["calls"] != float64(want[0]) || day["callers"] != float64(want[1]) {
				t.Fatalf("%s: day %v, want %v", label, day, want)
			}
		}
		for _, raw := range report["byTeam"].([]any) {
			team := raw.(map[string]any)
			if team["calls"] != float64(byTeam[team["team.name"].(string)]) {
				t.Fatalf("%s: chained lookup %v, want %d", label, team, byTeam[team["team.name"].(string)])
			}
		}
		if len(report["byTeam"].([]any)) != len(byTeam) || len(report["latest"].([]any)) != 5 {
			t.Fatalf("%s: byTeam=%v latest=%d", label, report["byTeam"], len(report["latest"].([]any)))
		}
	}

	// A restricted member sees exactly their own team.
	_, out := query("42", nil)
	check("member", out, []string{"t1"})
	if teams := out["data"].(map[string]any)["report"].(map[string]any)["teams"]; fmt.Sprint(teams) != "[t1]" {
		t.Fatalf("member scope: %v", teams)
	}
	// Unrestricted callers choose teams; without a choice they get active teams.
	_, out = query("7", []string{"t1", "t3"})
	check("admin chosen", out, []string{"t1", "t3"})
	_, out = query("7", nil)
	check("admin default", out, []string{"t1", "t2", "t3"})
	// Refusals fail the whole report.
	for label, c := range map[string]struct {
		user  string
		teams []string
	}{"outside membership": {"42", []string{"t2"}}, "ambiguous member": {"43", nil}, "no permission": {"9", nil}} {
		_, out := query(c.user, c.teams)
		if out["errors"] == nil || out["data"] != nil && out["data"].(map[string]any)["report"] != nil {
			t.Fatalf("%s returned data: %v", label, out)
		}
		if !strings.Contains(fmt.Sprint(out["errors"]), "permission_denied") {
			t.Fatalf("%s: %v", label, out["errors"])
		}
	}

	// Latency: the whole report through GraphQL vs replaying the exact
	// db_batch it sent straight to Database.
	_, _ = query("7", []string{"t1", "t2", "t3", "t4"})
	batchMu.Lock()
	var replay struct {
		Input map[string]any `json:"input"`
	}
	_ = json.Unmarshal(lastBatch, &replay)
	batchMu.Unlock()
	delete(replay.Input, "_project_id")
	measure := func(run func()) time.Duration {
		samples := make([]time.Duration, 0, 15)
		for i := 0; i < 15; i++ {
			started := time.Now()
			run()
			samples = append(samples, time.Since(started))
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		return samples[len(samples)/2]
	}
	direct := measure(func() { database.MCPAs("db_batch", replay.Input, 1, "", project) })
	viaGraph := measure(func() {
		if _, out := query("7", []string{"t1", "t2", "t3", "t4"}); out["errors"] != nil {
			t.Fatal(out["errors"])
		}
	})
	if os.Getenv("GRAPHQL_PIPELINE_PROFILE") != "" {
		// Where Database spends the batch: each operation alone, inside a
		// one-operation read batch and as its standalone tool.
		for _, raw := range replay.Input["operations"].([]any) {
			operation := raw.(map[string]any)
			args := operation["args"].(map[string]any)
			single := map[string]any{"database": "ops", "scope": replay.Input["scope"], "operations": []any{operation}}
			inBatch := measure(func() { database.MCPAs("db_batch", single, 1, "", project) })
			standalone := map[string]any{"database": "ops", "scope": replay.Input["scope"]}
			for key, value := range args {
				standalone[key] = value
			}
			alone := measure(func() { database.MCPAs("db_"+operation["op"].(string), standalone, 1, "", project) })
			t.Logf("profile %-9s %s %v: batch-of-one p50=%s standalone p50=%s", operation["op"], args["collection"], args["groupBy"], inBatch.Round(100*time.Microsecond), alone.Round(100*time.Microsecond))
		}
	}
	t.Logf("all teams, %d calls: direct db_batch (7 operations) p50=%s; GraphQL report incl. auth, access and combine p50=%s",
		len(f.calls), direct.Round(100*time.Microsecond), viaGraph.Round(100*time.Microsecond))

	// Subscription: one watch over every collection the report reads; a sale
	// (not a call) re-runs it once for both subscribers.
	header := http.Header{}
	header.Set("Sec-WebSocket-Protocol", "graphql-transport-ws")
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(graph.URL(), "http")+"/public/realtime/default", header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var ack wsMessage
	if conn.WriteJSON(map[string]any{"type": "connection_init", "payload": map[string]any{"Authorization": "Bearer " + pipelineToken("7")}}) != nil || conn.ReadJSON(&ack) != nil || ack.Type != "connection_ack" {
		t.Fatalf("realtime init: %+v", ack)
	}
	subscribe := func(id string) {
		if err := conn.WriteJSON(map[string]any{"id": id, "type": "subscribe", "payload": map[string]any{
			"query":     "subscription($from: String!, $to: String!, $teamIds: [String!]) { liveReport(from: $from, to: $to, teamIds: $teamIds) { agents { agent_id sales } } }",
			"variables": map[string]any{"from": from.Format(time.RFC3339), "to": to.Format(time.RFC3339), "teamIds": []string{"t1"}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	totalSales := func() float64 {
		_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		for {
			var message wsMessage
			if err := conn.ReadJSON(&message); err != nil {
				t.Fatalf("subscription: %v", err)
			}
			if message.Type == "error" {
				t.Fatalf("subscription failed: %s", message.Payload)
			}
			if message.Type != "next" {
				continue
			}
			var payload struct {
				Data struct {
					LiveReport struct {
						Agents []map[string]any `json:"agents"`
					} `json:"liveReport"`
				} `json:"data"`
				Errors []any `json:"errors"`
			}
			if json.Unmarshal(message.Payload, &payload) != nil || len(payload.Errors) > 0 {
				t.Fatalf("subscription payload: %s", message.Payload)
			}
			for _, row := range payload.Data.LiveReport.Agents {
				if row["agent_id"] == "__total__" {
					return row["sales"].(float64)
				}
			}
		}
	}
	subscribe("a")
	subscribe("b")
	_, totals, _, _ := f.expect([]string{"t1"}, from, to)
	if first, second := totalSales(), totalSales(); first != float64(totals.sales) || second != first {
		t.Fatalf("initial sales %v %v, want %d", first, second, totals.sales)
	}
	if stats, _ := operator("db_realtime_describe", map[string]any{})["stats"].(map[string]any); stats["Watchers"] != float64(1) {
		t.Fatalf("report subscriptions should share one watch: %v", stats)
	}
	bridge.takeCalls()
	committed := time.Now()
	operator("db_insert", map[string]any{"collection": "sales", "records": []any{map[string]any{"agent_id": "a00", "team_id": "t1", "amount": 99.5, "sold_at": "2026-10-15T12:00:00Z"}}})
	if first, second := totalSales(), totalSales(); first != float64(totals.sales+1) || second != first {
		t.Fatalf("after a sale: %v %v, want %d", first, second, totals.sales+1)
	}
	lag := time.Since(committed)
	time.Sleep(300 * time.Millisecond)
	calls, _ := bridge.takeCalls()
	if calls["db_batch"] != 1 {
		t.Fatalf("one commit should re-run the report once for both subscribers, got %v", calls)
	}
	t.Logf("sale commit -> both subscribers updated in %s (one db_batch)", lag.Round(100*time.Microsecond))
}
