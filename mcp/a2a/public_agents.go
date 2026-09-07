package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type rawAgentCard struct {
	AgentCard
	ProtocolVersion string `json:"protocolVersion"`
}

type connectionView struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Kind            string   `json:"kind"`
	BaseURL         string   `json:"base_url"`
	CardURL         string   `json:"card_url,omitempty"`
	ProtocolVersion string   `json:"protocol_version,omitempty"`
	ManagedBy       string   `json:"managed_by"`
	Authenticated   bool     `json:"authenticated"`
	Agents          []string `json:"agents,omitempty"`
}

func publicConnectionID(cardURL string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(cardURL)))
	return "public_" + hex.EncodeToString(sum[:8])
}

func publicCardID(card *AgentCard) string {
	endpoint := ""
	if len(card.SupportedInterfaces) > 0 {
		endpoint = card.SupportedInterfaces[0].URL
	}
	sum := sha256.Sum256([]byte(card.Name + "\x00" + endpoint))
	return "card_" + hex.EncodeToString(sum[:12])
}

func cardCandidates(raw string) ([]string, error) { return cardCandidatesFor(raw, false) }
func cardCandidatesFor(raw string, allowLoopback bool) ([]string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("card_url must be an absolute URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && allowLoopback && isLoopbackHost(u.Hostname())) {
		return nil, errors.New("card_url must use HTTPS (HTTP is allowed only for loopback development)")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("card_url cannot contain user info, query, or fragment")
	}
	if isLoopbackHost(u.Hostname()) && !allowLoopback {
		return nil, errors.New("card_url cannot target a private network")
	}
	if !isLoopbackHost(u.Hostname()) {
		if strings.HasSuffix(strings.ToLower(u.Hostname()), ".local") {
			return nil, errors.New("card_url cannot target a private host")
		}
		if ip := net.ParseIP(u.Hostname()); ip != nil && !publicIPAllowed(ip, allowLoopback) {
			return nil, errors.New("card_url cannot target a private network")
		}
	}
	if strings.Contains(u.Path, "/.well-known/") || strings.HasSuffix(u.Path, ".json") {
		return []string{u.String()}, nil
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/.well-known/agent-card.json"
	modern := u.String()
	u.Path = strings.TrimSuffix(u.Path, "agent-card.json") + "agent.json"
	return []string{modern, u.String()}, nil
}

func (a *App) fetchPublicAgentCard(ctx context.Context, app *sdk.AppCtx, rawURL, token string) (*AgentCard, string, error) {
	candidates, err := cardCandidatesFor(rawURL, allowPublicLoopback(app))
	if err != nil {
		return nil, "", err
	}
	var lastErr error
	for _, cardURL := range candidates {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, cardURL, nil)
		if reqErr != nil {
			return nil, "", reqErr
		}
		if strings.TrimSpace(token) != "" {
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
		}
		res, fetchErr := a.publicHTTPClient(app).Do(req)
		if fetchErr != nil {
			lastErr = fetchErr
			continue
		}
		if res.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("Agent Card returned HTTP %d", res.StatusCode)
			res.Body.Close()
			continue
		}
		var rawCard rawAgentCard
		decodeErr := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&rawCard)
		res.Body.Close()
		if decodeErr != nil {
			lastErr = fmt.Errorf("invalid Agent Card: %w", decodeErr)
			continue
		}
		card := rawCard.AgentCard
		if card.Name == "" {
			return nil, "", errors.New("Agent Card is missing name")
		}
		if len(card.SupportedInterfaces) == 0 && strings.TrimSpace(card.URL) != "" {
			version := strings.TrimSpace(rawCard.ProtocolVersion)
			if version == "" {
				version = "0.3.0"
			}
			card.SupportedInterfaces = []AgentInterface{{
				ProtocolBinding: "JSONRPC", ProtocolVersion: version, URL: card.URL,
			}}
		}
		var selected *AgentInterface
		selectedIndex := -1
		for i := range card.SupportedInterfaces {
			binding := strings.ToUpper(strings.ReplaceAll(card.SupportedInterfaces[i].ProtocolBinding, "-", ""))
			if binding == "JSONRPC" {
				candidate := card.SupportedInterfaces[i]
				selected = &candidate
				selectedIndex = i
				break
			}
		}
		if selected == nil || strings.TrimSpace(selected.URL) == "" {
			return nil, "", errors.New("Agent Card does not advertise a JSON-RPC interface")
		}
		cardOrigin, _ := url.Parse(cardURL)
		endpoint, endpointErr := url.Parse(selected.URL)
		if endpointErr != nil || endpoint.Scheme != cardOrigin.Scheme || !strings.EqualFold(endpoint.Host, cardOrigin.Host) {
			return nil, "", errors.New("Agent Card interface must remain on the card origin")
		}
		if endpoint.User != nil || endpoint.Fragment != "" {
			return nil, "", errors.New("Agent Card interface URL is invalid")
		}
		if selected.ProtocolVersion == "" {
			selected.ProtocolVersion = rawCard.ProtocolVersion
		}
		if selected.ProtocolVersion == "" {
			selected.ProtocolVersion = "0.3.0"
		}
		interfaces := []AgentInterface{*selected}
		for i, intf := range card.SupportedInterfaces {
			if i != selectedIndex {
				interfaces = append(interfaces, intf)
			}
		}
		card.SupportedInterfaces = interfaces
		return &card, cardURL, nil
	}
	if lastErr == nil {
		lastErr = errors.New("Agent Card could not be fetched")
	}
	return nil, "", lastErr
}

func (a *App) connectPublicAgent(ctx context.Context, app *sdk.AppCtx, rawURL, token, managedBy string) (*remoteAgent, *peerConfig, error) {
	candidates, err := cardCandidatesFor(rawURL, allowPublicLoopback(app))
	if err != nil {
		return nil, nil, err
	}
	var retained *peerConfig
	if managedBy == "agent" {
		for _, candidate := range candidates {
			records, e := loadPeerRecordsWhere(app, "WHERE id = ?", publicConnectionID(candidate))
			if e != nil {
				return nil, nil, e
			}
			if len(records) > 0 {
				existing := records[0].peerConfig
				if existing.Kind != "agent_card" {
					return nil, nil, errors.New("connection id conflict")
				}
				retained = &existing
				token = existing.Token
				break
			}
		}
	}
	card, cardURL, err := a.fetchPublicAgentCard(ctx, app, rawURL, token)
	if err != nil {
		return nil, nil, err
	}
	cardParsed, _ := url.Parse(cardURL)
	baseURL := cardParsed.Scheme + "://" + cardParsed.Host
	peer := peerConfig{
		ID: publicConnectionID(cardURL), Name: card.Name, BaseURL: baseURL,
		Token: strings.TrimSpace(token), Kind: "agent_card", DiscoveryURL: cardURL,
		ProtocolVersion: card.SupportedInterfaces[0].ProtocolVersion, ManagedBy: managedBy,
	}
	if retained != nil {
		remote, err := upsertRemoteAgent(app.AppDB(), *retained, directoryEntry{CardID: publicCardID(card), Name: card.Name, Description: card.Description, Online: true, Skills: skillIDs(card.Skills)}, card, configDuration(app, "card_cache_seconds", defaultCardCacheSeconds))
		return remote, retained, err
	}
	var existingManaged, existingKind string
	if err := app.AppDB().QueryRow(`SELECT managed_by, kind FROM a2a_peers WHERE id = ?`, peer.ID).Scan(&existingManaged, &existingKind); err == nil && existingManaged != "" {
		if existingKind != "agent_card" {
			return nil, nil, errors.New("generated public connection id conflicts with an A2A node")
		}
		if existingManaged == "config" || existingManaged == "app" {
			existing, findErr := findPeer(app, peer.ID)
			if findErr != nil {
				return nil, nil, findErr
			}
			remote, upsertErr := upsertRemoteAgent(app.AppDB(), *existing, directoryEntry{
				CardID: publicCardID(card), Name: card.Name, Description: card.Description,
				Online: true, Skills: skillIDs(card.Skills),
			}, card, time.Duration(configInt(app, "card_cache_seconds", defaultCardCacheSeconds))*time.Second)
			return remote, existing, upsertErr
		}
		peer.ManagedBy = existingManaged
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	if err := normalizePeer(&peer); err != nil {
		return nil, nil, err
	}
	keys, err := loadPeerKeyring(app)
	if err != nil {
		return nil, nil, err
	}
	if err := storePeer(app.AppDB(), keys, peer, nil); err != nil {
		return nil, nil, err
	}
	remote, err := upsertRemoteAgent(app.AppDB(), peer, directoryEntry{
		CardID: publicCardID(card), Name: card.Name, Description: card.Description,
		Online: true, Skills: skillIDs(card.Skills),
	}, card, time.Duration(configInt(app, "card_cache_seconds", defaultCardCacheSeconds))*time.Second)
	return remote, &peer, err
}

func (a *App) handleConnections(w http.ResponseWriter, r *http.Request) {
	app := appContextForRequest(r)
	if app == nil {
		http.Error(w, "not mounted", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		records, err := loadPeerRecords(app)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		views := make([]connectionView, 0, len(records))
		for _, record := range records {
			view := connectionView{ID: record.ID, Name: record.Name, Kind: record.Kind,
				BaseURL: record.BaseURL, CardURL: record.DiscoveryURL,
				ProtocolVersion: record.ProtocolVersion, ManagedBy: record.ManagedBy,
				Authenticated: record.Token != ""}
			rows, _ := app.AppDB().Query(`SELECT name FROM a2a_remote_agents WHERE peer_id = ? ORDER BY name`, record.ID)
			if rows != nil {
				for rows.Next() {
					var name string
					if rows.Scan(&name) == nil {
						view.Agents = append(view.Agents, name)
					}
				}
				rows.Close()
			}
			views = append(views, view)
		}
		writeJSON(w, map[string]any{"connections": views})
	case http.MethodPost:
		var input struct {
			Kind           string   `json:"kind"`
			ID             string   `json:"id"`
			Name           string   `json:"name"`
			BaseURL        string   `json:"base_url"`
			CardURL        string   `json:"card_url"`
			Token          string   `json:"token"`
			DiscoverAgents []string `json:"discover_agents"`
			InvokeAgents   []string `json:"invoke_agents"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if input.Kind == "" || input.Kind == "agent_card" {
			remote, peer, err := a.connectPublicAgent(r.Context(), app, input.CardURL, input.Token, "operator")
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"connection": connectionView{ID: peer.ID, Name: peer.Name,
				Kind: peer.Kind, BaseURL: peer.BaseURL, CardURL: peer.DiscoveryURL,
				ProtocolVersion: peer.ProtocolVersion, ManagedBy: peer.ManagedBy,
				Authenticated: peer.Token != "", Agents: []string{remote.Name}},
				"agent": discoverEntry{Address: "a2a:" + remote.Ref, Name: remote.Name,
					Description: remote.Description, Online: true, Peer: peer.Name, Skills: remote.Skills}})
			return
		}
		peer := peerConfig{ID: input.ID, Name: input.Name, BaseURL: input.BaseURL, Token: input.Token,
			Kind: "node", ManagedBy: "operator", DiscoverAgents: input.DiscoverAgents, InvokeAgents: input.InvokeAgents}
		if err := normalizePeer(&peer); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var existingManaged string
		var existingOwner sql.NullInt64
		existingErr := app.AppDB().QueryRow(`SELECT managed_by, owner_install_id FROM a2a_peers WHERE id = ?`, peer.ID).Scan(&existingManaged, &existingOwner)
		if existingErr == nil && (existingOwner.Valid || existingManaged == "config" || existingManaged == "app") {
			http.Error(w, "connection is managed by configuration or another app", http.StatusConflict)
			return
		}
		if existingErr != nil && !errors.Is(existingErr, sql.ErrNoRows) {
			http.Error(w, existingErr.Error(), http.StatusInternalServerError)
			return
		}
		keys, err := loadPeerKeyring(app)
		if err == nil {
			err = storePeer(app.AppDB(), keys, peer, nil)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"connection": connectionView{ID: peer.ID, Name: peer.Name,
			Kind: peer.Kind, BaseURL: peer.BaseURL, ManagedBy: peer.ManagedBy, Authenticated: true}})
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleConnectionItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE only", http.StatusMethodNotAllowed)
		return
	}
	app := appContextForRequest(r)
	if app == nil {
		http.Error(w, "not mounted", http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/connections/"))
	var managed string
	var owner sql.NullInt64
	err := app.AppDB().QueryRow(`SELECT managed_by, owner_install_id FROM a2a_peers WHERE id = ?`, id).Scan(&managed, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, map[string]any{"id": id, "removed": false})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if owner.Valid || managed == "config" || managed == "app" {
		http.Error(w, "connection is managed by configuration or another app", http.StatusConflict)
		return
	}
	tx, err := app.AppDB().Begin()
	if err == nil {
		_, err = tx.Exec(`DELETE FROM a2a_remote_agents WHERE peer_id = ?`, id)
	}
	if err == nil {
		_, err = tx.Exec(`DELETE FROM a2a_peers WHERE id = ?`, id)
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"id": id, "removed": true})
}
