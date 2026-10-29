package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gorilla/websocket"
)

// Opt-in load test for Database subscriptions: a paced writer commits single
// records while N subscribers each hold their own realtime connection. It
// reports commit-to-subscriber lag, how far bursts were coalesced, bridge
// calls, and CPU used by both sidecars. Set GRAPHQL_BENCH_REALTIME=1.
func TestRealDatabaseSubscriptionLoad(t *testing.T) {
	databaseDir := os.Getenv("GRAPHQL_TEST_DATABASE_DIR")
	if testing.Short() || databaseDir == "" || os.Getenv("GRAPHQL_BENCH_REALTIME") == "" {
		t.Skip("set GRAPHQL_TEST_DATABASE_DIR and GRAPHQL_BENCH_REALTIME=1")
	}
	duration := 5 * time.Second
	if value, err := time.ParseDuration(os.Getenv("GRAPHQL_BENCH_REALTIME_DURATION")); err == nil {
		duration = value
	}
	cooldown := 300 * time.Millisecond
	if value, err := time.ParseDuration(os.Getenv("GRAPHQL_BENCH_REALTIME_COOLDOWN")); err == nil {
		cooldown = value
	}
	bigRows := 100000
	if value, err := strconv.Atoi(os.Getenv("GRAPHQL_BENCH_DB_ROWS")); err == nil && value > 0 {
		bigRows = value
	}
	const project = "graphql-database-realtime-load"
	database := tk.SpawnSidecar(t, databaseDir, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	operator := func(tool string, args map[string]any) map[string]any {
		return database.MCPAs(tool, args, 1, "", project)
	}
	// Writes during the load phase reuse pooled connections as an operator.
	write := func(databaseName string) error {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "db_insert", "arguments": map[string]any{
			"database": databaseName, "collection": "orders", "records": []any{map[string]any{"region": "r0", "amount": 1.0}},
		}}})
		req, _ := http.NewRequest(http.MethodPost, database.URL()+"/mcp", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+database.Token())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Apteva-Project-ID", project)
		req.Header.Set("X-Apteva-Caller-Agent", "1")
		resp, err := bridgeClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || bytes.Contains(body, []byte(`"isError":true`)) || bytes.Contains(body, []byte(`"error":{`)) {
			return fmt.Errorf("insert: %d %s", resp.StatusCode, body)
		}
		return nil
	}
	fields := []any{map[string]any{"name": "region", "type": "text"}, map[string]any{"name": "amount", "type": "number"}}
	for _, name := range []string{"small", "big"} {
		operator("db_database_create", map[string]any{"database": name, "adapter": "aptevadb"})
		operator("db_collection_create", map[string]any{"database": name, "collection": "orders", "fields": fields})
		operator("db_realtime_configure", map[string]any{"database": name, "enabled": true})
		operator("db_access_grant", map[string]any{"database": name, "app": "graphql", "access": "read"})
	}
	for offset := 0; offset < bigRows; offset += 1000 {
		records := make([]any, 0, 1000)
		for index := offset; index < min(offset+1000, bigRows); index++ {
			records = append(records, map[string]any{"region": fmt.Sprintf("r%d", index%8), "amount": float64(index % 100)})
		}
		operator("db_insert", map[string]any{"database": "big", "collection": "orders", "records": records})
	}

	bridge := newDatabaseBridge(t, database, project)
	graph := spawnGraph(t, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", bridge.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "database-realtime-load"))
	graph.MCP("graphql_source_add", map[string]any{"name": "small", "kind": "database", "config": map[string]any{"database": "small", "collection": "orders"}})
	graph.MCP("graphql_source_add", map[string]any{"name": "big", "kind": "database", "config": map[string]any{"database": "big", "collection": "orders"}})
	graph.MCP("graphql_resolver_set", map[string]any{"parent_type": "Subscription", "field_name": "smallCount", "source": "small", "operation": "count"})
	graph.MCP("graphql_resolver_set", map[string]any{"parent_type": "Subscription", "field_name": "bigStats", "source": "big", "operation": "aggregate", "config": map[string]any{
		"groupBy": []any{"region"}, "metrics": []any{map[string]any{"name": "count", "op": "count"}, map[string]any{"name": "sum", "op": "sum", "field": "amount"}},
	}})
	schema := graph.MCP("graphql_schema_create", map[string]any{"environment": "development", "sdl": `type Query { ping: Int } type Stat { region: String! count: Int! sum: Float! } type Subscription { smallCount: Int! bigStats: [Stat!]! }`})
	graph.MCP("graphql_release_publish", map[string]any{"environment": "development", "schema_version": schema["schema"].(map[string]any)["version"]})

	databasePID, graphPID := listenerPID(t, database.URL()), listenerPID(t, graph.URL())
	type scenario struct {
		database, field string
		subscribers     int
		rate            int  // target commits per second
		distinct        bool // each subscriber runs its own query (its own group)
	}
	label := func(s scenario) string {
		if s.distinct {
			return s.database + "-distinct"
		}
		return s.database
	}
	scenarios := []scenario{}
	for _, subscribers := range []int{1, 10, 50} {
		for _, rate := range []int{20, 100, 500} {
			scenarios = append(scenarios, scenario{"small", "smallCount", subscribers, rate, false})
		}
	}
	for _, rate := range []int{20, 100} {
		scenarios = append(scenarios, scenario{"small", "smallCount", 50, rate, true})
	}
	for _, subscribers := range []int{1, 10} {
		for _, rate := range []int{20, 100} {
			scenarios = append(scenarios, scenario{"big", "bigStats", subscribers, rate, false})
		}
	}
	// GRAPHQL_BENCH_REALTIME_ONLY filters scenarios, e.g. "small" or "small/50".
	if value := os.Getenv("GRAPHQL_BENCH_REALTIME_ONLY"); value != "" {
		kept := []scenario{}
		for _, s := range scenarios {
			name := label(s)
			if strings.Contains(value, name) && (!strings.Contains(value, "/") || strings.Contains(value, fmt.Sprintf("%s/%d", name, s.subscribers))) {
				kept = append(kept, s)
			}
		}
		scenarios = kept
	}

	var report strings.Builder
	fmt.Fprintf(&report, "\n%s per scenario, big=%d records, single-record commits\n", duration, bigRows)
	fmt.Fprintf(&report, "%-14s %4s %5s | %7s | %-26s | %7s %8s | %6s %8s %6s | %s\n", "db", "subs", "rate", "commits", "lag p50 / p95 / max", "updates", "per-sub", "polls", "queries", "conns", "cpu db / graphql")
	total := map[string]int{"small": 0, "big": bigRows}
	for _, s := range scenarios {
		read := func(raw json.RawMessage) int {
			if s.field == "smallCount" {
				// Distinct subscribers alias the field, so read its only value.
				var payload struct {
					Data map[string]int `json:"data"`
				}
				_ = json.Unmarshal(raw, &payload)
				for _, count := range payload.Data {
					return count
				}
				return 0
			}
			var payload struct {
				Data struct {
					Stats []struct {
						Count int `json:"count"`
					} `json:"bigStats"`
				} `json:"data"`
			}
			_ = json.Unmarshal(raw, &payload)
			sum := 0
			for _, stat := range payload.Data.Stats {
				sum += stat.Count
			}
			return sum
		}
		type observation struct {
			count int
			at    time.Time
		}
		observed := make([][]observation, s.subscribers)
		conns := make([]*websocket.Conn, s.subscribers)
		var wg sync.WaitGroup
		var errMu sync.Mutex
		errors := []string{}
		for index := range conns {
			conn := dialRealtime(t, graph, project)
			conns[index] = conn
			query := "subscription { smallCount }"
			if s.distinct {
				query = fmt.Sprintf("subscription { c%d: smallCount }", index)
			}
			if s.field == "bigStats" {
				query = "subscription { bigStats { region count sum } }"
			}
			if err := conn.WriteJSON(map[string]any{"id": "s", "type": "subscribe", "payload": map[string]any{"query": query}}); err != nil {
				t.Fatal(err)
			}
			wg.Add(1)
			go func(index int, conn *websocket.Conn) {
				defer wg.Done()
				for {
					var message wsMessage
					if err := conn.ReadJSON(&message); err != nil {
						return
					}
					switch message.Type {
					case "next":
						var envelope struct {
							Errors []any `json:"errors"`
						}
						if json.Unmarshal(message.Payload, &envelope) == nil && len(envelope.Errors) > 0 {
							errMu.Lock()
							errors = append(errors, string(message.Payload))
							errMu.Unlock()
							continue
						}
						observed[index] = append(observed[index], observation{read(message.Payload), time.Now()})
					case "error":
						errMu.Lock()
						errors = append(errors, string(message.Payload))
						errMu.Unlock()
					}
				}
			}(index, conn)
		}
		// Every subscriber must hold its initial result before writes start.
		deadline := time.Now().Add(15 * time.Second)
		for ready := false; !ready; {
			ready = true
			for index := range observed {
				if len(observed[index]) == 0 {
					ready = false
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("%v: subscribers did not start", s)
			}
			time.Sleep(5 * time.Millisecond)
		}
		bridge.takeCalls()
		cpuDatabase, cpuGraph := cpuSeconds(t, databasePID), cpuSeconds(t, graphPID)

		// Durable single-record commits take a few milliseconds each, so higher
		// rates need concurrent writers. The k-th completed commit guarantees a
		// count of at least base+k, which is what lag is measured against.
		base := total[s.database]
		commits := []time.Time{}
		var commitMu sync.Mutex
		writers := min(max(s.rate/40, 1), 32)
		interval := time.Duration(writers) * time.Second / time.Duration(s.rate)
		started := time.Now()
		var writersDone sync.WaitGroup
		for writer := range writers {
			writersDone.Add(1)
			go func(offset time.Duration) {
				defer writersDone.Done()
				for next := started.Add(offset); time.Since(started) < duration; next = next.Add(interval) {
					if wait := time.Until(next); wait > 0 {
						time.Sleep(wait)
					}
					if err := write(s.database); err != nil {
						errMu.Lock()
						errors = append(errors, err.Error())
						errMu.Unlock()
						continue
					}
					commitMu.Lock()
					commits = append(commits, time.Now())
					commitMu.Unlock()
				}
			}(time.Duration(writer) * interval / time.Duration(writers))
		}
		writersDone.Wait()
		writeElapsed := time.Since(started)
		sort.Slice(commits, func(i, j int) bool { return commits[i].Before(commits[j]) })
		final := base + len(commits)
		total[s.database] = final
		// Wait for every subscriber to converge on the final state.
		converge := time.Now().Add(30 * time.Second)
		for {
			done := true
			for index := range observed {
				list := observed[index]
				if len(list) == 0 || list[len(list)-1].count < final {
					done = false
				}
			}
			if done || time.Now().After(converge) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		for _, conn := range conns {
			_ = conn.WriteJSON(map[string]any{"id": "s", "type": "complete"})
			_ = conn.Close()
		}
		wg.Wait()
		calls, newConns := bridge.takeCalls()
		usedDatabase, usedGraph := cpuSeconds(t, databasePID)-cpuDatabase, cpuSeconds(t, graphPID)-cpuGraph

		lags := []time.Duration{}
		updates, stale := 0, 0
		for index := range observed {
			list := observed[index][1:] // skip the initial result
			updates += len(list)
			position := 0
			for k, committed := range commits {
				want := base + k + 1
				for position < len(list) && list[position].count < want {
					position++
				}
				if position == len(list) {
					stale++
					continue
				}
				lags = append(lags, max(list[position].at.Sub(committed), 0))
			}
		}
		sort.Slice(lags, func(i, j int) bool { return lags[i] < lags[j] })
		pick := func(q float64) string {
			if len(lags) == 0 {
				return "-"
			}
			return lags[min(int(float64(len(lags))*q), len(lags)-1)].Round(100 * time.Microsecond).String()
		}
		rate := float64(len(commits)) / writeElapsed.Seconds()
		fmt.Fprintf(&report, "%-14s %4d %5d | %7s | %8s / %8s / %8s | %7d %8.1f | %6d %8d %6d | %.2fs / %.2fs\n",
			label(s), s.subscribers, s.rate, fmt.Sprintf("%d@%.0f/s", len(commits), rate), pick(0.5), pick(0.95), pick(1),
			updates, float64(updates)/float64(s.subscribers), calls["db_changes"], calls["db_count"]+calls["db_aggregate"], newConns, usedDatabase, usedGraph)
		if stale > 0 || len(errors) > 0 {
			sample := errors[:min(len(errors), 2)]
			for i, text := range sample {
				sample[i] = text[:min(len(text), 160)]
			}
			fmt.Fprintf(&report, "        ! %d commit observations missing after 30s, %d errors %v\n", stale, len(errors), sample)
		}
		// Let the shared watcher stop, and optionally let closed connections
		// drain, before the next scenario.
		time.Sleep(cooldown)
	}
	t.Log(report.String())
	if path := os.Getenv("GRAPHQL_BENCH_REPORT"); path != "" {
		_ = os.WriteFile(path, []byte(report.String()), 0o644)
	}
}

func listenerPID(t *testing.T, url string) int {
	t.Helper()
	port := url[strings.LastIndex(url, ":")+1:]
	out, err := exec.Command("lsof", "-t", "-iTCP:"+port, "-sTCP:LISTEN").Output()
	if err != nil {
		t.Fatalf("lsof %s: %v", port, err)
	}
	pid, err := strconv.Atoi(strings.Fields(string(out))[0])
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// cpuSeconds reads a process's accumulated user+system CPU time from ps.
func cpuSeconds(t *testing.T, pid int) float64 {
	t.Helper()
	out, err := exec.Command("ps", "-o", "time=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps %d: %v", pid, err)
	}
	seconds := 0.0
	for _, part := range strings.Split(strings.TrimSpace(string(out)), ":") {
		value, _ := strconv.ParseFloat(part, 64)
		seconds = seconds*60 + value
	}
	return seconds
}
