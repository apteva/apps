package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Routing is intentionally data driven. Carrier callbacks pin a published
// version and only ever execute that immutable snapshot; the mutable draft is
// management state and can never change a call already in progress.
type routingDefinition struct {
	Entry string        `json:"entry"`
	Nodes []routingNode `json:"nodes"`
}

type routingNode struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Label    string            `json:"label,omitempty"`
	Next     string            `json:"next,omitempty"`
	Branches map[string]string `json:"branches,omitempty"`
	Config   map[string]any    `json:"config,omitempty"`
}

type routingFlowRow struct {
	ID, ProjectID, Name, Description, DraftJSON, PublishedVersionID string
	Generated                                                       bool
	CreatedAt, UpdatedAt                                            string
}

type routingFlowVersionRow struct {
	ID, FlowID, ProjectID, Definition, CreatedAt string
	Version                                      int
}

type routingDestinationRow struct {
	ID, ProjectID, Name, Kind, ConfigJSON, CreatedAt, UpdatedAt string
	Enabled                                                     bool
}

type ringGroupRow struct {
	ID, ProjectID, Name, Strategy, OverflowNodeID, CreatedAt, UpdatedAt string
	TimeoutSec                                                          int
	Enabled                                                             bool
	Members                                                             []ringGroupMemberRow
}

type ringGroupMemberRow struct {
	DestinationID string `json:"destination_id"`
	Position      int    `json:"position"`
	Priority      int    `json:"priority"`
	Weight        int    `json:"weight"`
	TimeoutSec    int    `json:"timeout_sec"`
	Enabled       bool   `json:"enabled"`
}

type routingSimulationContext struct {
	Caller            string            `json:"caller,omitempty"`
	Called            string            `json:"called,omitempty"`
	At                string            `json:"at,omitempty"`
	Digits            map[string]string `json:"digits,omitempty"`
	StopAtInteraction bool              `json:"-"`
}

type routingTraceStep struct {
	NodeID   string `json:"node_id"`
	NodeType string `json:"node_type"`
	Label    string `json:"label,omitempty"`
	Outcome  string `json:"outcome"`
	Next     string `json:"next,omitempty"`
}

type routingSimulation struct {
	Valid          bool               `json:"valid"`
	Errors         []string           `json:"errors,omitempty"`
	Trace          []routingTraceStep `json:"trace,omitempty"`
	TerminalNodeID string             `json:"terminal_node_id,omitempty"`
	TerminalType   string             `json:"terminal_type,omitempty"`
	DestinationID  string             `json:"destination_id,omitempty"`
	RingGroupID    string             `json:"ring_group_id,omitempty"`
}

type inboundRoutingPlan struct {
	FlowID, VersionID, DestinationID, RingGroupID, TerminalType string
	AnswerMode, Directive, Voice, Greeting, HoldPrompt          string
	AgentID                                                     int64
	TimeoutSec                                                  int
	Trace                                                       []routingTraceStep
	NodeID, Prompt, ValidDigits                                 string
}

var routingIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

var supportedRoutingNodes = map[string]bool{
	"announcement": true,
	"schedule":     true,
	"caller_match": true,
	"dtmf_menu":    true,
	"destination":  true,
	"ring_group":   true,
	"voicemail":    true,
	"reject":       true,
	"hangup":       true,
}

var supportedDestinationKinds = map[string]bool{
	"browser":   true,
	"agent":     true,
	"ai":        true,
	"pstn":      true,
	"sip":       true,
	"voicemail": true,
}

func parseRoutingDefinition(raw string) (routingDefinition, error) {
	var def routingDefinition
	if strings.TrimSpace(raw) == "" {
		return def, errors.New("flow definition is empty")
	}
	if err := json.Unmarshal([]byte(raw), &def); err != nil {
		return def, fmt.Errorf("invalid flow JSON: %w", err)
	}
	return def, nil
}

func validateRoutingDefinition(def routingDefinition) []string {
	errs := []string{}
	if len(def.Nodes) == 0 {
		return []string{"flow must contain at least one node"}
	}
	if len(def.Nodes) > 64 {
		errs = append(errs, "flow contains more than 64 nodes")
	}
	if !routingIDPattern.MatchString(def.Entry) {
		errs = append(errs, "entry must reference a valid node id")
	}
	nodes := make(map[string]routingNode, len(def.Nodes))
	for _, node := range def.Nodes {
		if !routingIDPattern.MatchString(node.ID) {
			errs = append(errs, fmt.Sprintf("node %q has an invalid id", node.ID))
			continue
		}
		if _, exists := nodes[node.ID]; exists {
			errs = append(errs, fmt.Sprintf("node id %q is duplicated", node.ID))
		}
		node.Type = strings.ToLower(strings.TrimSpace(node.Type))
		if !supportedRoutingNodes[node.Type] {
			errs = append(errs, fmt.Sprintf("node %q has unsupported type %q", node.ID, node.Type))
		}
		nodes[node.ID] = node
	}
	if _, ok := nodes[def.Entry]; !ok {
		errs = append(errs, "entry node does not exist")
	}
	for _, node := range def.Nodes {
		for _, edge := range routingNodeEdges(node) {
			if _, ok := nodes[edge]; !ok {
				errs = append(errs, fmt.Sprintf("node %q points to missing node %q", node.ID, edge))
			}
		}
		switch node.Type {
		case "schedule":
			if node.Branches["open"] == "" || node.Branches["closed"] == "" {
				errs = append(errs, fmt.Sprintf("schedule node %q needs open and closed branches", node.ID))
			}
		case "dtmf_menu":
			if len(node.Branches) == 0 {
				errs = append(errs, fmt.Sprintf("DTMF node %q needs at least one digit branch", node.ID))
			}
			if node.Branches["default"] == "" && node.Branches["timeout"] == "" {
				errs = append(errs, fmt.Sprintf("DTMF node %q needs a default or timeout branch", node.ID))
			}
		case "caller_match":
			if node.Branches["match"] == "" || node.Branches["default"] == "" {
				errs = append(errs, fmt.Sprintf("caller-match node %q needs match and default branches", node.ID))
			}
		case "destination":
			if routingConfigString(node.Config, "destination_id") == "" {
				errs = append(errs, fmt.Sprintf("destination node %q needs destination_id", node.ID))
			}
		case "ring_group":
			if routingConfigString(node.Config, "ring_group_id") == "" {
				errs = append(errs, fmt.Sprintf("ring-group node %q needs ring_group_id", node.ID))
			}
		}
	}
	// Routing cycles are intentionally rejected in the first runtime. Queue and
	// retry nodes will add explicit, bounded cycles later rather than accepting
	// an arbitrary graph that can trap a carrier call forever.
	state := map[string]int{}
	var visit func(string)
	visit = func(id string) {
		if state[id] == 1 {
			errs = append(errs, fmt.Sprintf("flow contains a cycle at node %q", id))
			return
		}
		if state[id] == 2 {
			return
		}
		state[id] = 1
		for _, edge := range routingNodeEdges(nodes[id]) {
			if _, ok := nodes[edge]; ok {
				visit(edge)
			}
		}
		state[id] = 2
	}
	if _, ok := nodes[def.Entry]; ok {
		visit(def.Entry)
	}
	for id := range nodes {
		if state[id] == 0 {
			errs = append(errs, fmt.Sprintf("node %q is unreachable from the entry", id))
		}
	}
	return uniqueStrings(errs)
}

func routingNodeEdges(node routingNode) []string {
	edges := []string{}
	if strings.TrimSpace(node.Next) != "" {
		edges = append(edges, strings.TrimSpace(node.Next))
	}
	keys := make([]string, 0, len(node.Branches))
	for key := range node.Branches {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if edge := strings.TrimSpace(node.Branches[key]); edge != "" {
			edges = append(edges, edge)
		}
	}
	return uniqueStrings(edges)
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func simulateRoutingDefinition(def routingDefinition, input routingSimulationContext) routingSimulation {
	if errs := validateRoutingDefinition(def); len(errs) > 0 {
		return routingSimulation{Valid: false, Errors: errs}
	}
	nodes := map[string]routingNode{}
	for _, node := range def.Nodes {
		nodes[node.ID] = node
	}
	at := time.Now().UTC()
	if input.At != "" {
		if parsed, err := time.Parse(time.RFC3339, input.At); err == nil {
			at = parsed
		}
	}
	result := routingSimulation{Valid: true}
	current := def.Entry
	for steps := 0; steps <= len(def.Nodes); steps++ {
		node := nodes[current]
		trace := routingTraceStep{NodeID: node.ID, NodeType: node.Type, Label: node.Label}
		next := node.Next
		switch node.Type {
		case "announcement":
			trace.Outcome = "play"
		case "schedule":
			open := scheduleOpen(node.Config, at)
			key := "closed"
			if open {
				key = "open"
			}
			trace.Outcome, next = key, node.Branches[key]
		case "caller_match":
			matched := callerMatches(node.Config, input.Caller)
			key := "default"
			if matched {
				key = "match"
			}
			trace.Outcome, next = key, node.Branches[key]
		case "dtmf_menu":
			digit := ""
			if input.Digits != nil {
				digit = strings.TrimSpace(input.Digits[node.ID])
			}
			if digit == "" && input.StopAtInteraction {
				trace.Outcome = "waiting_for_digits"
				result.TerminalNodeID, result.TerminalType = node.ID, node.Type
			} else if digit != "" && node.Branches[digit] != "" {
				trace.Outcome, next = "digit:"+digit, node.Branches[digit]
			} else if digit == "" && node.Branches["timeout"] != "" {
				trace.Outcome, next = "timeout", node.Branches["timeout"]
			} else {
				trace.Outcome, next = "default", node.Branches["default"]
			}
		case "destination":
			trace.Outcome = "selected"
			result.DestinationID = routingConfigString(node.Config, "destination_id")
			result.TerminalNodeID, result.TerminalType = node.ID, node.Type
		case "ring_group":
			trace.Outcome = "offered"
			result.RingGroupID = routingConfigString(node.Config, "ring_group_id")
			result.TerminalNodeID, result.TerminalType = node.ID, node.Type
		case "voicemail", "reject", "hangup":
			trace.Outcome = node.Type
			result.TerminalNodeID, result.TerminalType = node.ID, node.Type
		}
		trace.Next = next
		result.Trace = append(result.Trace, trace)
		if result.TerminalNodeID != "" {
			return result
		}
		if next == "" {
			result.Valid = false
			result.Errors = []string{fmt.Sprintf("node %q did not select a next node", node.ID)}
			return result
		}
		current = next
	}
	result.Valid = false
	result.Errors = []string{"flow exceeded its bounded execution depth"}
	return result
}

func routingConfigString(config map[string]any, key string) string {
	if config == nil {
		return ""
	}
	if value, ok := config[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func routingConfigInt(config map[string]any, key string, fallback int) int {
	if config == nil {
		return fallback
	}
	switch value := config[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case string:
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func routingConfigStrings(config map[string]any, key string) []string {
	if config == nil {
		return nil
	}
	out := []string{}
	switch values := config[key].(type) {
	case []any:
		for _, value := range values {
			if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case []string:
		out = append(out, values...)
	case string:
		for _, value := range strings.Split(values, ",") {
			if value = strings.TrimSpace(value); value != "" {
				out = append(out, value)
			}
		}
	}
	return out
}

func callerMatches(config map[string]any, caller string) bool {
	caller = strings.TrimSpace(caller)
	for _, exact := range routingConfigStrings(config, "numbers") {
		if caller == exact {
			return true
		}
	}
	for _, prefix := range routingConfigStrings(config, "prefixes") {
		if strings.HasPrefix(caller, prefix) {
			return true
		}
	}
	return false
}

func scheduleOpen(config map[string]any, at time.Time) bool {
	location := time.UTC
	if zone := routingConfigString(config, "timezone"); zone != "" {
		if loaded, err := time.LoadLocation(zone); err == nil {
			location = loaded
		}
	}
	local := at.In(location)
	days := routingConfigStrings(config, "days")
	if len(days) == 0 {
		days = []string{"mon", "tue", "wed", "thu", "fri"}
	}
	day := strings.ToLower(local.Weekday().String()[:3])
	dayOpen := false
	for _, candidate := range days {
		if strings.ToLower(candidate[:min(3, len(candidate))]) == day {
			dayOpen = true
			break
		}
	}
	if !dayOpen {
		return false
	}
	start := routingConfigString(config, "start")
	if start == "" {
		start = "09:00"
	}
	end := routingConfigString(config, "end")
	if end == "" {
		end = "18:00"
	}
	clock := local.Format("15:04")
	return clock >= start && clock < end
}

func (a *App) validateRoutingReferences(project string, def routingDefinition) []string {
	errs := validateRoutingDefinition(def)
	for _, node := range def.Nodes {
		switch node.Type {
		case "destination":
			id := routingConfigString(node.Config, "destination_id")
			if id != "" {
				var count int
				if err := a.db().db.QueryRow(`SELECT COUNT(*) FROM routing_destinations WHERE id = ? AND project_id = ? AND enabled = 1`, id, project).Scan(&count); err != nil || count != 1 {
					errs = append(errs, fmt.Sprintf("node %q references an unavailable destination", node.ID))
				}
			}
		case "ring_group":
			id := routingConfigString(node.Config, "ring_group_id")
			if id != "" {
				var count int
				if err := a.db().db.QueryRow(`SELECT COUNT(*) FROM ring_groups WHERE id = ? AND project_id = ? AND enabled = 1`, id, project).Scan(&count); err != nil || count != 1 {
					errs = append(errs, fmt.Sprintf("node %q references an unavailable ring group", node.ID))
				}
			}
		}
	}
	return uniqueStrings(errs)
}

func (a *App) validateFlowForRoute(project string, def routingDefinition, route *routeRow) []string {
	if route == nil {
		return []string{"route is unavailable"}
	}
	errs := []string{}
	for _, node := range def.Nodes {
		if node.Type == "dtmf_menu" && (route.InboundTransport == inboundTransportSIPDirect || (route.CarrierSlug != "twilio" && route.CarrierSlug != "telnyx")) {
			errs = append(errs, fmt.Sprintf("%s does not support Telephony-managed DTMF menus on this route", route.CarrierSlug))
		}
		if node.Type == "voicemail" && route.CarrierSlug != "twilio" {
			errs = append(errs, fmt.Sprintf("voicemail is not enabled for %s routes yet", route.CarrierSlug))
		}
		if node.Type != "destination" {
			continue
		}
		destination, _ := a.findRoutingDestination(project, routingConfigString(node.Config, "destination_id"))
		if destination != nil && (destination.Kind == "pstn" || destination.Kind == "sip") {
			errs = append(errs, fmt.Sprintf("%s destination %q is defined but live child-leg bridging is not enabled yet", strings.ToUpper(destination.Kind), destination.Name))
		}
	}
	return uniqueStrings(errs)
}

func (a *App) saveRoutingFlow(project, id, name, description, draft string) (*routingFlowRow, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return nil, errors.New("flow name is required and must be at most 120 characters")
	}
	def, err := parseRoutingDefinition(draft)
	if err != nil {
		return nil, err
	}
	canonical, _ := json.Marshal(def)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if id == "" {
		id = "flow_" + newCallID()
	}
	if !routingIDPattern.MatchString(id) {
		return nil, errors.New("invalid flow id")
	}
	_, err = a.db().db.Exec(`INSERT INTO routing_flows
		(id, project_id, name, description, draft_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, description=excluded.description,
			draft_json=excluded.draft_json, updated_at=excluded.updated_at
		WHERE routing_flows.project_id=excluded.project_id`, id, project, name, strings.TrimSpace(description), string(canonical), now, now)
	if err != nil {
		return nil, err
	}
	return a.findRoutingFlow(project, id)
}

func (a *App) publishRoutingFlow(project, id string) (*routingFlowVersionRow, []string, error) {
	flow, err := a.findRoutingFlow(project, id)
	if err != nil || flow == nil {
		if err == nil {
			err = errors.New("unknown flow")
		}
		return nil, nil, err
	}
	def, err := parseRoutingDefinition(flow.DraftJSON)
	if err != nil {
		return nil, nil, err
	}
	if validation := a.validateRoutingReferences(project, def); len(validation) > 0 {
		return nil, validation, nil
	}
	assigned, err := a.listRoutesForProject(project)
	if err != nil {
		return nil, nil, err
	}
	validation := []string{}
	for index := range assigned {
		if assigned[index].FlowID == id {
			validation = append(validation, a.validateFlowForRoute(project, def, &assigned[index])...)
		}
	}
	if len(validation) > 0 {
		return nil, uniqueStrings(validation), nil
	}
	tx, err := a.db().db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0)+1 FROM routing_flow_versions WHERE flow_id=?`, id).Scan(&version); err != nil {
		return nil, nil, err
	}
	versionID := fmt.Sprintf("%s_v%d", id, version)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`INSERT INTO routing_flow_versions(id,flow_id,project_id,version,definition,created_at) VALUES(?,?,?,?,?,?)`, versionID, id, project, version, flow.DraftJSON, now); err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(`UPDATE routing_flows SET published_version_id=?, updated_at=? WHERE id=? AND project_id=?`, versionID, now, id, project); err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(`UPDATE inbound_routes SET published_flow_version_id=?, updated_at=? WHERE flow_id=? AND project_id=?`, versionID, now, id, project); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return &routingFlowVersionRow{ID: versionID, FlowID: id, ProjectID: project, Version: version, Definition: flow.DraftJSON, CreatedAt: now}, nil, nil
}

func (a *App) findRoutingFlow(project, id string) (*routingFlowRow, error) {
	var row routingFlowRow
	var generated int
	err := a.db().db.QueryRow(`SELECT id,project_id,name,description,draft_json,published_version_id,generated,created_at,updated_at FROM routing_flows WHERE id=? AND project_id=?`, id, project).Scan(&row.ID, &row.ProjectID, &row.Name, &row.Description, &row.DraftJSON, &row.PublishedVersionID, &generated, &row.CreatedAt, &row.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.Generated = generated != 0
	return &row, nil
}

func (a *App) findRoutingVersion(project, id string) (*routingFlowVersionRow, error) {
	var row routingFlowVersionRow
	err := a.db().db.QueryRow(`SELECT id,flow_id,project_id,version,definition,created_at FROM routing_flow_versions WHERE id=? AND project_id=?`, id, project).Scan(&row.ID, &row.FlowID, &row.ProjectID, &row.Version, &row.Definition, &row.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &row, err
}

func (a *App) listRoutingFlows(project string) ([]routingFlowRow, error) {
	rows, err := a.db().db.Query(`SELECT id,project_id,name,description,draft_json,published_version_id,generated,created_at,updated_at FROM routing_flows WHERE project_id=? ORDER BY generated,name`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []routingFlowRow{}
	for rows.Next() {
		var row routingFlowRow
		var generated int
		if err := rows.Scan(&row.ID, &row.ProjectID, &row.Name, &row.Description, &row.DraftJSON, &row.PublishedVersionID, &generated, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, err
		}
		row.Generated = generated != 0
		out = append(out, row)
	}
	return out, rows.Err()
}

func routingFlowPublic(row routingFlowRow) map[string]any {
	var draft any
	_ = json.Unmarshal([]byte(row.DraftJSON), &draft)
	return map[string]any{"id": row.ID, "name": row.Name, "description": row.Description, "draft": draft, "published_version_id": row.PublishedVersionID, "generated": row.Generated, "created_at": row.CreatedAt, "updated_at": row.UpdatedAt}
}

func routingDestinationPublic(row routingDestinationRow) map[string]any {
	var config any
	_ = json.Unmarshal([]byte(row.ConfigJSON), &config)
	return map[string]any{"id": row.ID, "name": row.Name, "kind": row.Kind, "config": config, "enabled": row.Enabled, "created_at": row.CreatedAt, "updated_at": row.UpdatedAt}
}

func ringGroupPublic(row ringGroupRow) map[string]any {
	return map[string]any{"id": row.ID, "name": row.Name, "strategy": row.Strategy, "timeout_sec": row.TimeoutSec, "overflow_node_id": row.OverflowNodeID, "enabled": row.Enabled, "members": row.Members, "created_at": row.CreatedAt, "updated_at": row.UpdatedAt}
}

func (a *App) listRoutingDestinations(project string) ([]routingDestinationRow, error) {
	rows, err := a.db().db.Query(`SELECT id,project_id,name,kind,config_json,enabled,created_at,updated_at FROM routing_destinations WHERE project_id=? ORDER BY kind,name`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []routingDestinationRow{}
	for rows.Next() {
		var row routingDestinationRow
		var enabled int
		if err := rows.Scan(&row.ID, &row.ProjectID, &row.Name, &row.Kind, &row.ConfigJSON, &enabled, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, err
		}
		row.Enabled = enabled != 0
		out = append(out, row)
	}
	return out, rows.Err()
}

func (a *App) saveRoutingDestination(project, id, name, kind string, config any, enabled bool) (*routingDestinationRow, error) {
	name = strings.TrimSpace(name)
	kind = strings.ToLower(strings.TrimSpace(kind))
	if name == "" || len(name) > 120 {
		return nil, errors.New("destination name is required and must be at most 120 characters")
	}
	if !supportedDestinationKinds[kind] {
		return nil, errors.New("unsupported destination kind")
	}
	if id == "" {
		id = "dest_" + newCallID()
	}
	if !routingIDPattern.MatchString(id) {
		return nil, errors.New("invalid destination id")
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, errors.New("invalid destination config")
	}
	var decoded map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &decoded) != nil {
		decoded = map[string]any{}
		raw = []byte("{}")
	}
	switch kind {
	case "agent", "ai":
		if routingConfigInt(decoded, "agent_id", 0) <= 0 {
			return nil, errors.New("agent and AI destinations require agent_id")
		}
	case "pstn":
		if !validE164(routingConfigString(decoded, "phone_number")) {
			return nil, errors.New("PSTN destination requires an E.164 phone_number")
		}
	case "sip":
		if routingConfigString(decoded, "uri") == "" {
			return nil, errors.New("SIP destination requires uri")
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	flag := 0
	if enabled {
		flag = 1
	}
	_, err = a.db().db.Exec(`INSERT INTO routing_destinations(id,project_id,name,kind,config_json,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,kind=excluded.kind,config_json=excluded.config_json,enabled=excluded.enabled,updated_at=excluded.updated_at WHERE routing_destinations.project_id=excluded.project_id`, id, project, name, kind, string(raw), flag, now, now)
	if err != nil {
		return nil, err
	}
	return a.findRoutingDestination(project, id)
}

func (a *App) findRoutingDestination(project, id string) (*routingDestinationRow, error) {
	var row routingDestinationRow
	var enabled int
	err := a.db().db.QueryRow(`SELECT id,project_id,name,kind,config_json,enabled,created_at,updated_at FROM routing_destinations WHERE id=? AND project_id=?`, id, project).Scan(&row.ID, &row.ProjectID, &row.Name, &row.Kind, &row.ConfigJSON, &enabled, &row.CreatedAt, &row.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.Enabled = enabled != 0
	return &row, nil
}

func (a *App) listRingGroups(project string) ([]ringGroupRow, error) {
	rows, err := a.db().db.Query(`SELECT id,project_id,name,strategy,timeout_sec,overflow_node_id,enabled,created_at,updated_at FROM ring_groups WHERE project_id=? ORDER BY name`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ringGroupRow{}
	for rows.Next() {
		var row ringGroupRow
		var enabled int
		if err := rows.Scan(&row.ID, &row.ProjectID, &row.Name, &row.Strategy, &row.TimeoutSec, &row.OverflowNodeID, &enabled, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, err
		}
		row.Enabled = enabled != 0
		members, err := a.listRingGroupMembers(row.ID)
		if err != nil {
			return nil, err
		}
		row.Members = members
		out = append(out, row)
	}
	return out, rows.Err()
}

func (a *App) listRingGroupMembers(groupID string) ([]ringGroupMemberRow, error) {
	rows, err := a.db().db.Query(`SELECT destination_id,position,priority,weight,timeout_sec,enabled FROM ring_group_members WHERE ring_group_id=? ORDER BY priority,position`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ringGroupMemberRow{}
	for rows.Next() {
		var row ringGroupMemberRow
		var enabled int
		if err := rows.Scan(&row.DestinationID, &row.Position, &row.Priority, &row.Weight, &row.TimeoutSec, &enabled); err != nil {
			return nil, err
		}
		row.Enabled = enabled != 0
		out = append(out, row)
	}
	return out, rows.Err()
}

func (a *App) saveRingGroup(project, id, name, strategy string, timeout int, members []ringGroupMemberRow) (*ringGroupRow, error) {
	name = strings.TrimSpace(name)
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if name == "" {
		return nil, errors.New("ring group name is required")
	}
	switch strategy {
	case "simultaneous", "sequential", "round_robin", "priority":
	default:
		return nil, errors.New("unsupported ring strategy")
	}
	if timeout < 5 || timeout > 300 {
		return nil, errors.New("timeout_sec must be between 5 and 300")
	}
	if len(members) == 0 {
		return nil, errors.New("ring group needs at least one member")
	}
	if id == "" {
		id = "group_" + newCallID()
	}
	for _, member := range members {
		destination, err := a.findRoutingDestination(project, member.DestinationID)
		if err != nil || destination == nil {
			return nil, fmt.Errorf("unknown destination %q", member.DestinationID)
		}
	}
	tx, err := a.db().db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`INSERT INTO ring_groups(id,project_id,name,strategy,timeout_sec,enabled,created_at,updated_at) VALUES(?,?,?,?,?,1,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,strategy=excluded.strategy,timeout_sec=excluded.timeout_sec,enabled=1,updated_at=excluded.updated_at WHERE ring_groups.project_id=excluded.project_id`, id, project, name, strategy, timeout, now, now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM ring_group_members WHERE ring_group_id=?`, id); err != nil {
		return nil, err
	}
	for position, member := range members {
		member.Position = position
		if member.Weight <= 0 {
			member.Weight = 1
		}
		if member.TimeoutSec <= 0 {
			member.TimeoutSec = timeout
		}
		enabled := 1
		if !member.Enabled {
			enabled = 0
		}
		if _, err := tx.Exec(`INSERT INTO ring_group_members(ring_group_id,destination_id,position,priority,weight,timeout_sec,enabled) VALUES(?,?,?,?,?,?,?)`, id, member.DestinationID, member.Position, member.Priority, member.Weight, member.TimeoutSec, enabled); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	groups, err := a.listRingGroups(project)
	for i := range groups {
		if groups[i].ID == id {
			return &groups[i], err
		}
	}
	return nil, errors.New("saved ring group not found")
}

func (a *App) ensureLegacyRoutingFlows(ctx *sdk.AppCtx) error {
	rows, err := a.db().db.Query(`SELECT id,project_id,phone_number,agent_id,answer_mode,auto_directive,auto_voice,auto_greeting,hold_prompt,timeout_sec FROM inbound_routes WHERE flow_id=''`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type legacy struct {
		id, project, phone, mode, directive, voice, greeting, hold string
		agent                                                      int64
		timeout                                                    int
	}
	pending := []legacy{}
	for rows.Next() {
		var row legacy
		if err := rows.Scan(&row.id, &row.project, &row.phone, &row.agent, &row.mode, &row.directive, &row.voice, &row.greeting, &row.hold, &row.timeout); err != nil {
			return err
		}
		pending = append(pending, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, route := range pending {
		flowID := "legacy_flow_" + route.id
		destID := "legacy_dest_" + route.id
		kind := "agent"
		if route.mode == answerModeHumanBrowser {
			kind = "browser"
		} else if route.mode == answerModeRealtimeImmediate {
			kind = "ai"
		}
		config := map[string]any{"agent_id": route.agent, "directive": route.directive, "voice": route.voice, "greeting": route.greeting, "hold_prompt": route.hold, "timeout_sec": route.timeout}
		rawConfig, _ := json.Marshal(config)
		def := routingDefinition{Entry: "destination", Nodes: []routingNode{{ID: "destination", Type: "destination", Label: "Existing route", Config: map[string]any{"destination_id": destID}}}}
		rawDef, _ := json.Marshal(def)
		now := time.Now().UTC().Format(time.RFC3339Nano)
		versionID := flowID + "_v1"
		tx, err := a.db().db.Begin()
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT OR IGNORE INTO routing_destinations(id,project_id,name,kind,config_json,enabled,created_at,updated_at) VALUES(?,?,?,?,?,1,?,?)`, destID, route.project, "Existing "+route.phone, kind, string(rawConfig), now, now); err == nil {
			_, err = tx.Exec(`INSERT OR IGNORE INTO routing_flows(id,project_id,name,description,draft_json,published_version_id,generated,created_at,updated_at) VALUES(?,?,?,?,?,?,1,?,?)`, flowID, route.project, "Route "+route.phone, "Generated from the existing inbound route", string(rawDef), versionID, now, now)
		}
		if err == nil {
			_, err = tx.Exec(`INSERT OR IGNORE INTO routing_flow_versions(id,flow_id,project_id,version,definition,created_at) VALUES(?,?,?,?,?,?)`, versionID, flowID, route.project, 1, string(rawDef), now)
		}
		if err == nil {
			_, err = tx.Exec(`UPDATE inbound_routes SET flow_id=?,published_flow_version_id=? WHERE id=? AND project_id=? AND flow_id=''`, flowID, versionID, route.id, route.project)
		}
		if err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) resolveInboundRoutingPlan(route *routeRow, caller string, digits map[string]string) (*inboundRoutingPlan, error) {
	if route == nil || route.PublishedFlowVersionID == "" {
		return nil, nil
	}
	version, err := a.findRoutingVersion(route.ProjectID, route.PublishedFlowVersionID)
	if err != nil || version == nil {
		return nil, err
	}
	def, err := parseRoutingDefinition(version.Definition)
	if err != nil {
		return nil, err
	}
	simulation := simulateRoutingDefinition(def, routingSimulationContext{Caller: caller, Called: route.PhoneNumber, Digits: digits, StopAtInteraction: true})
	if !simulation.Valid {
		return nil, errors.New(strings.Join(simulation.Errors, "; "))
	}
	plan := &inboundRoutingPlan{FlowID: version.FlowID, VersionID: version.ID, TerminalType: simulation.TerminalType, DestinationID: simulation.DestinationID, Trace: simulation.Trace, AnswerMode: route.AnswerMode, Directive: route.AutoDirective, Voice: route.AutoVoice, Greeting: route.AutoGreeting, HoldPrompt: route.HoldPrompt, AgentID: route.AgentID, TimeoutSec: route.TimeoutSec}
	plan.RingGroupID = simulation.RingGroupID
	plan.NodeID = simulation.TerminalNodeID
	if simulation.TerminalType == "dtmf_menu" {
		for _, node := range def.Nodes {
			if node.ID != simulation.TerminalNodeID {
				continue
			}
			plan.Prompt = routingConfigString(node.Config, "prompt")
			keys := make([]string, 0, len(node.Branches))
			for key := range node.Branches {
				if len(key) == 1 && strings.Contains("0123456789#*", key) {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			plan.ValidDigits = strings.Join(keys, "")
			break
		}
	}
	if simulation.RingGroupID != "" {
		groups, err := a.listRingGroups(route.ProjectID)
		if err != nil {
			return nil, err
		}
		for _, group := range groups {
			if group.ID == simulation.RingGroupID {
				for _, member := range group.Members {
					if member.Enabled {
						plan.DestinationID = member.DestinationID
						break
					}
				}
				break
			}
		}
	}
	if plan.DestinationID != "" {
		destination, err := a.findRoutingDestination(route.ProjectID, plan.DestinationID)
		if err != nil || destination == nil {
			return nil, fmt.Errorf("routing destination unavailable")
		}
		var config map[string]any
		_ = json.Unmarshal([]byte(destination.ConfigJSON), &config)
		plan.Directive = routingConfigString(config, "directive")
		plan.Voice = routingConfigString(config, "voice")
		plan.Greeting = routingConfigString(config, "greeting")
		plan.HoldPrompt = routingConfigString(config, "hold_prompt")
		plan.TimeoutSec = routingConfigInt(config, "timeout_sec", plan.TimeoutSec)
		plan.AgentID = int64(routingConfigInt(config, "agent_id", int(plan.AgentID)))
		switch destination.Kind {
		case "browser":
			plan.AnswerMode = answerModeHumanBrowser
		case "ai":
			plan.AnswerMode = answerModeRealtimeImmediate
		case "agent":
			plan.AnswerMode = answerModeAgent
		case "voicemail":
			plan.TerminalType = "voicemail"
		case "pstn", "sip":
			return nil, fmt.Errorf("%s live call legs are not enabled in this release", destination.Kind)
		}
	}
	announcements := []string{}
	for _, step := range simulation.Trace {
		if step.NodeType != "announcement" {
			continue
		}
		for _, node := range def.Nodes {
			if node.ID == step.NodeID {
				if text := routingConfigString(node.Config, "text"); text != "" {
					announcements = append(announcements, text)
				}
				break
			}
		}
	}
	if prefix := strings.Join(announcements, " "); prefix != "" {
		switch plan.TerminalType {
		case "dtmf_menu", "voicemail":
			plan.Prompt = strings.TrimSpace(prefix + " " + plan.Prompt)
		default:
			if plan.AnswerMode == answerModeRealtimeImmediate {
				plan.Greeting = strings.TrimSpace(prefix + " " + plan.Greeting)
			} else {
				plan.HoldPrompt = strings.TrimSpace(prefix + " " + plan.HoldPrompt)
			}
		}
	}
	return plan, nil
}

func (a *App) persistRoutingExecution(callID, project string, plan *inboundRoutingPlan) error {
	if plan == nil {
		return nil
	}
	tx, err := a.db().db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	executionID := "exec_" + callID
	if _, err := tx.Exec(`UPDATE calls SET routing_flow_id=?,routing_flow_version_id=?,routing_destination_id=? WHERE id=? AND project_id=?`, plan.FlowID, plan.VersionID, plan.DestinationID, callID, project); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO call_route_executions(id,call_id,project_id,flow_id,flow_version_id,status,current_node_id,selected_destination_id,started_at) VALUES(?,?,?,?,?,'selected',?,?,?)`, executionID, callID, project, plan.FlowID, plan.VersionID, lastTraceNode(plan.Trace), plan.DestinationID, now); err != nil {
		return err
	}
	for index, step := range plan.Trace {
		raw, _ := json.Marshal(step)
		id := fmt.Sprintf("%s_%03d", executionID, index)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO call_node_executions(id,execution_id,node_id,node_type,outcome,detail_json,entered_at,exited_at) VALUES(?,?,?,?,?,?,?,?)`, id, executionID, step.NodeID, step.NodeType, step.Outcome, string(raw), now, now); err != nil {
			return err
		}
	}
	if plan.RingGroupID != "" {
		rows, err := tx.Query(`SELECT destination_id,timeout_sec FROM ring_group_members WHERE ring_group_id=? AND enabled=1 ORDER BY priority,position`, plan.RingGroupID)
		if err != nil {
			return err
		}
		type offered struct {
			id      string
			timeout int
		}
		offers := []offered{}
		for rows.Next() {
			var item offered
			if err := rows.Scan(&item.id, &item.timeout); err != nil {
				rows.Close()
				return err
			}
			offers = append(offers, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, offer := range offers {
			timeout := offer.timeout
			if timeout <= 0 {
				timeout = 20
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO call_offers(id,call_id,project_id,ring_group_id,destination_id,status,offered_at,expires_at) VALUES(?,?,?,?,?,'offered',?,?)`, "offer_"+newCallID(), callID, project, plan.RingGroupID, offer.id, now, time.Now().UTC().Add(time.Duration(timeout)*time.Second).Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.emitRoutingTrace(project, callID, plan, true)
	return nil
}

func lastTraceNode(trace []routingTraceStep) string {
	if len(trace) == 0 {
		return ""
	}
	return trace[len(trace)-1].NodeID
}

func (a *App) routingPlanForCall(row *callRow, digits map[string]string) (*routeRow, *inboundRoutingPlan, error) {
	if row == nil || row.RouteID == "" {
		return nil, nil, errors.New("call has no inbound route")
	}
	route, err := a.db().findRoute(row.RouteID)
	if err != nil || route == nil {
		if err == nil {
			err = errors.New("inbound route no longer exists")
		}
		return nil, nil, err
	}
	// Calls are pinned to the version selected at ingress. A publication while
	// the caller is inside an IVR must not move the remainder of that call.
	if row.RoutingFlowVersionID != "" {
		route.PublishedFlowVersionID = row.RoutingFlowVersionID
	}
	plan, err := a.resolveInboundRoutingPlan(route, row.FromNumber, digits)
	if err != nil {
		return nil, nil, err
	}
	applyRoutingPlanToRoute(route, plan)
	return route, plan, nil
}

func applyRoutingPlanToRoute(route *routeRow, plan *inboundRoutingPlan) {
	if route == nil || plan == nil {
		return
	}
	route.AnswerMode = plan.AnswerMode
	route.AutoDirective = plan.Directive
	route.AutoVoice = plan.Voice
	route.AutoGreeting = plan.Greeting
	route.HoldPrompt = firstNonEmpty(plan.HoldPrompt, route.HoldPrompt)
	route.AgentID = plan.AgentID
	if plan.TimeoutSec > 0 {
		route.TimeoutSec = plan.TimeoutSec
	}
	route.RoutingTerminalType = plan.TerminalType
	route.RoutingNodeID = plan.NodeID
	route.RoutingPrompt = plan.Prompt
	route.RoutingValidDigits = plan.ValidDigits
}

func (a *App) updateCallRoutingPlan(row *callRow, plan *inboundRoutingPlan) error {
	if row == nil || plan == nil {
		return nil
	}
	peer := inboundPeerKind(plan.AnswerMode)
	_, err := a.db().db.Exec(`UPDATE calls SET agent_id=?, peer_kind=?, directive=?, voice=?,
		routing_flow_id=?, routing_flow_version_id=?, routing_destination_id=?, updated_at=?
		WHERE id=? AND project_id=?`, plan.AgentID, peer, firstNonEmpty(plan.Directive, "inbound pending"), plan.Voice,
		plan.FlowID, plan.VersionID, plan.DestinationID, time.Now().UTC().Format(time.RFC3339Nano), row.ID, row.ProjectID)
	if err != nil {
		return err
	}
	return a.persistRoutingProgress(row.ID, row.ProjectID, plan)
}

func (a *App) persistRoutingProgress(callID, project string, plan *inboundRoutingPlan) error {
	if plan == nil {
		return nil
	}
	tx, err := a.db().db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	executionID := "exec_" + callID
	if _, err := tx.Exec(`UPDATE call_route_executions SET status='selected',current_node_id=?,selected_destination_id=?,error_message='' WHERE id=? AND project_id=?`, plan.NodeID, plan.DestinationID, executionID, project); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, step := range plan.Trace {
		raw, _ := json.Marshal(step)
		if _, err := tx.Exec(`INSERT INTO call_node_executions(id,execution_id,node_id,node_type,outcome,detail_json,entered_at,exited_at) VALUES(?,?,?,?,?,?,?,?)`, "node_"+newCallID(), executionID, step.NodeID, step.NodeType, step.Outcome, string(raw), now, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.emitRoutingTrace(project, callID, plan, false)
	return nil
}

func (a *App) emitRoutingTrace(project, callID string, plan *inboundRoutingPlan, started bool) {
	if globalCtx == nil || plan == nil {
		return
	}
	ctx := globalCtx.WithProject(project)
	base := map[string]any{"call_id": callID, "flow_id": plan.FlowID, "flow_version_id": plan.VersionID, "destination_id": plan.DestinationID, "ring_group_id": plan.RingGroupID, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano)}
	if started {
		ctx.Emit("call.routing.started", base)
	}
	for _, step := range plan.Trace {
		payload := map[string]any{}
		for key, value := range base {
			payload[key] = value
		}
		payload["node_id"], payload["node_type"], payload["outcome"] = step.NodeID, step.NodeType, step.Outcome
		ctx.Emit("call.routing.node_entered", payload)
	}
	if started && plan.RingGroupID != "" {
		ctx.Emit("call.offered", base)
	}
}

func (a *App) twilioIVRActionURL(row *callRow, nodeID string) string {
	query := url.Values{"token": {row.CallbackSecret}, "project_id": {row.ProjectID}, "node": {nodeID}}.Encode()
	return a.publicAppURL() + "/ivr/twilio/" + url.PathEscape(row.ID) + "?" + query
}

func (a *App) writeTwilioRoutingPlan(w http.ResponseWriter, row *callRow, route *routeRow, plan *inboundRoutingPlan) error {
	if plan == nil {
		return errors.New("routing plan is unavailable")
	}
	switch plan.TerminalType {
	case "dtmf_menu":
		prompt := firstNonEmpty(plan.Prompt, "Please choose an option using your telephone keypad.")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprintf(w, `<Response><Gather input="dtmf" numDigits="1" timeout="6" actionOnEmptyResult="true" action="%s" method="POST"><Say>%s</Say></Gather></Response>`, xmlEscape(a.twilioIVRActionURL(row, plan.NodeID)), xmlEscape(prompt))
		return nil
	case "destination", "ring_group":
		switch plan.AnswerMode {
		case answerModeRealtimeImmediate:
			ctx := globalCtx.WithProject(row.ProjectID)
			if _, err := a.prepareInboundRealtime(ctx, row, plan.Directive, plan.Voice, plan.Greeting); err != nil {
				return err
			}
			if err := a.db().updateStatus(row.ID, "answered", ""); err != nil {
				return err
			}
			stored, err := a.db().findCall(row.ID)
			if err != nil || stored == nil {
				return firstError(err, errors.New("answered call disappeared"))
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(a.twilioStreamTwiML(stored)))
			return nil
		default:
			writeTwilioHold(w, a.holdText(*route), a.twilioWaitURL(*route, row.ID))
			return nil
		}
	case "voicemail":
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprintf(w, `<Response><Say>%s</Say><Record maxLength="180" playBeep="true" recordingStatusCallback="%s" recordingStatusCallbackMethod="POST"/><Hangup/></Response>`, xmlEscape(firstNonEmpty(plan.Prompt, "Please leave a message after the tone.")), xmlEscape(a.twilioRecordingStatusURL(row.ID, row.CallbackSecret, row.ProjectID)))
		return nil
	case "reject", "hangup":
		writeTwilioHangup(w)
		return nil
	default:
		return fmt.Errorf("unsupported routing terminal %q", plan.TerminalType)
	}
}

func firstError(err error, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

func (a *App) handleIVRCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/ivr/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] != "twilio" {
		http.NotFound(w, r)
		return
	}
	row, err := a.db().findCall(parts[1])
	if err != nil || row == nil {
		http.Error(w, "unknown call", http.StatusNotFound)
		return
	}
	if row.CarrierSlug != "twilio" || !secureEqual(r.URL.Query().Get("token"), row.CallbackSecret) || r.URL.Query().Get("project_id") != row.ProjectID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := a.verifyTwilioRequest(r, row.CarrierConnectionID); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if isTerminalStatus(row.Status) {
		writeTwilioHangup(w)
		return
	}
	nodeID := strings.TrimSpace(r.URL.Query().Get("node"))
	digit := strings.TrimSpace(r.FormValue("Digits"))
	if digit == "" {
		digit = "__timeout__"
	}
	route, plan, err := a.routingPlanForCall(row, map[string]string{nodeID: digit})
	if err != nil {
		_ = a.db().updateStatus(row.ID, "failed", "resume IVR: "+err.Error())
		writeTwilioSayHangup(w, "We could not route your call. Please try again later.")
		return
	}
	if err := a.updateCallRoutingPlan(row, plan); err != nil {
		http.Error(w, "persist routing selection", http.StatusInternalServerError)
		return
	}
	row, _ = a.db().findCall(row.ID)
	if err := a.writeTwilioRoutingPlan(w, row, route, plan); err != nil {
		_ = a.db().updateStatus(row.ID, "failed", "execute IVR: "+err.Error())
		writeTwilioSayHangup(w, "We could not route your call. Please try again later.")
	}
}

func (a *App) answerTelnyxIVR(ctx *sdk.AppCtx, row *callRow) error {
	if row == nil {
		return errors.New("call is unavailable")
	}
	_, err := executeCarrierTool(ctx, row.CarrierConnectionID, "answer_call", map[string]any{
		"call_control_id": row.CarrierSID,
		"command_id":      telnyxCommandID(row.ID, "ivr-answer"),
	})
	return err
}

func (a *App) startTelnyxGather(ctx *sdk.AppCtx, row *callRow, plan *inboundRoutingPlan) error {
	if row == nil || plan == nil || plan.TerminalType != "dtmf_menu" {
		return errors.New("DTMF plan is unavailable")
	}
	validDigits := firstNonEmpty(plan.ValidDigits, "0123456789")
	_, err := executeCarrierTool(ctx, row.CarrierConnectionID, "gather_using_speak", map[string]any{
		"call_control_id":            row.CarrierSID,
		"payload":                    firstNonEmpty(plan.Prompt, "Please choose an option using your telephone keypad."),
		"payload_type":               "text",
		"voice":                      "Telnyx.NaturalHD.Astra",
		"language":                   "en-US",
		"minimum_digits":             1,
		"maximum_digits":             1,
		"timeout_millis":             6000,
		"inter_digit_timeout_millis": 3000,
		"valid_digits":               validDigits,
		"gather_id":                  row.ID + ":" + plan.NodeID,
		"command_id":                 telnyxCommandID(row.ID, "gather-"+plan.NodeID),
	})
	return err
}

func (a *App) startTelnyxStream(ctx *sdk.AppCtx, row *callRow) error {
	_, err := executeCarrierTool(ctx, row.CarrierConnectionID, "start_streaming", map[string]any{
		"call_control_id":            row.CarrierSID,
		"stream_url":                 a.publicWSStreamURL("telnyx", row.ID, row.CallbackSecret),
		"stream_track":               "inbound_track",
		"stream_codec":               "PCMU",
		"stream_bidirectional_mode":  "rtp",
		"stream_bidirectional_codec": "PCMU",
		"command_id":                 telnyxCommandID(row.ID, "ivr-stream"),
	})
	return err
}

func (a *App) executeTelnyxRoutingPlan(ctx *sdk.AppCtx, row *callRow, route *routeRow, plan *inboundRoutingPlan) error {
	if err := a.updateCallRoutingPlan(row, plan); err != nil {
		return err
	}
	row, _ = a.db().findCall(row.ID)
	switch plan.TerminalType {
	case "dtmf_menu":
		return a.startTelnyxGather(ctx, row, plan)
	case "destination", "ring_group":
		switch plan.AnswerMode {
		case answerModeRealtimeImmediate:
			if _, err := a.prepareInboundRealtime(ctx, row, plan.Directive, plan.Voice, plan.Greeting); err != nil {
				return err
			}
			row, _ = a.db().findCall(row.ID)
			if err := a.db().updateStatus(row.ID, "answered", ""); err != nil {
				return err
			}
			return a.startTelnyxStream(ctx, row)
		case answerModeHumanBrowser:
			// The carrier leg is already answered by the IVR. Return it to the
			// project's browser-offer state; softphoneAnswer starts streaming
			// after the operator atomically claims it.
			_, err := a.db().db.Exec(`UPDATE calls SET status='pending',peer_kind=?,state_expires_at=?,updated_at=? WHERE id=?`, peerKindHuman, time.Now().UTC().Add(time.Duration(route.TimeoutSec)*time.Second).Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339Nano), row.ID)
			return err
		default:
			_, err := a.db().db.Exec(`UPDATE calls SET status='pending',state_expires_at=?,updated_at=? WHERE id=?`, time.Now().UTC().Add(time.Duration(route.TimeoutSec)*time.Second).Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339Nano), row.ID)
			return err
		}
	case "reject", "hangup":
		_, err := executeCarrierTool(ctx, row.CarrierConnectionID, "hangup_call", map[string]any{"call_control_id": row.CarrierSID, "command_id": telnyxCommandID(row.ID, "ivr-hangup")})
		return err
	case "voicemail":
		return errors.New("Telnyx voicemail destination is not enabled yet")
	default:
		return fmt.Errorf("unsupported Telnyx routing terminal %q", plan.TerminalType)
	}
}

// /routing is intentionally a compact JSON management surface. The UI and MCP
// tools share the same store and validator so publication cannot bypass rules.
func (a *App) handleRouting(w http.ResponseWriter, r *http.Request) {
	project, err := a.panelProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/routing/"), "/")
	if r.Method == http.MethodGet && path == "snapshot" {
		a.routingSnapshot(w, project)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID          string                   `json:"id"`
		Name        string                   `json:"name"`
		Description string                   `json:"description"`
		Draft       json.RawMessage          `json:"draft"`
		Kind        string                   `json:"kind"`
		Config      any                      `json:"config"`
		Enabled     *bool                    `json:"enabled"`
		Strategy    string                   `json:"strategy"`
		TimeoutSec  int                      `json:"timeout_sec"`
		Members     []ringGroupMemberRow     `json:"members"`
		FlowID      string                   `json:"flow_id"`
		RouteID     string                   `json:"route_id"`
		Context     routingSimulationContext `json:"context"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch path {
	case "flows/save":
		row, err := a.saveRoutingFlow(project, body.ID, body.Name, body.Description, string(body.Draft))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, routingFlowPublic(*row))
	case "flows/publish":
		version, validation, err := a.publishRoutingFlow(project, body.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(validation) > 0 {
			w.WriteHeader(http.StatusUnprocessableEntity)
			writeJSON(w, map[string]any{"valid": false, "errors": validation})
			return
		}
		writeJSON(w, map[string]any{"valid": true, "version": version})
	case "flows/simulate":
		var def routingDefinition
		if len(body.Draft) > 0 {
			if err := json.Unmarshal(body.Draft, &def); err != nil {
				http.Error(w, "invalid draft", http.StatusBadRequest)
				return
			}
		} else {
			flow, err := a.findRoutingFlow(project, body.ID)
			if err != nil || flow == nil {
				http.Error(w, "unknown flow", http.StatusNotFound)
				return
			}
			def, _ = parseRoutingDefinition(flow.DraftJSON)
		}
		result := simulateRoutingDefinition(def, body.Context)
		result.Errors = uniqueStrings(append(result.Errors, a.validateRoutingReferences(project, def)...))
		result.Valid = len(result.Errors) == 0
		writeJSON(w, result)
	case "destinations/save":
		enabled := true
		if body.Enabled != nil {
			enabled = *body.Enabled
		}
		row, err := a.saveRoutingDestination(project, body.ID, body.Name, body.Kind, body.Config, enabled)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, routingDestinationPublic(*row))
	case "ring-groups/save":
		row, err := a.saveRingGroup(project, body.ID, body.Name, body.Strategy, body.TimeoutSec, body.Members)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, ringGroupPublic(*row))
	case "routes/assign":
		flow, err := a.findRoutingFlow(project, body.FlowID)
		if err != nil || flow == nil || flow.PublishedVersionID == "" {
			http.Error(w, "flow must exist and be published", http.StatusBadRequest)
			return
		}
		route, routeErr := a.db().findRoute(body.RouteID)
		if routeErr != nil || route == nil || route.ProjectID != project {
			http.Error(w, "unknown route", http.StatusNotFound)
			return
		}
		version, versionErr := a.findRoutingVersion(project, flow.PublishedVersionID)
		if versionErr != nil || version == nil {
			http.Error(w, "published flow version is unavailable", http.StatusBadRequest)
			return
		}
		definition, parseErr := parseRoutingDefinition(version.Definition)
		if parseErr != nil {
			http.Error(w, parseErr.Error(), http.StatusBadRequest)
			return
		}
		if capabilityErrors := a.validateFlowForRoute(project, definition, route); len(capabilityErrors) > 0 {
			w.WriteHeader(http.StatusUnprocessableEntity)
			writeJSON(w, map[string]any{"valid": false, "errors": capabilityErrors})
			return
		}
		result, err := a.db().db.Exec(`UPDATE inbound_routes SET flow_id=?,published_flow_version_id=?,updated_at=? WHERE id=? AND project_id=?`, flow.ID, flow.PublishedVersionID, time.Now().UTC().Format(time.RFC3339Nano), body.RouteID, project)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		count, _ := result.RowsAffected()
		if count != 1 {
			http.Error(w, "unknown route", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "route_id": body.RouteID, "flow_id": flow.ID, "version_id": flow.PublishedVersionID})
	default:
		http.NotFound(w, r)
	}
}

func (a *App) routingSnapshot(w http.ResponseWriter, project string) {
	flows, err := a.listRoutingFlows(project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	destinations, err := a.listRoutingDestinations(project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	groups, err := a.listRingGroups(project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	routes, err := a.listRoutesForProject(project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	flowOut := make([]map[string]any, 0, len(flows))
	for _, row := range flows {
		flowOut = append(flowOut, routingFlowPublic(row))
	}
	destinationOut := make([]map[string]any, 0, len(destinations))
	for _, row := range destinations {
		destinationOut = append(destinationOut, routingDestinationPublic(row))
	}
	groupOut := make([]map[string]any, 0, len(groups))
	for _, row := range groups {
		groupOut = append(groupOut, ringGroupPublic(row))
	}
	routeOut := make([]map[string]any, 0, len(routes))
	for _, row := range routes {
		routeOut = append(routeOut, routePublic(a, row))
	}
	writeJSON(w, map[string]any{"flows": flowOut, "destinations": destinationOut, "ring_groups": groupOut, "routes": routeOut, "node_types": []string{"announcement", "schedule", "caller_match", "dtmf_menu", "destination", "ring_group", "voicemail", "reject", "hangup"}, "destination_kinds": []string{"browser", "agent", "ai", "pstn", "sip", "voicemail"}})
}

func (a *App) listRoutesForProject(project string) ([]routeRow, error) {
	rows, err := a.db().db.Query(`SELECT id FROM inbound_routes WHERE project_id=? ORDER BY created_at DESC`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []routeRow{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		route, err := a.db().findRoute(id)
		if err != nil {
			return nil, err
		}
		if route != nil {
			out = append(out, *route)
		}
	}
	return out, rows.Err()
}

func (a *App) toolFlowsList(_ context.Context, ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	flows, err := a.listRoutingFlows(currentProject(ctx))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	out := []map[string]any{}
	for _, flow := range flows {
		out = append(out, routingFlowPublic(flow))
	}
	return map[string]any{"flows": out}, nil
}
func (a *App) toolFlowsCreate(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	raw, err := json.Marshal(args["definition"])
	if err != nil {
		return mcpError("invalid definition"), nil
	}
	flow, err := a.saveRoutingFlow(currentProject(ctx), "", strArg(args, "name", ""), strArg(args, "description", ""), string(raw))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return routingFlowPublic(*flow), nil
}

func (a *App) toolFlowsUpdate(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := strArg(args, "flow_id", "")
	flow, err := a.findRoutingFlow(currentProject(ctx), id)
	if err != nil || flow == nil {
		return mcpError("unknown flow"), nil
	}
	raw, err := json.Marshal(args["definition"])
	if err != nil {
		return mcpError("invalid definition"), nil
	}
	name := strArg(args, "name", flow.Name)
	description := strArg(args, "description", flow.Description)
	updated, err := a.saveRoutingFlow(currentProject(ctx), id, name, description, string(raw))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return routingFlowPublic(*updated), nil
}

func (a *App) toolFlowsGet(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	flow, err := a.findRoutingFlow(currentProject(ctx), strArg(args, "flow_id", ""))
	if err != nil || flow == nil {
		return mcpError("unknown flow"), nil
	}
	return routingFlowPublic(*flow), nil
}

func (a *App) toolFlowsValidate(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	flow, err := a.findRoutingFlow(currentProject(ctx), strArg(args, "flow_id", ""))
	if err != nil || flow == nil {
		return mcpError("unknown flow"), nil
	}
	def, err := parseRoutingDefinition(flow.DraftJSON)
	if err != nil {
		return map[string]any{"valid": false, "errors": []string{err.Error()}}, nil
	}
	errs := a.validateRoutingReferences(currentProject(ctx), def)
	return map[string]any{"valid": len(errs) == 0, "errors": errs}, nil
}

func (a *App) toolDestinationsList(_ context.Context, ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	rows, err := a.listRoutingDestinations(currentProject(ctx))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	out := []map[string]any{}
	for _, row := range rows {
		out = append(out, routingDestinationPublic(row))
	}
	return map[string]any{"destinations": out}, nil
}

func (a *App) toolDestinationsCreate(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	row, err := a.saveRoutingDestination(currentProject(ctx), "", strArg(args, "name", ""), strArg(args, "kind", ""), args["config"], true)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return routingDestinationPublic(*row), nil
}

func (a *App) toolRingGroupsList(_ context.Context, ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	rows, err := a.listRingGroups(currentProject(ctx))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	out := []map[string]any{}
	for _, row := range rows {
		out = append(out, ringGroupPublic(row))
	}
	return map[string]any{"ring_groups": out}, nil
}

func (a *App) toolRingGroupsCreate(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	raw, err := json.Marshal(args["members"])
	if err != nil {
		return mcpError("invalid members"), nil
	}
	var members []ringGroupMemberRow
	if err := json.Unmarshal(raw, &members); err != nil {
		return mcpError("invalid members"), nil
	}
	row, err := a.saveRingGroup(currentProject(ctx), "", strArg(args, "name", ""), strArg(args, "strategy", "simultaneous"), intArg(args, "timeout_sec", 20), members)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return ringGroupPublic(*row), nil
}

func (a *App) toolRoutesSetFlow(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project := currentProject(ctx)
	route, err := a.db().findRoute(strArg(args, "route_id", ""))
	if err != nil || route == nil || route.ProjectID != project {
		return mcpError("unknown route"), nil
	}
	flow, err := a.findRoutingFlow(project, strArg(args, "flow_id", ""))
	if err != nil || flow == nil || flow.PublishedVersionID == "" {
		return mcpError("flow must exist and be published"), nil
	}
	version, _ := a.findRoutingVersion(project, flow.PublishedVersionID)
	if version == nil {
		return mcpError("published version unavailable"), nil
	}
	def, err := parseRoutingDefinition(version.Definition)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	if errs := a.validateFlowForRoute(project, def, route); len(errs) > 0 {
		return map[string]any{"valid": false, "errors": errs}, nil
	}
	_, err = a.db().db.Exec(`UPDATE inbound_routes SET flow_id=?,published_flow_version_id=?,updated_at=? WHERE id=? AND project_id=?`, flow.ID, flow.PublishedVersionID, time.Now().UTC().Format(time.RFC3339Nano), route.ID, project)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return map[string]any{"ok": true, "route_id": route.ID, "flow_id": flow.ID, "version_id": flow.PublishedVersionID}, nil
}
func (a *App) toolFlowsPublish(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	version, validation, err := a.publishRoutingFlow(currentProject(ctx), strArg(args, "flow_id", ""))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	if len(validation) > 0 {
		return map[string]any{"valid": false, "errors": validation}, nil
	}
	return map[string]any{"valid": true, "version": version}, nil
}
func (a *App) toolFlowsSimulate(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	flow, err := a.findRoutingFlow(currentProject(ctx), strArg(args, "flow_id", ""))
	if err != nil || flow == nil {
		return mcpError("unknown flow"), nil
	}
	def, err := parseRoutingDefinition(flow.DraftJSON)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	input := routingSimulationContext{Caller: strArg(args, "caller", ""), Called: strArg(args, "called", "")}
	return simulateRoutingDefinition(def, input), nil
}
