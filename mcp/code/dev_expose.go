package main

// Optional public exposure for dev runs via server-native ingress.
//
// When repos_dev_start is called with expose=true, we publish the
// running dev process at <slug>.<dev_base_hostname>. Apteva-server's
// own host router proxies public requests and manages exact-host
// certificates when DNS points at the server. Wildcard DNS/certs are
// still an operator concern until DNS-01 delegation lands.

import (
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func exposeDevRun(ctx *sdk.AppCtx, slug string, port int) (string, error) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return "", errors.New("platform unavailable")
	}
	base := strings.TrimSpace(ctx.Config().Get("dev_base_hostname"))
	if base == "" {
		return "", errors.New("dev_base_hostname not configured — set it in this install's Code app config (e.g. dev.example.com)")
	}
	hostname := strings.ToLower(slug + "." + strings.TrimPrefix(base, "."))
	target := fmt.Sprintf("http://127.0.0.1:%d", port)
	if _, err := ctx.PlatformAPI().ExposeIngress(sdk.IngressExposeRequest{
		Hostname:  hostname,
		Target:    target,
		OwnerKind: "code",
		CertFQDN:  hostname,
	}); err != nil {
		return "", err
	}
	return hostname, nil
}

func unexposeDevRun(ctx *sdk.AppCtx, slug string) error {
	base := strings.TrimSpace(ctx.Config().Get("dev_base_hostname"))
	if base == "" {
		return nil
	}
	hostname := strings.ToLower(slug + "." + strings.TrimPrefix(base, "."))
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil
	}
	return ctx.PlatformAPI().UnexposeIngress(hostname)
}
