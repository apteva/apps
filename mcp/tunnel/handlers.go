package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/tunnel/protocol"
	"github.com/gorilla/websocket"
)

const maxAdminBody = 64 << 10

var connectorUpgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	CheckOrigin: func(r *http.Request) bool {
		// Connector credentials must not be usable by arbitrary browser
		// origins. Native clients do not send Origin.
		return strings.TrimSpace(r.Header.Get("Origin")) == ""
	},
	EnableCompression: true,
}

func (a *App) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleConnect(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		writeError(w, http.StatusUnauthorized, "connector bearer token required")
		return
	}
	item, err := a.store.activeTunnelByTokenHash(tokenDigest(token))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid or revoked connector token")
		return
	}
	conn, err := connectorUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	active := a.connectors.attach(item.ID, conn, a.maxConcurrent)
	a.store.markConnected(item.ID)
	a.ctx.EmitWithProject("tunnel.connected", item.ProjectID, map[string]any{
		"tunnel_id": item.ID,
		"hostname":  item.Hostname,
	})
	go active.pingLoop()
	_ = active.readLoop(a.maxRequestBody)
	a.connectors.detach(active)
	a.store.markDisconnected(item.ID)
	a.ctx.EmitWithProject("tunnel.disconnected", item.ProjectID, map[string]any{
		"tunnel_id": item.ID,
		"hostname":  item.Hostname,
	})
}

func (a *App) handlePublic(w http.ResponseWriter, r *http.Request) {
	host := inboundHost(r)
	cfg, err := a.store.config()
	if err != nil || !hostBelongsToBase(host, cfg.BaseDomain) {
		http.NotFound(w, r)
		return
	}
	item, err := a.store.activeTunnelByHost(host)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	active := a.connectors.get(item.ID)
	if active == nil {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "tunnel connector is offline")
		return
	}
	body, err := readLimited(r.Body, a.maxRequestBody)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.requestTimeout)
	defer cancel()
	response, err := active.roundTrip(ctx, protocol.Message{
		Method:   r.Method,
		Path:     r.URL.EscapedPath(),
		RawQuery: r.URL.RawQuery,
		Headers:  publicForwardHeaders(r),
		Body:     body,
	})
	if err != nil {
		if errors.Is(err, errConnectorBusy) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "tunnel concurrency limit reached")
		} else if errors.Is(err, context.DeadlineExceeded) {
			writeError(w, http.StatusGatewayTimeout, "tunnel target timed out")
		} else {
			writeError(w, http.StatusBadGateway, "tunnel target unavailable")
		}
		return
	}
	for key, values := range response.Headers {
		if isHopHeader(key) {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.Header().Set("Via", "Apteva-Tunnel/1")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(response.Body)
	a.usage.record(item.ID, int64(len(body)), int64(len(response.Body)))
}

func (a *App) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.store.config()
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured":    false,
			"can_configure": true,
			"domains_bound": a.ctx.IntegrationFor("domains") != nil,
			"dns":           dnsStatus{},
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load tunnel configuration")
		return
	}
	writeJSON(w, http.StatusOK, configResponse(
		cfg,
		a.ctx.IntegrationFor("domains") != nil,
		requestProjectID(r, ""),
	))
}

type createTunnelInput struct {
	Name      string `json:"name"`
	ProjectID string `json:"project_id"`
}

func (a *App) handleCreateTunnel(w http.ResponseWriter, r *http.Request) {
	var input createTunnelInput
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	projectID := requestProjectID(r, input.ProjectID)
	result, err := a.createTunnel(projectID, input.Name)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (a *App) handleListTunnels(w http.ResponseWriter, r *http.Request) {
	projectID := requestProjectID(r, "")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	_ = a.usage.flush()
	items, err := a.store.listTunnels(projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list tunnels")
		return
	}
	for i := range items {
		items[i].Connected = a.connectors.connected(items[i].ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tunnels": items, "count": len(items)})
}

func (a *App) handleRotateTunnel(w http.ResponseWriter, r *http.Request) {
	projectID := requestProjectID(r, "")
	token, err := randomSecret("aptun_", 32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create connector token")
		return
	}
	item, err := a.store.rotateToken(r.PathValue("id"), projectID, tokenDigest(token))
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "tunnel not found")
		} else {
			writeError(w, http.StatusInternalServerError, "could not rotate connector token")
		}
		return
	}
	a.connectors.disconnect(item.ID, "connector token rotated")
	writeJSON(w, http.StatusOK, connectorCredentialResponse(a, item, token))
}

func (a *App) handleDeleteTunnel(w http.ResponseWriter, r *http.Request) {
	projectID := requestProjectID(r, "")
	item, err := a.store.revokeTunnel(r.PathValue("id"), projectID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "tunnel not found")
		} else {
			writeError(w, http.StatusInternalServerError, "could not revoke tunnel")
		}
		return
	}
	a.connectors.disconnect(item.ID, "tunnel revoked")
	if err := a.ctx.PlatformAPI().UnexposeIngress(item.Hostname); err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"deleted": true,
			"warning": "tunnel revoked but its ingress route could not be removed: " + err.Error(),
		})
		return
	}
	a.ctx.EmitWithProject("tunnel.deleted", projectID, map[string]any{
		"tunnel_id": item.ID,
		"hostname":  item.Hostname,
	})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (a *App) handleStats(w http.ResponseWriter, r *http.Request) {
	projectID := requestProjectID(r, "")
	_ = a.usage.flush()
	stats, err := a.store.stats(projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load tunnel statistics")
		return
	}
	connected := 0
	items, _ := a.store.listTunnels(projectID)
	for i := range items {
		if a.connectors.connected(items[i].ID) {
			connected++
		}
	}
	stats["connected_tunnels"] = connected
	stats["max_tunnels"] = a.maxTunnels
	writeJSON(w, http.StatusOK, stats)
}

func (a *App) createTunnel(projectID, requestedName string) (map[string]any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()

	if projectID == "" {
		return nil, newClientError("project_id is required")
	}
	name, err := normalizeTunnelName(requestedName)
	if err != nil {
		return nil, newClientError(err.Error())
	}
	cfg, err := a.store.config()
	if errors.Is(err, sql.ErrNoRows) {
		return nil, newClientError("configure a base domain before creating tunnels")
	}
	if err != nil {
		return nil, err
	}
	count, err := a.store.activeTunnelCount(projectID)
	if err != nil {
		return nil, err
	}
	if count >= a.maxTunnels {
		return nil, newClientError(fmt.Sprintf("project tunnel quota reached (%d)", a.maxTunnels))
	}
	hostname := name + "." + cfg.BaseDomain
	token, err := randomSecret("aptun_", 32)
	if err != nil {
		return nil, err
	}
	item, err := a.store.createTunnel(projectID, name, hostname, tokenDigest(token))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, newConflictError("tunnel name is already reserved")
		}
		return nil, err
	}
	_, err = a.ctx.WithProject(projectID).PlatformAPI().ExposeIngress(ingressRequest(hostname))
	if err != nil {
		_, _ = a.store.revokeTunnel(item.ID, projectID)
		return nil, fmt.Errorf("reserve public ingress: %w", err)
	}
	a.ctx.EmitWithProject("tunnel.created", projectID, map[string]any{
		"tunnel_id": item.ID,
		"hostname":  hostname,
	})
	return connectorCredentialResponse(a, item, token), nil
}

func ingressRequest(hostname string) sdk.IngressExposeRequest {
	return sdk.IngressExposeRequest{
		Hostname:  hostname,
		Target:    "app://tunnel?ingress_auth=app_token",
		OwnerKind: "tunnel",
		CertFQDN:  hostname,
		AllowHTTP: false,
		TLSMode:   "auto",
	}
}

func connectorCredentialResponse(a *App, item *tunnel, token string) map[string]any {
	item.Connected = a.connectors.connected(item.ID)
	return map[string]any{
		"tunnel":          item,
		"connector_token": token,
		"connect_url":     a.connectURL(),
		"notice":          "The connector token is shown once. Store it securely; rotate it if lost.",
	}
}

func (a *App) connectURL() string {
	if info, err := a.ctx.PlatformInfo(); err == nil && info != nil && strings.TrimSpace(info.PublicURL) != "" {
		return strings.TrimRight(info.PublicURL, "/") + "/api/apps/tunnel/v1/connect"
	}
	return "/api/apps/tunnel/v1/connect"
}

func requestProjectID(r *http.Request, bodyValue string) string {
	if value := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID")); value != "" {
		return value
	}
	if value := strings.TrimSpace(r.URL.Query().Get("project_id")); value != "" {
		return value
	}
	return strings.TrimSpace(bodyValue)
}

func inboundHost(r *http.Request) string {
	host := r.Host
	if value := r.Header.Get("X-Forwarded-Host"); value != "" {
		host = value
	}
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

func hostBelongsToBase(host, base string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	base = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(base), "."))
	if host == "" || base == "" || host == base {
		return false
	}
	prefix := strings.TrimSuffix(host, "."+base)
	return prefix != host && prefix != "" && !strings.Contains(prefix, ".")
}

func bearerToken(value string) string {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return parts[1]
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, errors.New("could not read request body")
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("body exceeds configured limit of %d bytes", limit)
	}
	return body, nil
}

func readJSON(w http.ResponseWriter, r *http.Request, output any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return errors.New("invalid JSON request")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}
