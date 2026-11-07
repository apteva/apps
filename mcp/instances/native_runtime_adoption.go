package main

// Host proxy adapter only. Server owns approved routes, deployment selection,
// readiness/admission, durable adoption operations, publication and rollback policy.
import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const proxyContract = "apteva.ingress-provider/v1"
const proxyPrefix = "/_apteva/ingress/providers/caddy"

type nativeProxySpec struct {
	Adapter  string `json:"adapter"`
	Hostname string `json:"hostname"`
	Service  string `json:"service"`
	Binary   string `json:"binary"`
	Config   string `json:"config"`
	SiteFile string `json:"site_file"`
	Format   string `json:"format"`
	Admin    string `json:"admin"`
}
type nativeProxyRequest struct {
	Proxy             nativeProxySpec `json:"proxy"`
	PlanSHA256        string          `json:"plan_sha256,omitempty"`
	MaintenanceWindow bool            `json:"maintenance_window,omitempty"`
}
type proxyIntent struct {
	ExpectedDeploymentID string                    `json:"expected_deployment_id,omitempty"`
	ExpectedGeneration   int64                     `json:"expected_generation,omitempty"`
	Contract             string                    `json:"contract"`
	ControllerID         string                    `json:"controller_id"`
	OperationID          string                    `json:"operation_id"`
	InstallID            int64                     `json:"install_id"`
	DeploymentID         string                    `json:"deployment_id"`
	Generation           int64                     `json:"generation"`
	ProjectID            string                    `json:"project_id,omitempty"`
	Target               nativeTarget              `json:"target"`
	Hostname             string                    `json:"hostname"`
	HostKey              string                    `json:"host_key,omitempty"`
	Upstream             string                    `json:"upstream"`
	Routes               []sdk.RuntimeIngressRoute `json:"routes"`
	Options              nativeProxySpec           `json:"options"`
	PlanSHA256           string                    `json:"plan_sha256,omitempty"`
	MaintenanceWindow    bool                      `json:"maintenance_window,omitempty"`
}

//go:embed runtime/proxy_adoption.py
var nativeProxyProgram string

func validProxyPath(s string) bool {
	return len(s) > 1 && len(s) < 512 && path.IsAbs(s) && path.Clean(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}
func validateProxySpec(s nativeProxySpec) error {
	if s.Adapter != "caddy" || s.Format != "json" && s.Format != "caddyfile" {
		return errors.New("unsupported proxy adapter/configuration format")
	}
	if !regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])$`).MatchString(s.Hostname) || len(s.Hostname) > 253 || !strings.Contains(s.Hostname, ".") || strings.Contains(s.Hostname, "..") {
		return errors.New("invalid proxy hostname")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]+\.service$`).MatchString(s.Service) || len(s.Service) > 128 {
		return errors.New("explicit systemd proxy service required")
	}
	if !validProxyPath(s.Binary) || !validProxyPath(s.Config) || !validProxyPath(s.SiteFile) {
		return errors.New("absolute proxy binary/config/site_file paths required")
	}
	if s.Format == "json" && s.Config != s.SiteFile {
		return errors.New("JSON adoption requires config=site_file")
	}
	if strings.HasPrefix(s.Admin, "unix/") {
		if !validProxyPath(strings.TrimPrefix(s.Admin, "unix/")) {
			return errors.New("invalid admin socket")
		}
	} else if s.Admin != "127.0.0.1:2019" && s.Admin != "localhost:2019" {
		return errors.New("admin must use a local Unix socket or loopback port 2019")
	}
	return nil
}
func validateProxyIntent(r proxyIntent, action string) error {
	if r.Contract != proxyContract || !nativeID(r.ControllerID) || !nativeID(r.DeploymentID) || r.InstallID <= 0 || r.Generation <= 0 || r.OperationID == "" || len(r.OperationID) > 256 {
		return errors.New("invalid ingress provider contract/owner")
	}
	if action != "prepare" && action != "inspect-target" && (r.Options.Hostname != r.Hostname || validateProxySpec(r.Options) != nil) {
		return errors.New("invalid explicit proxy specification")
	}
	if e := sdk.ValidateRuntimeIngress(&sdk.RuntimeIngress{Routes: r.Routes}); e != nil {
		return e
	}
	if action == "inspect-target" {
		return validateNativeIngress(&nativeIngress{Hostname: r.Hostname}, sdk.Manifest{Runtime: sdk.Runtime{Ingress: &sdk.RuntimeIngress{Routes: r.Routes}}})
	}
	if action == "prepare" {
		if r.Options.Service != "" || r.Options.Config != "" || r.Options.SiteFile != "" || r.Options.Binary != "" || r.Options.Admin != "" {
			return errors.New("prepare creates only a new provider-owned proxy; use inspect/adopt for an existing proxy")
		}
		return validateNativeIngress(&nativeIngress{Hostname: r.Hostname}, sdk.Manifest{Runtime: sdk.Runtime{Ingress: &sdk.RuntimeIngress{Routes: r.Routes}}})
	}
	u, e := url.Parse(r.Upstream)
	if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return errors.New("Caddy host adapter requires an explicit HTTP loopback upstream")
	}
	port, e := strconv.Atoi(u.Port())
	if e != nil || port < 1024 || port > 65535 {
		return errors.New("invalid loopback upstream port")
	}
	if action != "inspect" && r.HostKey == "" {
		return errors.New("observed provider host_key required")
	}
	if (action == "adopt" || action == "rollback") && (!nativeDigest(r.PlanSHA256) || !r.MaintenanceWindow) {
		return errors.New("reviewed plan and quiesced maintenance window required")
	}
	return nil
}
func proxySite(d nativeRecord) string {
	cfg := nativeIngressConfig(d, true)
	old := fmt.Sprintf("reverse_proxy 127.0.0.1:%d\n", d.ControlPort)
	return strings.ReplaceAll(cfg, old, fmt.Sprintf("reverse_proxy 127.0.0.1:%d {\n stream_close_delay 24h\n }\n", d.ControlPort))
}
func proxyPayload(r proxyIntent, key string, providerInstall int64, action string) map[string]any {
	u, _ := url.Parse(r.Upstream)
	_, port, _ := net.SplitHostPort(u.Host)
	n, _ := strconv.Atoi(port)
	root := "/var/lib/apteva-ingress/" + nativeHash(r.ControllerID+":"+strconv.FormatInt(providerInstall, 10))
	d := nativeRecord{ID: r.DeploymentID, InstallID: r.InstallID, Generation: r.Generation, ControlPort: n, PublicRoutes: r.Routes, Options: nativeOptions{Ingress: &nativeIngress{Hostname: r.Hostname}}}
	return map[string]any{"expected_deployment_id": r.ExpectedDeploymentID, "expected_generation": r.ExpectedGeneration, "action": action, "request": nativeProxyRequest{Proxy: r.Options, PlanSHA256: r.PlanSHA256, MaintenanceWindow: r.MaintenanceWindow}, "root": root, "install_id": r.InstallID, "deployment_id": r.DeploymentID, "generation": r.Generation, "host_key": key, "control_port": n, "routes": r.Routes, "site": proxySite(d)}
}

var proxyRun = func(c context.Context, i *Instance, r proxyIntent, providerInstall int64, action string) (map[string]any, error) {
	// Physical identity is obtained through the registered SSH transport. Inspect is
	// read-only; every mutation checks the same machine/SSH-host-key identity again.
	probe := `set -eu; test "$(uname -s)" = Linux; test "$(id -u)" = 0; test -d /run/systemd/system
 for tool in python3 systemctl ss sha256sum;do command -v "$tool" >/dev/null;done
 IFS= read -r machine < /etc/machine-id
 if test -f /etc/ssh/ssh_host_ed25519_key.pub; then IFS= read -r hostpub < /etc/ssh/ssh_host_ed25519_key.pub; else IFS= read -r hostpub < /etc/ssh/ssh_host_rsa_key.pub; fi
 identity=$(printf '%s\n%s' "$machine" "$hostpub" | sha256sum); identity=${identity%% *}; printf 'linux-systemd:%s' "$identity"
`
	key, e := nativeRun(c, i, probe)
	key = strings.TrimSpace(key)
	if e != nil || !strings.HasPrefix(key, "linux-systemd:") || !nativeDigest(strings.TrimPrefix(key, "linux-systemd:")) {
		return nil, errors.New("ingress target identity unavailable")
	}
	if r.HostKey != "" && r.HostKey != key {
		return nil, errors.New("ingress target identity changed")
	}
	if action == "inspect-target" {
		return map[string]any{"contract": proxyContract, "host_key": key, "public_ipv4": i.PublicIPv4}, nil
	}
	if action == "prepare" {
		root := "/var/lib/apteva-ingress/" + nativeHash(r.ControllerID+":"+strconv.FormatInt(providerInstall, 10))
		r.Options = nativeProxySpec{Adapter: "caddy", Hostname: r.Hostname, Service: "apteva-ingress-" + nativeHash(root)[:16] + ".service", Binary: root + "/caddy", Config: root + "/Caddyfile", SiteFile: root + "/hosts/" + r.Hostname + ".caddy", Format: "caddyfile", Admin: "unix/" + root + "/admin.sock"}
		exists, e := nativeRun(c, i, "if test -f "+quoteShellArg(r.Options.SiteFile+".apteva-owner.json")+";then printf existing;else printf fresh;fi")
		if e != nil {
			return nil, e
		}
		if exists != "existing" {
			if _, e = nativeRun(c, i, `for tool in curl tar sha512sum flock;do command -v "$tool" >/dev/null;done`); e != nil {
				return nil, errors.New("proxy_preparation_prerequisites_unavailable")
			}
			arch, e := nativeRun(c, i, "uname -m")
			if e != nil {
				return nil, e
			}
			platform := "linux-amd64"
			if strings.TrimSpace(arch) == "aarch64" {
				platform = "linux-arm64"
			} else if strings.TrimSpace(arch) != "x86_64" {
				return nil, errors.New("unsupported proxy host architecture")
			}
			if _, e = nativeRun(c, i, `test -z "$(ss -Hnt state established '( sport = :80 or sport = :443 )')"`); e != nil {
				return nil, errors.New("active proxy connections require an idle window before preparation")
			}
			d := nativeRecord{ID: r.DeploymentID, InstallID: r.InstallID, ReleaseDir: root + "/inst-" + strconv.FormatInt(r.InstallID, 10) + "/proxy", Artifact: nativeArtifact{Platform: platform}, Options: nativeOptions{Ingress: &nativeIngress{Hostname: r.Hostname}}}
			if e = (nativeSSH{}).PrepareCaddy(c, i, d); e != nil {
				return nil, errors.New("proxy preparation unconfirmed; occupied ports require explicit adoption")
			}
		}
	}
	payload := proxyPayload(r, key, providerInstall, action)
	encoded := base64.StdEncoding.EncodeToString([]byte(nativeJSON(payload)))
	cmd := nativeHostIdentity(nativeRecord{Execution: nativeExecution{HostKey: key}}) + "\npython3 - " + quoteShellArg(encoded) + " <<'APTEVA_PROXY_ADAPTER'\n" + nativeProxyProgram + "\nAPTEVA_PROXY_ADAPTER"
	raw, e := nativeRun(c, i, cmd)
	if e != nil {
		return nil, errors.New("proxy outcome unknown; inspect status before retry")
	}
	var out map[string]any
	if json.Unmarshal([]byte(raw), &out) != nil {
		return nil, errors.New("invalid proxy receipt; inspect status")
	}
	if message, ok := out["error"].(string); ok {
		return nil, fmt.Errorf("proxy adapter: %s", message)
	}
	out["options"] = r.Options
	out["contract"] = proxyContract
	out["host_key"] = key
	return out, nil
}

func (a *App) handleProxyAdapter(w http.ResponseWriter, r *http.Request) {
	ctx := appCtxForRequest(r)
	if ctx == nil {
		http.Error(w, "Instances unavailable", 503)
		return
	}
	action := strings.TrimPrefix(r.URL.Path, proxyPrefix+"/")
	if action == "describe" && r.Method == http.MethodGet {
		writeNative(w, map[string]any{"contract": proxyContract, "id": "caddy", "adapters": []string{"caddy"}, "formats": []string{"json", "caddyfile"}, "upstreams": []string{"http-loopback"}, "adoption": true, "rollback": true, "maintenance_window_required": true, "idle_connections_required": true, "runtime_backend_required": false, "prepare": true, "requires_host_identity": true})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	switch action {
	case "prepare", "inspect-target", "inspect", "adopt", "status", "rollback", "publish", "unpublish", "release":
	default:
		http.NotFound(w, r)
		return
	}
	var intent proxyIntent
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if dec.Decode(&intent) != nil || dec.Decode(new(any)) != io.EOF {
		http.Error(w, "one bounded ingress request required", 400)
		return
	}
	if e := validateProxyIntent(intent, action); e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	providerID, e := strconv.ParseInt(os.Getenv("APTEVA_INSTALL_ID"), 10, 64)
	if e != nil || providerID <= 0 {
		http.Error(w, "provider identity unavailable", 503)
		return
	}
	inst, e := resolveProxyTarget(ctx.AppDB(), providerID, os.Getenv("APTEVA_PROJECT_ID"), intent)
	if e != nil {
		http.Error(w, "registered target/project ownership unavailable", 409)
		return
	}
	work, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out, e := proxyRun(work, inst, intent, providerID, action)
	if e != nil {
		code := strings.TrimPrefix(e.Error(), "proxy adapter: ")
		if !regexp.MustCompile(`^[a-z_]{1,128}$`).MatchString(code) {
			code = "proxy_outcome_unconfirmed"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(409)
		json.NewEncoder(w).Encode(map[string]string{"code": code})
		return
	}
	writeNative(w, out)
}

// Resolve only host inventory. Ingress does not initialize a native execution
// controller, allocate native listeners or create deployment/artifact records.
func resolveProxyTarget(db *sql.DB, providerID int64, project string, r proxyIntent) (*Instance, error) {
	if len(r.ProjectID) > 256 || strings.ContainsAny(r.ProjectID, "\x00\r\n") || project != "" && project != r.ProjectID || r.Target.InstallID != providerID || r.Target.Type != "instances.instance" || r.InstallID == providerID {
		return nil, errors.New("provider ownership mismatch")
	}
	id, e := strconv.ParseInt(r.Target.ID, 10, 64)
	if e != nil || id <= 0 {
		return nil, errors.New("registered remote target required")
	}
	inst, e := dbGetInstance(db, id)
	if e != nil || inst.Status != "ready" || inst.IsLocal() || inst.Platform != "" && inst.Platform != "linux" {
		return nil, errors.New("remote Linux target unavailable")
	}
	return inst, nil
}
