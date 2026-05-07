package main

// Optional public exposure for dev runs via the Routes app.
//
// When repos_dev_start is called with expose=true, we publish the
// running dev process at <slug>.<dev_base_hostname> through the
// Routes app. Apteva-server's host router then proxies public
// requests to the dev process. The user must:
//   1. Install the Routes app.
//   2. Set the dev_base_hostname config on this Code install
//      (e.g. "dev.example.com").
//   3. Have a wildcard cert for *.<dev_base_hostname> in the Certs
//      app — without it, the route serves the server's self-signed
//      fallback (browser warning) until the real cert lands.
//
// Without any of these, expose=true degrades to a clear error
// message; the dev run itself still starts. None of this affects
// the local 127.0.0.1:<port> path which always works regardless.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func exposeDevRun(ctx *sdk.AppCtx, slug string, port int) (string, error) {
	if !routesAppAvailable(ctx) {
		return "", errors.New("Routes app not bound — install routes and bind it to the 'routes' role on this install")
	}
	base := strings.TrimSpace(ctx.Config().Get("dev_base_hostname"))
	if base == "" {
		return "", errors.New("dev_base_hostname not configured — set it in this install's Code app config (e.g. dev.example.com)")
	}
	hostname := strings.ToLower(slug + "." + strings.TrimPrefix(base, "."))
	target := fmt.Sprintf("http://127.0.0.1:%d", port)
	args := map[string]any{
		"hostname":         hostname,
		"target":           target,
		"owner_install_id": myInstallID(),
		"owner_kind":       "code",
		"cert_fqdn":        "*." + base, // wildcard naming convention
	}
	if err := callRoutesTool(ctx, "routes_register", args, nil); err != nil {
		return "", err
	}
	return hostname, nil
}

func unexposeDevRun(ctx *sdk.AppCtx, slug string) error {
	if !routesAppAvailable(ctx) {
		return nil
	}
	base := strings.TrimSpace(ctx.Config().Get("dev_base_hostname"))
	if base == "" {
		return nil
	}
	hostname := strings.ToLower(slug + "." + strings.TrimPrefix(base, "."))
	return callRoutesTool(ctx, "routes_unregister", map[string]any{
		"hostname":         hostname,
		"owner_install_id": myInstallID(),
	}, nil)
}

// routesAppAvailable reports whether the optional Routes app dep is
// bound on this install. Mirror of deploy's domainsAvailable shape.
func routesAppAvailable(ctx *sdk.AppCtx) bool {
	if ctx == nil {
		return false
	}
	bound := ctx.IntegrationFor("routes")
	return bound != nil && bound.Kind == "app"
}

// callRoutesTool unwraps the standard MCP envelope around CallApp's
// JSON-RPC response. Same shape as the deploy app's helper.
func callRoutesTool(ctx *sdk.AppCtx, tool string, args map[string]any, out any) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform unavailable")
	}
	raw, err := ctx.PlatformAPI().CallApp("routes", tool, args)
	if err != nil {
		return fmt.Errorf("call routes.%s: %w", tool, err)
	}
	var env struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decode routes.%s envelope: %w", tool, err)
	}
	if env.Error != nil {
		return fmt.Errorf("routes.%s: %s", tool, env.Error.Message)
	}
	content, _ := env.Result["content"].([]any)
	if len(content) == 0 {
		return nil
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if text == "" || out == nil {
		return nil
	}
	return json.Unmarshal([]byte(text), out)
}

// myInstallID reads APTEVA_INSTALL_ID from the env. Returns 0 when
// unset; the routes app rejects 0 with a clear error.
func myInstallID() int64 {
	v := strings.TrimSpace(os.Getenv("APTEVA_INSTALL_ID"))
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
