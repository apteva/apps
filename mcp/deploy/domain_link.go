package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Domain attach/detach goes through the optional Domains app. When
// the dep isn't bound we fall back to a free-text `domain` column —
// same as before this app learned about Domains.
//
// Wire layer: PlatformClient.CallApp(appName, tool, args) returns the
// full MCP JSON-RPC envelope. The actual tool result is JSON-encoded
// inside result.content[0].text, mirroring the Code app's repos_export
// path in sources.go.

// callDomainsTool invokes a tool on the Domains app and unwraps the
// MCP envelope into the tool's `any` result, decoded into out.
func callDomainsTool(ctx *sdk.AppCtx, tool string, args map[string]any, out any) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform unavailable")
	}
	raw, err := ctx.PlatformAPI().CallApp("domains", tool, args)
	if err != nil {
		return fmt.Errorf("call domains.%s: %w", tool, err)
	}
	var env struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decode domains.%s envelope: %w", tool, err)
	}
	if env.Error != nil {
		return fmt.Errorf("domains.%s: %s", tool, env.Error.Message)
	}
	content, _ := env.Result["content"].([]any)
	if len(content) == 0 {
		return fmt.Errorf("domains.%s returned empty content", tool)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if text == "" {
		return fmt.Errorf("domains.%s returned no text payload", tool)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return fmt.Errorf("decode domains.%s payload: %w", tool, err)
	}
	return nil
}

// domainsAvailable reports whether the optional Domains app dep is
// bound on this install. False means callers should treat the domain
// field as free text.
func (a *App) domainsAvailable(ctx *sdk.AppCtx) bool {
	if ctx == nil {
		return false
	}
	bound := ctx.IntegrationFor("domains")
	return bound != nil && bound.Kind == "app"
}

// resolveApex calls domains.domain_list and finds the registered apex
// that's a suffix of fqdn ("app.acme.com" → "acme.com"). Errors if
// the fqdn doesn't sit under any registered domain — that's the
// validation the picker UI is built around.
func resolveApex(ctx *sdk.AppCtx, fqdn string) (apex, sub string, err error) {
	var resp struct {
		Domains []struct {
			Name string `json:"name"`
		} `json:"domains"`
	}
	if err := callDomainsTool(ctx, "domain_list", map[string]any{}, &resp); err != nil {
		return "", "", err
	}
	fqdn = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(fqdn, ".")))
	best := ""
	for _, d := range resp.Domains {
		name := strings.ToLower(d.Name)
		if name == "" {
			continue
		}
		if fqdn == name || strings.HasSuffix(fqdn, "."+name) {
			if len(name) > len(best) {
				best = name
			}
		}
	}
	if best == "" {
		return "", "", fmt.Errorf("no registered domain matches %q — register it with the Domains app first", fqdn)
	}
	if fqdn == best {
		return best, "", nil
	}
	return best, strings.TrimSuffix(fqdn, "."+best), nil
}

// attachDomainSpec captures the inputs to attach. Wrapping them keeps
// the tool/HTTP/init paths from drifting.
type attachDomainSpec struct {
	FQDN   string
	Target string // CNAME target or A-record IP. Falls back to public_host config.
	Type   string // "CNAME" (default) | "A"
	TTL    int
}

// attachDomain validates the FQDN against the Domains app and writes
// the DNS record. Persists (domain, record_id, attached_at) on the
// deployment. record_id encodes "<apex>|<type>" so detach can target
// the same record without re-resolving.
func (a *App) attachDomain(ctx *sdk.AppCtx, d *Deployment, spec attachDomainSpec) error {
	if !a.domainsAvailable(ctx) {
		return errors.New("domains app not installed — install it or clear the domain field")
	}
	fqdn := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(spec.FQDN, ".")))
	if fqdn == "" {
		return errors.New("fqdn required")
	}
	rtype := strings.ToUpper(strings.TrimSpace(spec.Type))
	if rtype == "" {
		rtype = "CNAME"
	}
	if rtype != "CNAME" && rtype != "A" {
		return fmt.Errorf("unsupported record type %q (CNAME or A)", rtype)
	}
	target := strings.TrimSpace(spec.Target)
	if target == "" {
		target = strings.TrimSpace(configOr(globalCtx, "public_host", ""))
	}
	if target == "" {
		return errors.New("target required (no public_host configured) — set public_host on the deploy app or pass target")
	}
	if rtype == "CNAME" {
		// CNAME values are domain names; ensure no trailing dot.
		target = strings.TrimSuffix(target, ".")
	}
	ttl := spec.TTL
	if ttl <= 0 {
		ttl = 600
	}

	apex, sub, err := resolveApex(ctx, fqdn)
	if err != nil {
		return err
	}
	subArg := sub
	if subArg == "" {
		subArg = "@"
	}
	// CNAME at the apex isn't valid per RFC. Block it explicitly with
	// a friendlier message than whatever the registrar will say.
	if rtype == "CNAME" && sub == "" {
		return errors.New("apex CNAME isn't allowed; use type=A with an IP, or attach a subdomain")
	}

	if err := callDomainsTool(ctx, "domain_records_set", map[string]any{
		"domain": apex,
		"name":   subArg,
		"type":   rtype,
		"value":  target,
		"ttl":    ttl,
	}, nil); err != nil {
		return err
	}

	recordID := apex + "|" + rtype
	if err := dbSetDeploymentDomain(globalCtx.AppDB(), d.ID, fqdn, recordID, nowUTC()); err != nil {
		return err
	}
	emit("deploy.domain.attached", map[string]any{
		"deployment_id": d.ID, "fqdn": fqdn, "apex": apex, "type": rtype,
	})
	return nil
}

// detachDomain best-effort deletes the DNS record (if we know what we
// wrote) and clears the deployment's domain link. The DB clear runs
// even if the remote delete fails — leaving the row pointed at a
// dead record is worse than a leaked record at the registrar, which
// the user can clean up via the Domains panel.
func (a *App) detachDomain(ctx *sdk.AppCtx, d *Deployment) error {
	if d.Domain == "" && d.DomainRecordID == "" {
		return nil
	}
	var deleteErr error
	if d.DomainRecordID != "" && a.domainsAvailable(ctx) {
		apex, rtype, ok := splitRecordID(d.DomainRecordID)
		if ok {
			fqdn := strings.ToLower(strings.TrimSuffix(d.Domain, "."))
			sub := ""
			if fqdn != apex {
				sub = strings.TrimSuffix(fqdn, "."+apex)
			}
			subArg := sub
			if subArg == "" {
				subArg = "@"
			}
			deleteErr = callDomainsTool(ctx, "domain_records_delete", map[string]any{
				"domain": apex, "name": subArg, "type": rtype,
			}, nil)
		}
	}
	if err := dbSetDeploymentDomain(globalCtx.AppDB(), d.ID, "", "", ""); err != nil {
		return err
	}
	emit("deploy.domain.detached", map[string]any{
		"deployment_id": d.ID, "fqdn": d.Domain,
	})
	return deleteErr
}

func splitRecordID(s string) (apex, rtype string, ok bool) {
	i := strings.IndexByte(s, '|')
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}
