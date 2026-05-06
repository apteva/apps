package main

import (
	"errors"
	"fmt"
	"strconv"

	sdk "github.com/apteva/app-sdk"
)

// MCPTools — agent-facing surface. cert_material is the only tool
// that returns key material; it's gated to caller=server|certs via
// allowedMaterialCaller.
func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "cert_issue", Handler: a.toolIssue,
			Description: "Issue a TLS cert for an FQDN. Async — returns the cert row with status=issuing or pending; poll cert_get for completion. Args: fqdn.",
			InputSchema: schemaObject(map[string]any{
				"fqdn": map[string]any{"type": "string"},
			}, []string{"fqdn"}),
		},
		{
			Name: "cert_get", Handler: a.toolGet,
			Description: "Fetch one cert by id or fqdn. Args: id OR fqdn.",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "integer"},
				"fqdn": map[string]any{"type": "string"},
			}, nil),
		},
		{
			Name: "cert_list", Handler: a.toolList,
			Description: "List certs in this project. Args: include_revoked? (default false).",
			InputSchema: schemaObject(map[string]any{
				"include_revoked": map[string]any{"type": "boolean"},
			}, nil),
		},
		{
			Name: "cert_material", Handler: a.toolMaterial,
			Description: "PEM cert + key. PRIVILEGED — only callable from the server (TLS cache) or this app. Args: fqdn.",
			InputSchema: schemaObject(map[string]any{
				"fqdn": map[string]any{"type": "string"},
			}, []string{"fqdn"}),
		},
		{
			Name: "cert_revoke", Handler: a.toolRevoke,
			Description: "Revoke a cert (best-effort) and mark it revoked locally. Args: id OR fqdn.",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "integer"},
				"fqdn": map[string]any{"type": "string"},
			}, nil),
		},
		{
			Name: "cert_renew", Handler: a.toolRenew,
			Description: "Force-renew a cert now. The renewal worker also runs daily for certs in the renewal window. Args: id OR fqdn.",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "integer"},
				"fqdn": map[string]any{"type": "string"},
			}, nil),
		},
	}
}

// ─── tool handlers ────────────────────────────────────────────────

func (a *App) toolIssue(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fqdn := strArg(args, "fqdn")
	if fqdn == "" {
		return nil, errors.New("fqdn required")
	}
	row, err := dbInsertOrTouchCert(ctx.AppDB(), pid, fqdn)
	if err != nil {
		return nil, err
	}
	a.kickIssuance(ctx, pid, fqdn)
	return map[string]any{"cert": row}, nil
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	c, err := a.lookupCert(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"cert": c}, nil
}

func (a *App) toolList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := dbListCerts(ctx.AppDB(), pid, boolArg(args, "include_revoked"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"certs": rows, "count": len(rows)}, nil
}

func (a *App) toolMaterial(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if !allowedMaterialCaller(ctx) {
		return nil, errors.New("cert_material is privileged — caller not allowed")
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fqdn := strArg(args, "fqdn")
	if fqdn == "" {
		return nil, errors.New("fqdn required")
	}
	m, err := dbCertMaterial(ctx.AppDB(), pid, fqdn)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return map[string]any{"found": false, "fqdn": fqdn}, nil
	}
	return map[string]any{
		"found":      true,
		"fqdn":       m.FQDN,
		"cert_pem":   string(m.CertPEM),
		"key_pem":    string(m.KeyPEM),
		"issued_at":  m.Issued.UTC().Format("2006-01-02T15:04:05Z"),
		"expires_at": m.Expires.UTC().Format("2006-01-02T15:04:05Z"),
	}, nil
}

func (a *App) toolRevoke(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	c, err := a.lookupCert(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	// We don't try to revoke at the ACME server — most issuers
	// auto-revoke when the key is rotated, and a client-driven revoke
	// for a cert we may have already lost the key for is brittle.
	// Mark it revoked locally so the cache stops serving it.
	if err := dbSetCertStatus(ctx.AppDB(), c.ID, "revoked", ""); err != nil {
		return nil, err
	}
	emit("certs.revoked", map[string]any{"cert_id": c.ID, "fqdn": c.FQDN})
	return map[string]any{"revoked": true, "id": c.ID, "fqdn": c.FQDN}, nil
}

func (a *App) toolRenew(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	c, err := a.lookupCert(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	a.kickIssuance(ctx, pid, c.FQDN)
	out, _ := dbGetCert(ctx.AppDB(), c.ID)
	return map[string]any{"cert": out}, nil
}

// ─── helpers ───────────────────────────────────────────────────────

func (a *App) lookupCert(ctx *sdk.AppCtx, pid string, args map[string]any) (*Cert, error) {
	if id := int64(intArg(args, "id")); id != 0 {
		c, err := dbGetCert(ctx.AppDB(), id)
		if err != nil || c == nil {
			return nil, fmt.Errorf("cert %d not found", id)
		}
		return c, nil
	}
	if fqdn := strArg(args, "fqdn"); fqdn != "" {
		c, err := dbGetCertByFQDN(ctx.AppDB(), pid, fqdn)
		if err != nil {
			return nil, err
		}
		if c == nil {
			return nil, fmt.Errorf("cert for %q not found", fqdn)
		}
		return c, nil
	}
	return nil, errors.New("id or fqdn required")
}

// kickIssuance starts an async issuance goroutine for fqdn unless one
// is already in flight. The user-facing tool always returns
// immediately — completion lands via cert_get / events.
func (a *App) kickIssuance(ctx *sdk.AppCtx, projectID, fqdn string) {
	release, held := a.withIssuanceLock(fqdn)
	if !held {
		return
	}
	go func() {
		defer release()
		if err := a.issueCert(ctx, projectID, fqdn); err != nil {
			emit("certs.issuance.failed", map[string]any{
				"fqdn": fqdn, "error": err.Error(),
			})
		}
	}()
}

// renewalPass scans for live certs entering the renewal window and
// kicks issuance for each. Idempotent — kickIssuance no-ops when one
// is already running.
func (a *App) renewalPass() {
	if globalCtx == nil || globalCtx.AppDB() == nil {
		return
	}
	rows, err := dbDueForRenewal(globalCtx.AppDB(), a.renewWindow)
	if err != nil {
		globalCtx.Logger().Warn("renewal scan failed", "err", err)
		return
	}
	for _, c := range rows {
		globalCtx.Logger().Info("renewing cert", "fqdn", c.FQDN, "expires_at", c.ExpiresAt)
		a.kickIssuance(globalCtx, c.ProjectID, c.FQDN)
	}
}

// allowedMaterialCaller decides whether cert_material may be served.
// v1 policy: same-app calls (renewal worker exercising it) and calls
// from the server are allowed. The platform stamps cross-app calls
// with the caller identity in WhoAmI(); we trust that envelope.
func allowedMaterialCaller(ctx *sdk.AppCtx) bool {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return false
	}
	id, err := ctx.PlatformAPI().WhoAmI()
	if err != nil || id == nil {
		return false
	}
	switch id.AppName {
	case "certs", "server":
		return true
	}
	return false
}

// schemaObject is a small helper that mirrors the domains/deploy apps'
// schema-builder so tool definitions stay terse.
func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// ─── arg helpers ──────────────────────────────────────────────────

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func intArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

func boolArg(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	}
	return false
}
