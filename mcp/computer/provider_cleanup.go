package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type providerLease struct {
	SessionID, Backend, ProviderID        string
	ConnectionID                          int64
	ProjectID, ProviderProjectID, BaseURL string
	Attempts                              int
	TerminalStatus                        string
}

func storeProviderLease(ctx *sdk.AppCtx, id string, comp backends.Computer, cfg backends.Config) error {
	if cfg.Type == "local" || cfg.Type == "service" {
		return nil
	}
	providerID := backendSessionID(comp)
	if providerID == "" {
		return nil
	}
	var connectionID int64
	if binding := ctx.IntegrationFor(cfg.Type); binding != nil {
		connectionID = int64(binding.ConnectionID)
	}
	_, err := ctx.AppDB().Exec(`INSERT INTO computer_provider_leases(session_id,backend,provider_id,connection_id,project_id,provider_project_id,base_url) VALUES(?,?,?,?,?,?,?) ON CONFLICT(session_id) DO NOTHING`, id, cfg.Type, providerID, connectionID, ctx.CurrentProject(), cfg.ProjectID, cfg.URL)
	return err
}
func leaseForSession(db *sql.DB, id string) (providerLease, error) {
	var l providerLease
	err := db.QueryRow(`SELECT session_id,backend,provider_id,connection_id,project_id,provider_project_id,base_url,attempts,terminal_status FROM computer_provider_leases WHERE session_id=?`, id).Scan(&l.SessionID, &l.Backend, &l.ProviderID, &l.ConnectionID, &l.ProjectID, &l.ProviderProjectID, &l.BaseURL, &l.Attempts, &l.TerminalStatus)
	return l, err
}
func leaseConfig(ctx *sdk.AppCtx, l providerLease) (backends.Config, error) {
	if l.ProjectID != "" {
		ctx = ctx.WithProject(l.ProjectID)
	}
	cfg := backendConfig(ctx, map[string]any{}, l.Backend, 0, 0, backends.EnvironmentOptions{})
	if l.ConnectionID != 0 {
		creds, err := ctx.PlatformAPI().GetConnectionCredentials(l.ConnectionID)
		if err != nil {
			return cfg, err
		}
		if creds == nil {
			return cfg, fmt.Errorf("provider credentials unavailable")
		}
		f := creds.Fields
		switch l.Backend {
		case "browserbase":
			cfg.APIKey = firstNonEmpty(f["api_key"], f["BROWSERBASE_API_KEY"])
		case "steel":
			cfg.APIKey = firstNonEmpty(f["token"], f["api_key"], f["STEEL_API_KEY"])
		case "browser-engine":
			cfg.APIKey = firstNonEmpty(f["BROWSER_API_KEY"], f["api_key"], f["token"])
		}
	}
	cfg.ProjectID, cfg.URL = l.ProviderProjectID, l.BaseURL
	return cfg, validateBackendConfigured(cfg)
}

var releaseProviderLease = func(ctx *sdk.AppCtx, l providerLease) error {
	cfg, err := leaseConfig(ctx, l)
	if err != nil {
		return err
	}
	method, base, suffix, header := "POST", "", "", ""
	var body []byte
	switch l.Backend {
	case "browserbase":
		base = "https://api.browserbase.com/v1"
		header = "X-BB-API-Key"
		body, _ = json.Marshal(map[string]string{"projectId": cfg.ProjectID, "status": "REQUEST_RELEASE"})
	case "steel":
		base = "https://api.steel.dev/v1"
		suffix = "/release"
		header = "Steel-Api-Key"
	case "browser-engine":
		method = "DELETE"
		base = firstNonEmpty(cfg.URL, "https://api.browserengine.co")
		header = "x-api-key"
	default:
		return fmt.Errorf("unsupported cleanup backend %s", l.Backend)
	}
	requestCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, method, strings.TrimRight(base, "/")+"/sessions/"+url.PathEscape(l.ProviderID)+suffix, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set(header, cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == 404 || resp.StatusCode == 410 || resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("provider release HTTP %d", resp.StatusCode)
}

func (a *App) reconcileProviderCleanup(ctx *sdk.AppCtx) {
	a.providerCleanupMu.Lock()
	defer a.providerCleanupMu.Unlock()
	rows, err := ctx.AppDB().Query(`SELECT session_id FROM computer_provider_leases WHERE pending=1 AND retry_after<=? LIMIT 20`, nowUTC())
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		lease, err := leaseForSession(ctx.AppDB(), id)
		if err != nil {
			continue
		}
		if err = releaseProviderLease(ctx, lease); err != nil {
			delay := time.Duration(min(300, 5*(1<<min(lease.Attempts, 6)))) * time.Second
			_, _ = ctx.AppDB().Exec(`UPDATE computer_provider_leases SET attempts=attempts+1,retry_after=? WHERE session_id=?`, time.Now().UTC().Add(delay).Format(time.RFC3339Nano), id)
			ctx.Logger().Warn("provider cleanup pending", "session_id", id, "err", err.Error())
			continue
		}
		tx, err := ctx.AppDB().Begin()
		if err != nil {
			continue
		}
		_, err = tx.Exec(`UPDATE computer_sessions SET status=?,updated_at=? WHERE id=? AND status IN ('cleanup_pending','interrupted')`, lease.TerminalStatus, nowUTC(), id)
		if err == nil {
			_, err = tx.Exec(`UPDATE computer_provider_leases SET pending=0,terminal_status='released' WHERE session_id=?`, id)
		}
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}
}

func (a *App) pruneSessionHistory(ctx *sdk.AppCtx) {
	days, err := strconv.Atoi(os.Getenv("APTEVA_COMPUTER_HISTORY_RETENTION_DAYS"))
	if err != nil || days <= 0 {
		return
	}
	if err := dbPruneSessionHistory(ctx.AppDB(), time.Now().AddDate(0, 0, -days)); err != nil {
		ctx.Logger().Warn("computer history retention failed", "err", err.Error())
	}
}

// Pre-upgrade rows have a provider id but no saved credential binding. Retain
// those ids too; reconciliation can try the operator's current configuration.
// Do not invent a historic connection identity or silently forget the resource.
func recoverLegacyProviderLeases(ctx *sdk.AppCtx) error {
	for _, backend := range []string{"browserbase", "steel", "browser-engine"} {
		cfg := backendConfig(ctx, map[string]any{}, backend, 0, 0, backends.EnvironmentOptions{})
		_, err := ctx.AppDB().Exec(`INSERT INTO computer_provider_leases(session_id,backend,provider_id,provider_project_id,base_url,pending,terminal_status)
   SELECT id,backend,backend_session_id,?,?,1,'interrupted' FROM computer_sessions s
   WHERE backend=? AND backend_session_id<>'' AND status IN ('active','interrupted','cleanup_pending')
   AND NOT EXISTS (SELECT 1 FROM computer_provider_leases l WHERE l.session_id=s.id)`, cfg.ProjectID, cfg.URL, backend)
		if err != nil {
			return err
		}
	}
	return nil
}
