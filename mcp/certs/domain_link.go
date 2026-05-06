package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Cross-app calls to the Domains app. Same envelope shape as the
// deploy app's domain_link.go — the MCP JSON-RPC response carries
// the tool result as JSON inside result.content[0].text.

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

// resolveApex looks up the registered apex domain that's a suffix of
// fqdn. Used both for the ACME challenge TXT placement and to
// validate that this project even owns the FQDN.
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
		return "", "", fmt.Errorf("no registered domain matches %q — register the apex with the Domains app first", fqdn)
	}
	if fqdn == best {
		return best, "", nil
	}
	return best, strings.TrimSuffix(fqdn, "."+best), nil
}

// challengeRecordName returns the TXT record name to write for the
// DNS-01 challenge. The full FQDN of the TXT is "_acme-challenge.<fqdn>"
// — but the Domains app expects the *subdomain* relative to the apex.
func challengeRecordName(apex, sub string) string {
	if sub == "" {
		return "_acme-challenge"
	}
	return "_acme-challenge." + sub
}

// setChallengeTXT and deleteChallengeTXT thin wrappers — the tool
// names match the Domains app exactly.
func setChallengeTXT(ctx *sdk.AppCtx, apex, sub, value string) error {
	return callDomainsTool(ctx, "domain_records_set", map[string]any{
		"domain": apex,
		"name":   challengeRecordName(apex, sub),
		"type":   "TXT",
		"value":  value,
		"ttl":    60,
	}, nil)
}

func deleteChallengeTXT(ctx *sdk.AppCtx, apex, sub string) error {
	return callDomainsTool(ctx, "domain_records_delete", map[string]any{
		"domain": apex,
		"name":   challengeRecordName(apex, sub),
		"type":   "TXT",
	}, nil)
}
