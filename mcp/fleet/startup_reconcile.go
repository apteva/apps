package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var transientDependencyStatus = regexp.MustCompile(`(?i)(?:http|status(?: code)?|returned|upstream)[^0-9]{0,16}(502|503)\b|\b(502|503)\s+(?:bad gateway|service unavailable)\b`)

var defaultStartupRetryDelays = []time.Duration{
	250 * time.Millisecond,
	750 * time.Millisecond,
	2 * time.Second,
}

func (a *App) hostedStartupRetryDelays() []time.Duration {
	if a != nil && a.startupRetryDelays != nil {
		return a.startupRetryDelays
	}
	return defaultStartupRetryDelays
}

func isTransientHostedDependencyError(err error) bool {
	return err != nil && transientDependencyStatus.MatchString(err.Error())
}

func (a *App) retryHostedStartup(ctx context.Context, app *sdk.AppCtx, operation string, fn func() error) error {
	delays := a.hostedStartupRetryDelays()
	var lastErr error
	for attempt := 0; attempt <= len(delays); attempt++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if !isTransientHostedDependencyError(lastErr) || attempt == len(delays) {
			break
		}
		if app != nil {
			app.Logger().Warn("fleet: transient hosted startup dependency failure",
				"operation", operation, "attempt", attempt+1, "err", lastErr)
		}
		delay := delays[attempt]
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%s: %w", operation, ctx.Err())
		case <-timer.C:
		}
	}
	return fmt.Errorf("%s: %w", operation, lastErr)
}

func hostedTenantExpectedRunning(status string) bool {
	switch status {
	case StatusActive, StatusStarting, StatusSetupPending, StatusDisconnected:
		return true
	default:
		return false
	}
}

func (a *App) reconcileHostedOnBoot(ctx context.Context, app *sdk.AppCtx, t *Tenant, port int) error {
	// Intentionally offline tenants must not depend on the remote instance
	// still existing. In particular, a stopped clone may outlive a disposable
	// rehearsal VPS; looking up that missing instance would otherwise abort
	// Fleet's entire OnMount and leave every tenant unmanaged.
	if !hostedTenantExpectedRunning(t.Status) {
		return nil
	}

	var alive bool
	if err := a.retryHostedStartup(ctx, app, "verify hosted tenant port", func() error {
		var err error
		alive, err = hostedPortListening(app, t.InstanceID, port)
		return err
	}); err != nil {
		return err
	}
	if !alive {
		a.tryRespawnHosted(ctx, app, t)
		if err := a.retryHostedStartup(ctx, app, "verify respawned hosted tenant port", func() error {
			var err error
			alive, err = hostedPortListening(app, t.InstanceID, port)
			return err
		}); err != nil {
			return err
		}
		if !alive {
			return errors.New("hosted tenant port is not listening after startup recovery")
		}
	}

	if t.IngressMode != IngressDirect {
		if err := a.reconcileHostedIngressOnBoot(ctx, app, t, port); err != nil {
			return err
		}
	}
	if t.Status == StatusDisconnected {
		if err := a.store.setStatus(t.ID, StatusActive, "worker:reconcile"); err != nil {
			return fmt.Errorf("mark hosted tenant active: %w", err)
		}
	}
	if err := a.store.resetRespawn(t.ID); err != nil {
		return fmt.Errorf("reset hosted respawn counter: %w", err)
	}
	return nil
}

func (a *App) reconcileHostedIngressOnBoot(ctx context.Context, app *sdk.AppCtx, t *Tenant, targetPort int) error {
	if app == nil || app.PlatformAPI() == nil {
		return errors.New("platform unavailable for hosted ingress reconciliation")
	}
	hostnames, err := a.tenantIngressHostnames(t)
	if err != nil {
		return fmt.Errorf("list hosted ingress hostnames: %w", err)
	}

	var existing []sdk.IngressRoute
	if err := a.retryHostedStartup(ctx, app, "list hosted ingress routes", func() error {
		var listErr error
		existing, listErr = app.PlatformAPI().ListIngressRoutes()
		return listErr
	}); err != nil {
		return err
	}
	oldPorts := ingressTargetPorts(existing, hostnames)

	var localPort int
	if err := a.retryHostedStartup(ctx, app, "recreate hosted tunnel", func() error {
		var tunnelErr error
		localPort, tunnelErr = a.openHostedTunnel(app, t.InstanceID, targetPort)
		return tunnelErr
	}); err != nil {
		return err
	}
	target := fmt.Sprintf("http://127.0.0.1:%d", localPort)

	var refresh ingressRefreshResult
	if err := a.retryHostedStartup(ctx, app, "replace hosted ingress routes", func() error {
		var refreshErr error
		refresh, refreshErr = a.refreshParentTenantIngressTargetsCount(app, t, target)
		return refreshErr
	}); err != nil {
		a.markHostedTunnelDirty(t.InstanceID, targetPort)
		return err
	}
	if refresh.Expected != len(hostnames) || refresh.Rewritten != refresh.Expected {
		a.markHostedTunnelDirty(t.InstanceID, targetPort)
		return fmt.Errorf("hosted ingress replacement incomplete: rewritten=%d expected=%d",
			refresh.Rewritten, refresh.Expected)
	}
	_ = a.takeHostedTunnelChanged(t.InstanceID, targetPort)

	payload := map[string]any{
		"instance_id":      t.InstanceID,
		"target_port":      targetPort,
		"old_tunnel_port":  firstPort(oldPorts),
		"old_tunnel_ports": oldPorts,
		"new_tunnel_port":  localPort,
		"routes_rewritten": refresh.Rewritten,
		"routes_expected":  refresh.Expected,
	}
	if err := a.store.recordEvent(t.ID, "hosted_ingress_reconciled", "worker:reconcile", payload); err != nil {
		return fmt.Errorf("record hosted ingress reconciliation: %w", err)
	}
	return nil
}

func (a *App) tenantIngressHostnames(t *Tenant) ([]string, error) {
	hosts, err := a.store.listTenantHosts(t.ID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(hosts)+1)
	add := func(hostname string) {
		hostname = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(hostname, ".")))
		if hostname == "" || seen[hostname] {
			return
		}
		seen[hostname] = true
		out = append(out, hostname)
	}
	add(t.Domain)
	for _, host := range hosts {
		add(host.Hostname)
	}
	sort.Strings(out)
	return out, nil
}

func ingressTargetPorts(routes []sdk.IngressRoute, hostnames []string) []int {
	wanted := make(map[string]bool, len(hostnames))
	for _, hostname := range hostnames {
		wanted[hostname] = true
	}
	seen := map[int]bool{}
	var ports []int
	for _, route := range routes {
		if !wanted[strings.ToLower(strings.TrimSpace(route.Hostname))] {
			continue
		}
		u, err := url.Parse(route.Target)
		if err != nil {
			continue
		}
		host, rawPort, err := net.SplitHostPort(u.Host)
		if err != nil || (host != "127.0.0.1" && host != "localhost") {
			continue
		}
		port, err := strconv.Atoi(rawPort)
		if err == nil && port > 0 && !seen[port] {
			seen[port] = true
			ports = append(ports, port)
		}
	}
	sort.Ints(ports)
	return ports
}

func firstPort(ports []int) int {
	if len(ports) == 0 {
		return 0
	}
	return ports[0]
}
