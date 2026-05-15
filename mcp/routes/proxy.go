package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// proxy.go — routing_mode: proxy.
//
// In proxy mode the routes app drives an external reverse proxy
// (Caddy or nginx) instead of relying on apteva-server's HostRouter.
// It detects the proxy, makes a one-time guarded edit so the proxy's
// main config `import`s an app-owned include file, then keeps that
// include file in sync with the routes table.
//
// The app owns ONLY its include file. The operator's main config is
// touched exactly once — backed up, validated, rolled back on failure
// — and never again. Hand-written operator blocks are left alone.

// proxyTarget is a detected reverse proxy plus everything needed to
// drive it.
type proxyTarget struct {
	kind        string   // "caddy" | "nginx"
	binary      string   // resolved path to the caddy/nginx binary
	mainConfig  string   // /etc/caddy/Caddyfile, /etc/nginx/nginx.conf, …
	includeDir  string   // app-owned dir the main config imports
	includePath string   // the single file we render into includeDir
	importLine  string   // the line the main config needs to pull in includeDir
	reloadCmd   []string // argv to gracefully reload the proxy
	renderer    proxyRenderer
}

// ─── lifecycle ─────────────────────────────────────────────────────

// startProxyMode runs from OnMount when routing_mode == proxy: detect,
// bootstrap, initial sync, then a periodic resync loop.
func (a *App) startProxyMode(ctx *sdk.AppCtx) {
	kind := configOr(ctx, "proxy_kind", "auto")
	configPath := configOr(ctx, "proxy_config_path", "")
	reloadOverride := configOr(ctx, "proxy_reload_command", "")
	p, err := detectProxy(kind, configPath, reloadOverride)
	if err != nil {
		ctx.Logger().Warn("routes: proxy mode set but no reverse proxy detected — staying inert",
			"proxy_kind", kind, "err", err)
		return
	}
	a.proxy = p
	ctx.Logger().Info("routes: reverse proxy detected",
		"kind", p.kind, "main_config", p.mainConfig, "include", p.includePath)

	if err := bootstrapProxy(p); err != nil {
		ctx.Logger().Warn("routes: proxy bootstrap failed — the include file will still be written, "+
			"but add the import line to your main config by hand",
			"import_line", p.importLine, "err", err)
	}
	a.syncProxy(ctx, "startup")
	go a.proxySyncLoop(ctx)
}

// syncProxy renders the include file from the routes table and reloads
// the proxy — but only when the rendered content actually differs from
// what's on disk, so a periodic resync is cheap and a reload fires
// only on a real diff.
//
// Drift detection is on-disk, not in-memory. The earlier version
// cached the last-written content in a.lastInclude; an out-of-band
// edit to the Caddyfile would diverge from the table forever because
// in-memory state still matched the renderer's output. Reading the
// file on every tick is microseconds, and the periodic sync loop
// (45s) now auto-converges any hand-edit on the next tick — that
// satisfies the "drift detection / reconcile pass" requirement
// without needing a separate `routes_reconcile` tool.
func (a *App) syncProxy(ctx *sdk.AppCtx, reason string) {
	if a.proxy == nil {
		return
	}
	a.syncMu.Lock()
	defer a.syncMu.Unlock()

	routes, err := dbListRoutes(ctx.AppDB(), nil)
	if err != nil {
		ctx.Logger().Warn("routes: sync — list failed", "err", err)
		return
	}
	content := a.proxy.renderer.render(routes, a.certDir)
	onDisk, _ := os.ReadFile(a.proxy.includePath) // empty bytes on ENOENT, fine for the compare below
	if string(onDisk) == content {
		a.lastInclude = content // keep the cache in sync for the early-return fast path below
		return                   // file matches the table — nothing to do
	}
	if err := os.MkdirAll(a.proxy.includeDir, 0o755); err != nil {
		ctx.Logger().Warn("routes: sync — mkdir include dir failed", "dir", a.proxy.includeDir, "err", err)
		return
	}
	if err := atomicWriteFile(a.proxy.includePath, []byte(content), 0o644); err != nil {
		ctx.Logger().Warn("routes: sync — write include failed", "path", a.proxy.includePath, "err", err)
		return
	}
	a.lastInclude = content
	if err := reloadProxy(a.proxy); err != nil {
		ctx.Logger().Warn("routes: sync — proxy reload failed (include written; reload by hand or check proxy_reload_command)",
			"err", err)
		return
	}
	ctx.Logger().Info("routes: proxy config synced", "reason", reason, "routes", len(routes), "drift", string(onDisk) != "" && string(onDisk) != content)
}

// proxySyncLoop catches drift the event path can't see — most
// importantly a cert landing AFTER its route was registered (issuance
// is async). render→diff means a reload only fires on a real change.
func (a *App) proxySyncLoop(ctx *sdk.AppCtx) {
	t := time.NewTicker(45 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.stopCh:
			return
		case <-t.C:
			a.syncProxy(ctx, "periodic")
		}
	}
}

// ─── detection ─────────────────────────────────────────────────────

// detectProxy finds the installed reverse proxy. kind is "auto",
// "caddy", or "nginx". configPath, when non-empty, is used as the
// proxy's main config instead of auto-locating it (the escape hatch
// for non-standard installs). reloadOverride, when non-empty,
// replaces the per-kind default reload command.
func detectProxy(kind, configPath, reloadOverride string) (*proxyTarget, error) {
	switch kind {
	case "caddy":
		return detectCaddy(configPath, reloadOverride)
	case "nginx":
		return detectNginx(configPath, reloadOverride)
	case "", "auto":
		if p, err := detectCaddy(configPath, reloadOverride); err == nil {
			return p, nil
		}
		if p, err := detectNginx(configPath, reloadOverride); err == nil {
			return p, nil
		}
		return nil, fmt.Errorf("no caddy or nginx found on PATH")
	default:
		return nil, fmt.Errorf("unknown proxy_kind %q (auto|caddy|nginx)", kind)
	}
}

func detectCaddy(configPath, reloadOverride string) (*proxyTarget, error) {
	bin, err := exec.LookPath("caddy")
	if err != nil {
		return nil, fmt.Errorf("caddy not on PATH: %w", err)
	}
	main := configPath
	if main == "" {
		// Linux: /etc/caddy/Caddyfile. macOS Homebrew: <prefix>/etc/
		// Caddyfile (no caddy/ subdir) — /opt/homebrew on Apple
		// Silicon, /usr/local on Intel.
		main = findMainConfig("caddy", "--config",
			"/etc/caddy/Caddyfile",
			"/opt/homebrew/etc/Caddyfile",
			"/usr/local/etc/Caddyfile",
			"/usr/local/etc/caddy/Caddyfile")
	}
	if main == "" || !fileExists(main) {
		return nil, fmt.Errorf("caddy found but no Caddyfile located — set proxy_config_path")
	}
	includeDir := filepath.Join(filepath.Dir(main), "apteva.d")
	reload := []string{bin, "reload", "--config", main}
	if reloadOverride != "" {
		reload = strings.Fields(reloadOverride)
	}
	return &proxyTarget{
		kind:        "caddy",
		binary:      bin,
		mainConfig:  main,
		includeDir:  includeDir,
		includePath: filepath.Join(includeDir, "apteva-routes.caddy"),
		importLine:  fmt.Sprintf("import %s/*.caddy", includeDir),
		reloadCmd:   reload,
		renderer:    caddyRenderer{},
	}, nil
}

func detectNginx(configPath, reloadOverride string) (*proxyTarget, error) {
	bin, err := exec.LookPath("nginx")
	if err != nil {
		return nil, fmt.Errorf("nginx not on PATH: %w", err)
	}
	main := configPath
	if main == "" {
		main = findMainConfig("nginx", "-c",
			"/etc/nginx/nginx.conf",
			"/opt/homebrew/etc/nginx/nginx.conf",
			"/usr/local/etc/nginx/nginx.conf")
	}
	if main == "" || !fileExists(main) {
		return nil, fmt.Errorf("nginx found but no nginx.conf located — set proxy_config_path")
	}
	includeDir := filepath.Join(filepath.Dir(main), "apteva.d")
	reload := []string{bin, "-s", "reload"}
	if reloadOverride != "" {
		reload = strings.Fields(reloadOverride)
	}
	return &proxyTarget{
		kind:        "nginx",
		binary:      bin,
		mainConfig:  main,
		includeDir:  includeDir,
		includePath: filepath.Join(includeDir, "apteva-routes.conf"),
		importLine:  fmt.Sprintf("include %s/*.conf;", includeDir),
		reloadCmd:   reload,
		renderer:    nginxRenderer{},
	}, nil
}

// findMainConfig resolves a proxy's main config file: first the config
// flag on its systemd unit, then standard fallback locations.
func findMainConfig(unit, flag string, fallbacks ...string) string {
	if p := configFlagFromUnit(unit, flag); p != "" && fileExists(p) {
		return p
	}
	for _, c := range fallbacks {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

// configFlagFromUnit greps `systemctl cat <unit>` for an ExecStart
// flag value (`--config /path`, `--config=/path`, `-c /path`).
// Best-effort: empty when systemctl is unavailable or the flag absent.
func configFlagFromUnit(unit, flag string) string {
	out, err := exec.Command("systemctl", "cat", unit).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "ExecStart") {
			continue
		}
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == flag && i+1 < len(fields) {
				return fields[i+1]
			}
			if strings.HasPrefix(f, flag+"=") {
				return strings.TrimPrefix(f, flag+"=")
			}
		}
	}
	return ""
}

// ─── one-time bootstrap ────────────────────────────────────────────

// bootstrapProxy makes the proxy's main config aware of our include
// dir — exactly once, idempotently, with backup + validate + rollback.
func bootstrapProxy(p *proxyTarget) error {
	if err := os.MkdirAll(p.includeDir, 0o755); err != nil {
		return fmt.Errorf("create include dir: %w", err)
	}
	main, err := os.ReadFile(p.mainConfig)
	if err != nil {
		return fmt.Errorf("read main config: %w", err)
	}
	// Idempotent: already references our include dir via a real
	// directive → done forever. Checked line-by-line with comments
	// stripped, so a mention of the path in a comment doesn't count.
	if alreadyImported(string(main), p.includeDir) {
		return nil
	}
	// Back up before touching the operator's file.
	backup := fmt.Sprintf("%s.apteva-bak.%d", p.mainConfig, time.Now().Unix())
	if err := os.WriteFile(backup, main, 0o644); err != nil {
		return fmt.Errorf("back up main config: %w", err)
	}
	// Append exactly one import line — never modify existing content.
	updated := string(main)
	if !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	updated += "\n# added by the apteva routes app — imports app-managed route blocks\n" + p.importLine + "\n"
	if err := atomicWriteFile(p.mainConfig, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write main config: %w", err)
	}
	// Validate; restore the backup on failure so the proxy is never
	// left with a config it would reject.
	if err := validateProxyConfig(p); err != nil {
		_ = atomicWriteFile(p.mainConfig, main, 0o644)
		return fmt.Errorf("config invalid after adding import line — restored backup %s: %w", backup, err)
	}
	return nil
}

func validateProxyConfig(p *proxyTarget) error {
	var cmd *exec.Cmd
	switch p.kind {
	case "caddy":
		cmd = exec.Command(p.binary, "validate", "--config", p.mainConfig)
	case "nginx":
		cmd = exec.Command(p.binary, "-t", "-c", p.mainConfig)
	default:
		return nil
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func reloadProxy(p *proxyTarget) error {
	if len(p.reloadCmd) == 0 {
		return fmt.Errorf("no reload command")
	}
	cmd := exec.Command(p.reloadCmd[0], p.reloadCmd[1:]...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ─── rendering ─────────────────────────────────────────────────────

// proxyRenderer turns the routes table into one proxy include file.
// Pluggable per proxy_kind — a future traefik/HAProxy is one new
// implementation, not a rearchitecture.
type proxyRenderer interface {
	render(routes []*Route, certDir string) string
}

// certPaths returns the fullchain/key paths for a route's cert_fqdn
// under certDir, and whether both files currently exist.
func certPaths(r *Route, certDir string) (fullchain, key string, present bool) {
	if certDir == "" {
		return "", "", false
	}
	cf := r.CertFQDN
	if cf == "" {
		cf = r.Hostname
	}
	fullchain = filepath.Join(certDir, cf, "fullchain.pem")
	key = filepath.Join(certDir, cf, "privkey.pem")
	return fullchain, key, fileExists(fullchain) && fileExists(key)
}

type caddyRenderer struct{}

func (caddyRenderer) render(routes []*Route, certDir string) string {
	var b strings.Builder
	b.WriteString("# Managed by the apteva routes app — do not edit.\n")
	b.WriteString("# Regenerated from the routes table on every change.\n\n")
	for _, r := range routes {
		site := r.Hostname
		if r.AllowHTTP {
			// Serve both schemes without the automatic HTTPS redirect.
			site = "http://" + r.Hostname + ", " + r.Hostname
		}
		fmt.Fprintf(&b, "%s {\n", site)
		if full, key, ok := certPaths(r, certDir); ok {
			// Explicit cert from the certs app. Without this line Caddy
			// would fall back to its own automatic HTTPS.
			fmt.Fprintf(&b, "\ttls %s %s\n", full, key)
		}
		fmt.Fprintf(&b, "\treverse_proxy %s\n}\n\n", r.Target)
	}
	return b.String()
}

type nginxRenderer struct{}

func (nginxRenderer) render(routes []*Route, certDir string) string {
	var b strings.Builder
	b.WriteString("# Managed by the apteva routes app — do not edit.\n")
	b.WriteString("# Regenerated from the routes table on every change.\n\n")
	for _, r := range routes {
		full, key, haveCert := certPaths(r, certDir)
		if haveCert {
			fmt.Fprintf(&b, "server {\n\tlisten 443 ssl;\n\tserver_name %s;\n", r.Hostname)
			fmt.Fprintf(&b, "\tssl_certificate %s;\n\tssl_certificate_key %s;\n", full, key)
			b.WriteString(nginxLocation(r.Target))
			b.WriteString("}\n")
			if r.AllowHTTP {
				fmt.Fprintf(&b, "server {\n\tlisten 80;\n\tserver_name %s;\n", r.Hostname)
				b.WriteString(nginxLocation(r.Target))
				b.WriteString("}\n")
			} else {
				fmt.Fprintf(&b, "server {\n\tlisten 80;\n\tserver_name %s;\n\treturn 301 https://$host$request_uri;\n}\n", r.Hostname)
			}
		} else {
			// No cert on disk yet — HTTP-only until one lands; the
			// periodic resync upgrades it once the cert appears.
			fmt.Fprintf(&b, "server {\n\tlisten 80;\n\tserver_name %s;\n", r.Hostname)
			b.WriteString(nginxLocation(r.Target))
			b.WriteString("}\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func nginxLocation(target string) string {
	return "\tlocation / {\n" +
		"\t\tproxy_pass " + target + ";\n" +
		"\t\tproxy_set_header Host $host;\n" +
		"\t\tproxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n" +
		"\t\tproxy_set_header X-Forwarded-Proto $scheme;\n" +
		"\t}\n"
}

// ─── small helpers ─────────────────────────────────────────────────

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// alreadyImported reports whether the main config pulls in includeDir
// via a real directive — not just a mention in a comment. Both Caddy
// and nginx use '#' for comments, so stripping from the first '#' on
// each line is enough.
func alreadyImported(config, includeDir string) bool {
	for _, line := range strings.Split(config, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if strings.Contains(line, includeDir) {
			return true
		}
	}
	return false
}

// atomicWriteFile writes data to a sibling temp file then renames it
// over path — readers see either the old file or the new, never a
// partial write.
func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
