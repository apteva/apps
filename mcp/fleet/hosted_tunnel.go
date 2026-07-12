package main

import (
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type hostedTunnelKey struct {
	InstanceID int64
	TargetPort int
}

func platformContext(ctx *sdk.AppCtx) *sdk.AppCtx {
	if ctx != nil {
		return ctx
	}
	return globalCtx
}

func (a *App) openHostedTunnel(ctx *sdk.AppCtx, instanceID int64, targetPort int) (int, error) {
	ctx = platformContext(ctx)
	if ctx == nil {
		return 0, errors.New("platform unavailable")
	}
	if instanceID <= 0 {
		return 0, errors.New("hosted tunnel requires instance_id")
	}
	if err := validateTenantPort(targetPort); err != nil {
		return 0, err
	}
	var out struct {
		LocalHost string `json:"local_host"`
		LocalPort int    `json:"local_port"`
	}
	if err := callSiblingTool(ctx, "instances", "", "instance_open_tunnel", map[string]any{
		"id": instanceID, "target_port": targetPort,
	}, &out); err != nil {
		return 0, err
	}
	if out.LocalPort <= 0 || (out.LocalHost != "" && out.LocalHost != "127.0.0.1" && out.LocalHost != "localhost") {
		return 0, errors.New("instances returned an invalid tunnel endpoint")
	}
	key := hostedTunnelKey{InstanceID: instanceID, TargetPort: targetPort}
	a.hostedTunnelMu.Lock()
	if a.hostedTunnels[key] != out.LocalPort {
		a.dirtyTunnels[key] = true
	}
	a.hostedTunnels[key] = out.LocalPort
	a.hostedTunnelMu.Unlock()
	return out.LocalPort, nil
}

func (a *App) takeHostedTunnelChanged(instanceID int64, targetPort int) bool {
	key := hostedTunnelKey{InstanceID: instanceID, TargetPort: targetPort}
	a.hostedTunnelMu.Lock()
	defer a.hostedTunnelMu.Unlock()
	changed := a.dirtyTunnels[key]
	delete(a.dirtyTunnels, key)
	return changed
}

func (a *App) markHostedTunnelDirty(instanceID int64, targetPort int) {
	a.hostedTunnelMu.Lock()
	a.dirtyTunnels[hostedTunnelKey{InstanceID: instanceID, TargetPort: targetPort}] = true
	a.hostedTunnelMu.Unlock()
}

func (a *App) closeHostedTunnel(ctx *sdk.AppCtx, instanceID int64, targetPort int) {
	ctx = platformContext(ctx)
	if ctx == nil || instanceID <= 0 || targetPort <= 0 {
		return
	}
	_ = callSiblingTool(ctx, "instances", "", "instance_close_tunnel", map[string]any{
		"id": instanceID, "target_port": targetPort,
	}, nil)
}

func (a *App) hostedTunnelBaseURL(ctx *sdk.AppCtx, instanceID int64, targetPort int) (string, error) {
	localPort, err := a.openHostedTunnel(ctx, instanceID, targetPort)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://127.0.0.1:%d", localPort), nil
}

func (a *App) internalTenantBaseURL(ctx *sdk.AppCtx, t *Tenant) (string, error) {
	if t == nil {
		return "", errors.New("tenant required")
	}
	if t.Kind != KindLocal {
		return strings.TrimRight(t.BaseURL, "/"), nil
	}
	port, err := portFromBaseURL(t.BaseURL)
	if err != nil || port == 0 {
		return "", fmt.Errorf("managed tenant has invalid base_url %q", t.BaseURL)
	}
	if !t.IsHosted() {
		return fmt.Sprintf("http://127.0.0.1:%d", port), nil
	}
	return a.hostedTunnelBaseURL(ctx, t.InstanceID, port)
}
