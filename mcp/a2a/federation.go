package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	defaultPeerTimeoutSeconds = 8
	defaultCardCacheSeconds   = 300
)

type peerConfig struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	BaseURL        string   `json:"base_url"`
	Token          string   `json:"token"`
	DiscoverAgents []string `json:"discover_agents,omitempty"`
	InvokeAgents   []string `json:"invoke_agents,omitempty"`
}

type localNode struct {
	NodeID      string `json:"node_id"`
	DisplayName string `json:"display_name"`
}

type AgentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Examples    []string `json:"examples,omitempty"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
}

type AgentInterface struct {
	ProtocolBinding string `json:"protocolBinding"`
	ProtocolVersion string `json:"protocolVersion"`
	URL             string `json:"url"`
}

type AgentCapabilities struct {
	Streaming              bool `json:"streaming"`
	PushNotifications      bool `json:"pushNotifications"`
	StateTransitionHistory bool `json:"stateTransitionHistory"`
	ExtendedAgentCard      bool `json:"extendedAgentCard"`
}

type AgentProvider struct {
	Organization string `json:"organization"`
	URL          string `json:"url"`
}

type AgentCard struct {
	Name                string                       `json:"name"`
	Description         string                       `json:"description"`
	URL                 string                       `json:"url,omitempty"`
	Version             string                       `json:"version"`
	Provider            AgentProvider                `json:"provider"`
	SupportedInterfaces []AgentInterface             `json:"supportedInterfaces"`
	Capabilities        AgentCapabilities            `json:"capabilities"`
	DefaultInputModes   []string                     `json:"defaultInputModes"`
	DefaultOutputModes  []string                     `json:"defaultOutputModes"`
	Skills              []AgentSkill                 `json:"skills"`
	SecuritySchemes     map[string]map[string]string `json:"securitySchemes,omitempty"`
	Security            []map[string][]string        `json:"security,omitempty"`
}

type agentProfile struct {
	ProjectID    string
	LocalAgentID int64
	CardID       string
	Description  string
	Skills       []AgentSkill
	Enabled      bool
}

type directoryEntry struct {
	CardID      string   `json:"card_id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Online      bool     `json:"online"`
	Skills      []string `json:"skills,omitempty"`
}

type remoteAgent struct {
	Ref         string
	PeerID      string
	CardID      string
	Name        string
	Description string
	EndpointURL string
	Skills      []string
	Card        *AgentCard
	FetchedAt   string
	ExpiresAt   string
}

func randomID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return prefix + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return prefix + hex.EncodeToString(b)
}

func ensureLocalNode(app *sdk.AppCtx) (*localNode, error) {
	name := strings.TrimSpace(app.Config().Get("node_name"))
	if name == "" {
		name = "Apteva"
	}
	now := nowUTC()
	_, err := app.AppDB().Exec(`INSERT OR IGNORE INTO a2a_node
		(singleton_id, node_id, display_name, created_at, updated_at)
		VALUES (1, ?, ?, ?, ?)`, randomID("node_"), name, now, now)
	if err != nil {
		return nil, err
	}
	_, _ = app.AppDB().Exec(`UPDATE a2a_node SET display_name = ?, updated_at = ? WHERE singleton_id = 1`, name, now)
	var node localNode
	if err := app.AppDB().QueryRow(`SELECT node_id, display_name FROM a2a_node WHERE singleton_id = 1`).Scan(&node.NodeID, &node.DisplayName); err != nil {
		return nil, err
	}
	return &node, nil
}

func peerConfigs(app *sdk.AppCtx) ([]peerConfig, error) {
	raw := strings.TrimSpace(app.Config().Get("peers_json"))
	if raw == "" {
		return nil, nil
	}
	var peers []peerConfig
	if err := json.Unmarshal([]byte(raw), &peers); err != nil {
		var wrapper struct {
			Peers []peerConfig `json:"peers"`
		}
		if err2 := json.Unmarshal([]byte(raw), &wrapper); err2 != nil {
			return nil, fmt.Errorf("invalid peers_json: %w", err)
		}
		peers = wrapper.Peers
	}
	seenID := map[string]bool{}
	seenToken := map[string]bool{}
	for i := range peers {
		peers[i].ID = strings.TrimSpace(peers[i].ID)
		peers[i].Name = strings.TrimSpace(peers[i].Name)
		peers[i].BaseURL = strings.TrimRight(strings.TrimSpace(peers[i].BaseURL), "/")
		peers[i].Token = strings.TrimSpace(peers[i].Token)
		if peers[i].ID == "" || peers[i].BaseURL == "" || peers[i].Token == "" {
			return nil, fmt.Errorf("peers_json entry %d requires id, base_url, and token", i)
		}
		if seenID[peers[i].ID] {
			return nil, fmt.Errorf("duplicate peer id %q", peers[i].ID)
		}
		if seenToken[peers[i].Token] {
			return nil, fmt.Errorf("peer tokens must be unique")
		}
		seenID[peers[i].ID] = true
		seenToken[peers[i].Token] = true
		if peers[i].Name == "" {
			peers[i].Name = peers[i].ID
		}
		if err := validatePeerBaseURL(peers[i].BaseURL); err != nil {
			return nil, fmt.Errorf("peer %q: %w", peers[i].ID, err)
		}
	}
	return peers, nil
}

func validatePeerBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("base_url must be an absolute URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return errors.New("base_url must use HTTPS (HTTP is allowed only for loopback development)")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("base_url cannot contain user info, query, or fragment")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func findPeer(app *sdk.AppCtx, id string) (*peerConfig, error) {
	peers, err := peerConfigs(app)
	if err != nil {
		return nil, err
	}
	for i := range peers {
		if peers[i].ID == id || strings.EqualFold(peers[i].Name, id) {
			return &peers[i], nil
		}
	}
	return nil, fmt.Errorf("peer %q is not configured", id)
}

func authenticatePeer(app *sdk.AppCtx, r *http.Request) (*peerConfig, error) {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(raw) < 8 || !strings.EqualFold(raw[:7], "Bearer ") {
		return nil, errors.New("bearer token required")
	}
	token := strings.TrimSpace(raw[7:])
	peers, err := peerConfigs(app)
	if err != nil {
		return nil, err
	}
	for i := range peers {
		if len(token) == len(peers[i].Token) && subtle.ConstantTimeCompare([]byte(token), []byte(peers[i].Token)) == 1 {
			return &peers[i], nil
		}
	}
	return nil, errors.New("invalid peer token")
}

func peerAllows(peer *peerConfig, action string, profile *agentProfile, agent *sdk.PlatformAgent) bool {
	var rules []string
	switch action {
	case "discover":
		rules = peer.DiscoverAgents
	case "invoke":
		rules = peer.InvokeAgents
	default:
		return false
	}
	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if rule == "*" || rule == profile.CardID || rule == strconv.FormatInt(agent.ID, 10) || strings.EqualFold(rule, agent.Name) {
			return true
		}
	}
	return false
}

func ensureAgentProfile(db *sql.DB, agent sdk.PlatformAgent) (*agentProfile, error) {
	now := nowUTC()
	_, err := db.Exec(`INSERT OR IGNORE INTO a2a_agent_profiles
		(project_id, local_agent_id, card_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`, agent.ProjectID, agent.ID, randomID("card_"), now, now)
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(`SELECT project_id, local_agent_id, card_id, description, skills_json, enabled
		FROM a2a_agent_profiles WHERE project_id = ? AND local_agent_id = ?`, agent.ProjectID, agent.ID)
	var p agentProfile
	var skillsRaw string
	var enabled int
	if err := row.Scan(&p.ProjectID, &p.LocalAgentID, &p.CardID, &p.Description, &skillsRaw, &enabled); err != nil {
		return nil, err
	}
	p.Enabled = enabled != 0
	_ = json.Unmarshal([]byte(skillsRaw), &p.Skills)
	return &p, nil
}

func getAgentProfileByCard(db *sql.DB, cardID string) (*agentProfile, error) {
	row := db.QueryRow(`SELECT project_id, local_agent_id, card_id, description, skills_json, enabled
		FROM a2a_agent_profiles WHERE card_id = ?`, cardID)
	var p agentProfile
	var skillsRaw string
	var enabled int
	err := row.Scan(&p.ProjectID, &p.LocalAgentID, &p.CardID, &p.Description, &skillsRaw, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Enabled = enabled != 0
	_ = json.Unmarshal([]byte(skillsRaw), &p.Skills)
	return &p, nil
}

func listCardAgents(app *sdk.AppCtx, projectID string) ([]sdk.PlatformAgent, bool, error) {
	agents, err := sdk.ListAgentsVia(app.PlatformAPI(), projectID)
	if err != nil {
		return nil, false, err
	}
	annotated := false
	for _, agent := range agents {
		if agent.AttachedToCaller {
			annotated = true
			break
		}
	}
	if !annotated {
		return agents, false, nil
	}
	out := make([]sdk.PlatformAgent, 0, len(agents))
	for _, agent := range agents {
		if agent.AttachedToCaller {
			out = append(out, agent)
		}
	}
	return out, true, nil
}

func publicA2ABaseURL(app *sdk.AppCtx) string {
	base := strings.TrimRight(strings.TrimSpace(app.Config().Get("public_url")), "/")
	if base == "" {
		if info, err := app.PlatformInfo(); err == nil && info != nil {
			base = strings.TrimRight(strings.TrimSpace(info.PublicURL), "/")
		}
	}
	if strings.HasSuffix(base, "/api/apps/a2a") {
		return base
	}
	return base + "/api/apps/a2a"
}

func buildAgentCard(app *sdk.AppCtx, agent sdk.PlatformAgent, profile *agentProfile) *AgentCard {
	description := strings.TrimSpace(profile.Description)
	if description == "" {
		description = "Apteva agent " + agent.Name
	}
	endpoint := publicA2ABaseURL(app) + "/agents/" + url.PathEscape(profile.CardID)
	return &AgentCard{
		Name:        agent.Name,
		Description: description,
		URL:         endpoint,
		Version:     "1.0.0",
		Provider:    AgentProvider{Organization: "Apteva", URL: publicA2ABaseURL(app)},
		SupportedInterfaces: []AgentInterface{{
			ProtocolBinding: "JSONRPC",
			ProtocolVersion: "1.0",
			URL:             endpoint,
		}},
		Capabilities:       AgentCapabilities{},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             profile.Skills,
		SecuritySchemes: map[string]map[string]string{
			"peerBearer": {"type": "http", "scheme": "bearer"},
		},
		Security: []map[string][]string{{"peerBearer": {}}},
	}
}

func skillIDs(skills []AgentSkill) []string {
	out := make([]string, 0, len(skills))
	for _, skill := range skills {
		if skill.ID != "" {
			out = append(out, skill.ID)
		}
	}
	return out
}

func matchesAgentQuery(name, description string, skills []string, query, capability string) bool {
	if capability != "" {
		found := false
		for _, skill := range skills {
			if strings.EqualFold(skill, capability) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if query == "" {
		return true
	}
	haystack := strings.ToLower(name + " " + description + " " + strings.Join(skills, " "))
	return strings.Contains(haystack, strings.ToLower(query))
}

func remoteRef(peerID, cardID string) string {
	sum := sha256.Sum256([]byte(peerID + "\x00" + cardID))
	return "remote_" + hex.EncodeToString(sum[:12])
}

func upsertRemoteAgent(db *sql.DB, peer peerConfig, entry directoryEntry, card *AgentCard, ttl time.Duration) (*remoteAgent, error) {
	ref := remoteRef(peer.ID, entry.CardID)
	endpoint := ""
	cardJSON := ""
	if card != nil {
		if len(card.SupportedInterfaces) > 0 {
			endpoint = card.SupportedInterfaces[0].URL
		}
		raw, _ := json.Marshal(card)
		cardJSON = string(raw)
	}
	skills := entry.Skills
	if card != nil {
		skills = skillIDs(card.Skills)
	}
	skillsJSON, _ := json.Marshal(skills)
	now := time.Now().UTC()
	_, err := db.Exec(`INSERT INTO a2a_remote_agents
		(ref, peer_id, card_id, name, description, endpoint_url, skills_json, card_json, fetched_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(peer_id, card_id) DO UPDATE SET
			ref=excluded.ref, name=excluded.name, description=excluded.description,
			endpoint_url=CASE WHEN excluded.endpoint_url <> '' THEN excluded.endpoint_url ELSE a2a_remote_agents.endpoint_url END,
			skills_json=excluded.skills_json,
			card_json=CASE WHEN excluded.card_json <> '' THEN excluded.card_json ELSE a2a_remote_agents.card_json END,
			fetched_at=excluded.fetched_at, expires_at=excluded.expires_at`,
		ref, peer.ID, entry.CardID, entry.Name, entry.Description, endpoint, string(skillsJSON), cardJSON,
		now.Format(time.RFC3339), now.Add(ttl).Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	return getRemoteAgent(db, ref)
}

func getRemoteAgent(db *sql.DB, ref string) (*remoteAgent, error) {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "a2a:")
	row := db.QueryRow(`SELECT ref, peer_id, card_id, name, description, endpoint_url,
		skills_json, card_json, fetched_at, expires_at FROM a2a_remote_agents WHERE ref = ?`, ref)
	var out remoteAgent
	var skillsRaw, cardRaw string
	err := row.Scan(&out.Ref, &out.PeerID, &out.CardID, &out.Name, &out.Description, &out.EndpointURL,
		&skillsRaw, &cardRaw, &out.FetchedAt, &out.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(skillsRaw), &out.Skills)
	if cardRaw != "" {
		var card AgentCard
		if json.Unmarshal([]byte(cardRaw), &card) == nil {
			out.Card = &card
		}
	}
	return &out, nil
}

func getRemoteAgentByPeerCard(db *sql.DB, peerID, cardID string) (*remoteAgent, error) {
	var ref string
	err := db.QueryRow(`SELECT ref FROM a2a_remote_agents WHERE peer_id = ? AND card_id = ?`, peerID, cardID).Scan(&ref)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return getRemoteAgent(db, ref)
}

func (a *App) httpClient(app *sdk.AppCtx) *http.Client {
	if a.client != nil {
		return a.client
	}
	return &http.Client{
		Timeout: time.Duration(configInt(app, "peer_timeout_seconds", defaultPeerTimeoutSeconds)) * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (a *App) fetchPeerDirectory(ctx context.Context, app *sdk.AppCtx, peer peerConfig, query, capability string) ([]directoryEntry, error) {
	u := peer.BaseURL + "/directory/agents"
	values := url.Values{}
	if query != "" {
		values.Set("q", query)
	}
	if capability != "" {
		values.Set("capability", capability)
	}
	if encoded := values.Encode(); encoded != "" {
		u += "?" + encoded
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+peer.Token)
	res, err := a.httpClient(app).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("directory returned HTTP %d", res.StatusCode)
	}
	var payload struct {
		Agents []directoryEntry `json:"agents"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Agents, nil
}

func (a *App) fetchRemoteCard(ctx context.Context, app *sdk.AppCtx, peer peerConfig, cardID string) (*AgentCard, error) {
	u := peer.BaseURL + "/agent-cards/" + url.PathEscape(cardID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+peer.Token)
	res, err := a.httpClient(app).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Agent Card returned HTTP %d", res.StatusCode)
	}
	var card AgentCard
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&card); err != nil {
		return nil, err
	}
	if card.Name == "" || len(card.SupportedInterfaces) == 0 {
		return nil, errors.New("Agent Card is missing name or supportedInterfaces")
	}
	peerURL, _ := url.Parse(peer.BaseURL)
	for _, intf := range card.SupportedInterfaces {
		endpoint, err := url.Parse(intf.URL)
		if err != nil || endpoint.Scheme != peerURL.Scheme || !strings.EqualFold(endpoint.Host, peerURL.Host) {
			return nil, errors.New("Agent Card interface must remain on the configured peer origin")
		}
	}
	return &card, nil
}

func sortDiscoverEntries(entries []discoverEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Name == entries[j].Name {
			return entries[i].Peer < entries[j].Peer
		}
		return entries[i].Name < entries[j].Name
	})
}

func (a *App) toolGetAgent(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	from, err := identify(ctx, app)
	if err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(stringArg(args, "agent"))
	if ref == "" {
		return nil, errors.New("agent required — use an address returned by agents_discover")
	}
	if strings.HasPrefix(ref, "agent:") || isPositiveInteger(ref) {
		idRaw := strings.TrimPrefix(ref, "agent:")
		id, _ := strconv.ParseInt(idRaw, 10, 64)
		agent, getErr := app.PlatformAPI().GetAgent(id)
		if getErr != nil || agent == nil {
			return nil, fmt.Errorf("local agent %d not found", id)
		}
		if from.ProjectID != "" && agent.ProjectID != "" && from.ProjectID != agent.ProjectID {
			return nil, errors.New("agent is outside the caller's project")
		}
		profile, profileErr := ensureAgentProfile(app.AppDB(), *agent)
		if profileErr != nil {
			return nil, profileErr
		}
		if !profile.Enabled {
			return nil, errors.New("agent is not exposed through A2A")
		}
		card := buildAgentCard(app, *agent, profile)
		return map[string]any{
			"address": "agent:" + strconv.FormatInt(agent.ID, 10),
			"peer":    "local",
			"online":  strings.EqualFold(agent.Status, "running"),
			"card":    card,
		}, nil
	}
	if !strings.HasPrefix(ref, "a2a:") && !strings.HasPrefix(ref, "remote_") {
		return nil, errors.New("unknown agent reference — use an address returned by agents_discover")
	}
	remote, err := getRemoteAgent(app.AppDB(), ref)
	if err != nil {
		return nil, err
	}
	if remote == nil {
		return nil, errors.New("remote agent reference expired — run agents_discover again")
	}
	peer, err := findPeer(app, remote.PeerID)
	if err != nil {
		return nil, err
	}
	refresh, _ := args["refresh"].(bool)
	expires, _ := time.Parse(time.RFC3339, remote.ExpiresAt)
	if refresh || remote.Card == nil || time.Now().After(expires) {
		card, fetchErr := a.fetchRemoteCard(ctx, app, *peer, remote.CardID)
		if fetchErr != nil {
			return nil, fmt.Errorf("could not retrieve Agent Card from %s: %w", peer.Name, fetchErr)
		}
		entry := directoryEntry{
			CardID: remote.CardID, Name: card.Name, Description: card.Description,
			Online: true, Skills: skillIDs(card.Skills),
		}
		remote, err = upsertRemoteAgent(app.AppDB(), *peer, entry, card,
			time.Duration(configInt(app, "card_cache_seconds", defaultCardCacheSeconds))*time.Second)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"address": "a2a:" + remote.Ref,
		"peer":    peer.Name,
		"online":  true,
		"card":    remote.Card,
	}, nil
}

func isPositiveInteger(s string) bool {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return err == nil && n > 0
}
