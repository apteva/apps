package main

// Optional public exposure for dev runs via server-native ingress.
//
// When repos_dev_start is called with expose=true, we publish the
// running dev process at an installation/project/repository-specific host. Apteva-server's
// own host router proxies public requests and manages exact-host
// certificates when DNS points at the server. Wildcard DNS/certs are
// still an operator concern until DNS-01 delegation lands.

import (
	"crypto/sha256"
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
	hostname := ingressHostname(ctx, slug, base)
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

func ingressHostname(ctx *sdk.AppCtx, identity, base string) string {
	sum := sha256.Sum256([]byte(ctx.DataDir() + "\x00" + identity))
	name := slugify(identity)
	if len(name) > 40 {
		name = name[:40]
	}
	return fmt.Sprintf("%s-%x.%s", name, sum[:6], strings.ToLower(strings.TrimPrefix(base, ".")))
}
func (s *devSupervisor) cleanupIngress(ctx *sdk.AppCtx, dr *DevRun) error {
	if ctx == nil {
		return nil
	}
	current, err := dbGetDevRun(ctx.AppDB(), dr.ProjectID, dr.RepoID)
	if err != nil {
		return err
	}
	if current == nil {
		return nil
	}
	if current.IngressHostname != "" {
		if ctx.PlatformAPI() == nil {
			return errors.New("platform unavailable; ingress ownership retained")
		}
		if err := ctx.PlatformAPI().UnexposeIngress(current.IngressHostname); err != nil {
			return err
		}
		return dbUpdateDevRun(ctx.AppDB(), dr.ID, map[string]any{"ingress_hostname": ""})
	}
	return nil
}
func localExecutionEnabled(ctx *sdk.AppCtx) bool {
	return ctx != nil && ctx.Config() != nil && strings.EqualFold(ctx.Config().Get("trusted_local_execution"), "true")
}

func (s *devSupervisor) expose(ctx *sdk.AppCtx, repo *Repo, dr *DevRun) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.all[dr.ID]
	if p == nil || p.stopping || p.logFile == nil || p.logFile.Name() != dr.LogPath {
		return "", errors.New("dev run changed before exposure; retry the current run")
	}
	hostname, err := exposeDevRun(ctx, repoStoreKey(repo), dr.Port)
	if err != nil {
		return "", err
	}
	if err := dbUpdateDevRun(ctx.AppDB(), dr.ID, map[string]any{"ingress_hostname": hostname}); err != nil {
		_ = ctx.PlatformAPI().UnexposeIngress(hostname)
		return "", err
	}
	return hostname, nil
}
