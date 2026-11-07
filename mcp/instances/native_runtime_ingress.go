package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type nativeIngress struct {
	Hostname string `json:"hostname"`
}

func validateNativeIngress(v *nativeIngress, manifest sdk.Manifest) error {
	if v == nil {
		return nil
	}
	if !regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])$`).MatchString(v.Hostname) || len(v.Hostname) > 253 || !strings.Contains(v.Hostname, ".") || strings.Contains(v.Hostname, "..") {
		return errors.New("invalid ingress hostname")
	}
	if manifest.Runtime.Ingress == nil {
		return errors.New("app release declares no public ingress surfaces")
	}
	return sdk.ValidateRuntimeIngress(manifest.Runtime.Ingress)
}
func nativeIngressEndpoints(d *nativeRecord, r nativeRequest) {
	if d.Options.Ingress == nil {
		return
	}
	base := "https://" + d.Options.Ingress.Hostname
	d.Execution.Endpoints = map[string]string{}
	for key, path := range r.Manifest.Runtime.Ingress.Endpoints {
		d.Execution.Endpoints[key] = base + path
	}
	if r.Manifest.Runtime.Ingress.BrowserPath != "" {
		d.Execution.BrowserEndpoint = base + r.Manifest.Runtime.Ingress.BrowserPath
	}
	d.PublicRoutes = r.Manifest.Runtime.Ingress.Routes
}
func nativeIngressRoot(d nativeRecord) string {
	return strings.Split(d.ReleaseDir, "/inst-")[0] + "/ingress"
}
func nativeIngressConfig(d nativeRecord, publish bool) string {
	cfg := d.Options.Ingress.Hostname + " {\n"
	if publish {
		cfg += " handle /.well-known/apteva-runtime-ready {\n respond " + strconv.Quote(d.ID) + " 200\n }\n"
		for i, r := range d.PublicRoutes {
			cfg += fmt.Sprintf(" @surface%d path %s\n handle @surface%d {\n reverse_proxy 127.0.0.1:%d\n }\n", i, r.Path, i, d.ControlPort)
		}
	}
	return cfg + " handle {\n respond \"Not found\" 404\n }\n}\n"
}

// One backend-owned proxy per target; every hostname has a durable installation
// owner and a current deployment marker. No unrelated config/service is edited.
func (nativeSSH) PrepareCaddy(c context.Context, i *Instance, d nativeRecord) error {
	root := nativeIngressRoot(d)
	host := d.Options.Ingress.Hostname
	owner := strconv.FormatInt(d.InstallID, 10)
	unit := "apteva-ingress-" + nativeHash(root)[:16] + ".service"
	config := nativeIngressConfig(d, false)
	cmd := `set -eu; umask 077
 root=` + quoteShellArg(root) + `; host=` + quoteShellArg(host) + `; owner=` + quoteShellArg(owner) + `; unit=` + quoteShellArg(unit) + `
 for p in "$(dirname "$root")" "$root" "$root/hosts" "$root/data" "$root/config" "$root/caddy" "$root/Caddyfile" "$root/.controller-owner" "$root/$host.owner" "$root/hosts/$host.caddy";do test ! -L "$p";done
 if test -d "$root";then
  if test -f "$root/.controller-owner";then test "$(cat "$root/.controller-owner")" = "$root";else test -z "$(find "$root" -mindepth 1 -maxdepth 1 -print -quit)";fi
 fi
 test ! -e "$root/hosts/$host.caddy.apteva-owner.json"
 mkdir -p "$root/hosts" "$root/data" "$root/config"
 printf '%s' "$root" > "$root/.controller-owner"
 exec 8>"$root/.lock"; flock -x 8
 if test -f "$root/$host.owner"; then test "$(cat "$root/$host.owner")" = "$owner"; else printf '%s' "$owner" > "$root/$host.owner"; fi
 if ! test -x "$root/caddy"; then
  curl -fsSL --connect-timeout 5 --max-time 45 -o "$root/caddy.tgz.tmp" https://github.com/caddyserver/caddy/releases/download/v2.11.7/caddy_2.11.7_linux_` + strings.TrimPrefix(d.Artifact.Platform, "linux-") + `.tar.gz
  curl -fsSL --connect-timeout 5 --max-time 15 -o "$root/checksums.tmp" https://github.com/caddyserver/caddy/releases/download/v2.11.7/caddy_2.11.7_checksums.txt
  digest=$(awk '$2=="caddy_2.11.7_linux_` + strings.TrimPrefix(d.Artifact.Platform, "linux-") + `.tar.gz" {print $1}' "$root/checksums.tmp"); test ${#digest} = 128
  printf '%s  %s\n' "$digest" "$root/caddy.tgz.tmp" | sha512sum -c - >/dev/null
  tar -xzf "$root/caddy.tgz.tmp" -C "$root" caddy; chmod 755 "$root/caddy"
 fi
 if ! test -f "$root/Caddyfile"; then
  test -z "$(ss -H -lnt 'sport = :80 or sport = :443')"
  printf '{\n admin unix/%s/admin.sock\n}\nimport %s/hosts/*.caddy\n' "$root" "$root" > "$root/Caddyfile"
 fi
 if test ! -f "$root/hosts/$host.caddy"; then
  printf '%s' ` + quoteShellArg(config) + ` > "$root/hosts/$host.caddy.tmp"
  printf '{\n admin unix/%s/admin.sock\n}\n' "$root" > "$root/candidate.conf"
  for existing in "$root"/hosts/*.caddy; do
   test -f "$existing" || continue
   test "$existing" = "$root/hosts/$host.caddy" && continue
   cat "$existing" >> "$root/candidate.conf"
  done
  cat "$root/hosts/$host.caddy.tmp" >> "$root/candidate.conf"
  "$root/caddy" validate --config "$root/candidate.conf" --adapter caddyfile >/dev/null 2>&1
  mv "$root/hosts/$host.caddy.tmp" "$root/hosts/$host.caddy"
  printf '%s' ` + quoteShellArg(d.ID) + ` > "$root/$host.deployment"
 fi
 "$root/caddy" validate --config "$root/Caddyfile" --adapter caddyfile >/dev/null 2>&1
 unitfile=/etc/systemd/system/"$unit"
 if test -f "$unitfile"; then test "$(head -n1 "$unitfile")" = "# apteva-ingress:$root"; else
  printf '# apteva-ingress:%s\n[Unit]\nAfter=network-online.target\n[Service]\nEnvironment=XDG_DATA_HOME=%s/data\nEnvironment=XDG_CONFIG_HOME=%s/config\nExecStart=%s/caddy run --config %s/Caddyfile --adapter caddyfile\nRestart=on-failure\nUMask=0077\nNoNewPrivileges=true\nPrivateTmp=true\nProtectHome=true\nProtectSystem=strict\nReadWritePaths=%s\n[Install]\nWantedBy=multi-user.target\n' "$root" "$root" "$root" "$root" "$root" "$root" > "$unitfile"
 fi
 systemctl daemon-reload
 if systemctl is-active --quiet "$unit"; then "$root/caddy" reload --address "unix/$root/admin.sock" --config "$root/Caddyfile" --adapter caddyfile >/dev/null 2>&1; else systemctl enable --now "$unit" >/dev/null 2>&1; fi
`
	_, e := nativeRun(c, i, cmd)
	return e
}
