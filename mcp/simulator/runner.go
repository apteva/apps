package main

import (
	"context"
	"errors"
	"net/url"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func platformCapability(caps Capabilities, platform string) (PlatformCapability, error) {
	switch platform {
	case "android":
		return caps.Android, nil
	case "ios":
		return caps.IOS, nil
	default:
		return PlatformCapability{}, errors.New("unknown platform: " + platform)
	}
}

func validateCapability(caps Capabilities, platform string, needBuild, needStream bool) error {
	pc, err := platformCapability(caps, platform)
	if err != nil {
		return err
	}
	if !pc.Available {
		return errors.New("host_unsupported: " + strings.Join(pc.Reasons, "; "))
	}
	if needBuild && !pc.BuildAvailable {
		return errors.New("build_unsupported: " + strings.Join(pc.BuildReasons, "; "))
	}
	if needStream && !pc.StreamingAvailable {
		return errors.New("streaming_unsupported: " + strings.Join(pc.StreamingReasons, "; "))
	}
	return nil
}

func unavailableRemoteCapability(err error) PlatformCapability {
	message := "runner_unavailable"
	if err != nil {
		message = err.Error()
	}
	return PlatformCapability{
		Available: false, Reasons: []string{message},
		BuildAvailable: false, BuildReasons: []string{message},
		StreamingAvailable: false, StreamingReasons: []string{message},
		Tools: map[string]ToolProbe{},
	}
}

func (a *App) configuredCapabilities(ctx *sdk.AppCtx) Capabilities {
	androidHost := configuredHostID(ctx, "android")
	iosHost := configuredHostID(ctx, "ios")
	out := probeCapabilities(ctx)
	if androidHost > 0 {
		caps, err := a.capabilitiesForHost(ctx, androidHost)
		if err != nil {
			out.Android = unavailableRemoteCapability(err)
		} else {
			out.Android = caps.Android
		}
	}
	if iosHost > 0 {
		caps, err := a.capabilitiesForHost(ctx, iosHost)
		if err != nil {
			out.IOS = unavailableRemoteCapability(err)
		} else {
			out.IOS = caps.IOS
		}
	}
	out.Hosts = map[string]int64{"android": androidHost, "ios": iosHost}
	return out
}

func (a *App) capabilitiesForHost(ctx *sdk.AppCtx, hostID int64) (Capabilities, error) {
	if hostID < 0 {
		return Capabilities{}, errors.New("host_id must be 0 (local) or a positive Instances id")
	}
	if hostID == 0 {
		return probeCapabilities(ctx), nil
	}
	client, err := a.ensureRemoteWorker(ctx, hostID)
	if err != nil {
		return Capabilities{}, err
	}
	return client.capabilities(context.Background())
}

func (a *App) capabilityCheckForHost(ctx *sdk.AppCtx, platform string, hostID int64, needBuild, needStream bool) error {
	caps, err := a.capabilitiesForHost(ctx, hostID)
	if err != nil {
		return err
	}
	return validateCapability(caps, platform, needBuild, needStream)
}

func (a *App) bootOnHost(ctx *sdk.AppCtx, platform, image, deviceType string, hostID int64) (*Sim, error) {
	if hostID < 0 {
		return nil, errors.New("host_id must be 0 (local) or a positive Instances id")
	}
	if hostID == 0 {
		switch platform {
		case "android":
			return a.bootAndroid(ctx, image, deviceType)
		case "ios":
			return a.bootIOS(ctx, image, deviceType)
		default:
			return nil, errBadPlatform
		}
	}
	a.bootMu.Lock()
	defer a.bootMu.Unlock()
	client, err := a.ensureRemoteWorker(ctx, hostID)
	if err != nil {
		return nil, err
	}
	extra := []string(nil)
	if platform == "android" {
		extra = splitArgs(configOrDefault(ctx, "emulator_extra_args"))
	}
	device, err := client.boot(context.Background(), platform, image, deviceType, extra)
	if err != nil {
		return nil, err
	}
	projectID := ctx.CurrentProject()
	existing, err := dbFindSimByBackend(ctx.AppDB(), projectID, platform, "instances", hostID, device.ID)
	if err != nil {
		return nil, err
	}
	publicID := "sim_" + randHex(16)
	if existing != nil {
		publicID = existing.ID
	}
	sim := Sim{
		ID: publicID, ProjectID: projectID, Platform: platform,
		Runtime: device.Runtime, DeviceType: device.DeviceType, Status: device.Status,
		PID: device.PID, Serial: device.Serial, RunnerKind: "instances",
		InstanceID: hostID, BackendID: device.ID, BootedAt: device.BootedAt, Error: device.Error,
	}
	if err := dbUpsertSim(ctx.AppDB(), sim); err != nil {
		return nil, err
	}
	return dbGetSim(ctx.AppDB(), publicID)
}

func (a *App) ensureBootedSimOnHost(ctx *sdk.AppCtx, framework string, hostID int64) (*Sim, error) {
	projectID := ctx.CurrentProject()
	sims, err := dbListSims(ctx.AppDB(), projectID)
	if err != nil {
		return nil, err
	}
	for i := range sims {
		sim := &sims[i]
		if sim.Platform != framework || sim.InstanceID != hostID || sim.Status != "booted" {
			continue
		}
		if refreshed := a.refreshSimStatus(sim); refreshed != nil && refreshed.Status == "booted" {
			return refreshed, nil
		}
	}
	if framework == "android" {
		return a.bootOnHost(ctx, framework, configOrDefault(ctx, "android_image"), configOrDefault(ctx, "android_device_type"), hostID)
	}
	return a.bootOnHost(ctx, framework, configOrDefault(ctx, "ios_runtime"), configOrDefault(ctx, "ios_device_type"), hostID)
}

func (a *App) shutdownSim(ctx *sdk.AppCtx, sim *Sim) error {
	if sim == nil {
		return nil
	}
	a.stopStream(sim.ID)
	if sim.IsRemote() {
		client, err := a.ensureRemoteWorker(ctx, sim.InstanceID)
		if err != nil {
			return err
		}
		if err := client.devicePost(context.Background(), sim.NativeID(), "shutdown", map[string]any{}); err != nil {
			return err
		}
	} else {
		if p := a.sup.get(sim.ID); p != nil {
			a.sup.shutdownProcess(p)
			a.sup.drop(sim.ID)
		}
		switch sim.Platform {
		case "android":
			if sim.Serial != "" {
				_ = shutdownAndroidSim(sim.Serial)
			}
		case "ios":
			_ = shutdownIOSSim(sim.NativeID())
		}
	}
	_ = dbUpdateSim(ctx.AppDB(), sim.ID, map[string]any{"status": "shutdown", "pid": 0})
	_ = dbDeleteStreamToken(ctx.AppDB(), sim.ID)
	_ = dbStopActiveSimRuns(ctx.AppDB(), sim.ID)
	return nil
}

func (a *App) screenshotSim(ctx *sdk.AppCtx, sim *Sim) ([]byte, error) {
	if sim.IsRemote() {
		client, err := a.ensureRemoteWorker(ctx, sim.InstanceID)
		if err != nil {
			return nil, err
		}
		return client.screenshot(context.Background(), sim.NativeID())
	}
	if sim.Platform == "android" {
		return androidScreenshot(sim.Serial)
	}
	return iosScreenshot(sim.NativeID())
}

func (a *App) logsForSim(ctx *sdk.AppCtx, sim *Sim, lines int) (string, error) {
	if sim.IsRemote() {
		client, err := a.ensureRemoteWorker(ctx, sim.InstanceID)
		if err != nil {
			return "", err
		}
		return client.logs(context.Background(), sim.NativeID(), lines)
	}
	if sim.Platform == "android" {
		return androidLogs(sim.Serial, lines)
	}
	return iosLogs(sim.NativeID(), lines)
}

func (a *App) inputForSim(ctx *sdk.AppCtx, sim *Sim, event inputEvent) error {
	if err := validateInputEvent(event); err != nil {
		return err
	}
	if sim.IsRemote() {
		client, err := a.ensureRemoteWorker(ctx, sim.InstanceID)
		if err != nil {
			return err
		}
		return client.devicePost(context.Background(), sim.NativeID(), "input", event)
	}
	return a.sendInput(sim, event)
}

func (a *App) buildForSim(callCtx context.Context, ctx *sdk.AppCtx, sim *Sim, params buildParams) (*buildResult, string, error) {
	if !sim.IsRemote() {
		result, err := a.runBuild(callCtx, ctx, params)
		if err != nil {
			return nil, "", err
		}
		return result, filepathBase(result.ArtifactPath), nil
	}
	client, err := a.ensureRemoteWorker(ctx, sim.InstanceID)
	if err != nil {
		return nil, "", err
	}
	result, err := client.build(callCtx, remoteBuildRequest{
		Framework: params.Framework, SourceTGZB64: params.SourceTGZB64, DeviceID: sim.NativeID(),
		AndroidModule: params.Module, IOSScheme: params.Scheme, BuildCmd: params.BuildCmd,
		GradleExtra:     splitArgs(ctx.Config().Get("gradle_extra_args")),
		XcodeExtra:      splitArgs(ctx.Config().Get("xcodebuild_extra_args")),
		AllowedBuildEnv: buildEnvAllowlist(ctx.Config().Get("build_env_allowlist")),
	})
	if err != nil {
		return nil, "", err
	}
	return &buildResult{BundleID: result.BundleID, Activity: result.Activity}, result.ArtifactID, nil
}

func (a *App) installAndLaunchForSim(ctx *sdk.AppCtx, sim *Sim, result *buildResult, artifactID string) error {
	if !sim.IsRemote() {
		return a.installAndLaunch(sim, result)
	}
	client, err := a.ensureRemoteWorker(ctx, sim.InstanceID)
	if err != nil {
		return err
	}
	if err := client.devicePost(context.Background(), sim.NativeID(), "install", map[string]any{"artifact_id": artifactID}); err != nil {
		return err
	}
	return client.devicePost(context.Background(), sim.NativeID(), "launch", map[string]any{
		"bundle_id": result.BundleID, "activity": result.Activity,
	})
}

func (a *App) installArtifactForSim(ctx *sdk.AppCtx, sim *Sim, artifactPath, artifactID string) error {
	if sim.IsRemote() {
		if artifactID == "" {
			return errors.New("artifact_id required for a remote simulator")
		}
		client, err := a.ensureRemoteWorker(ctx, sim.InstanceID)
		if err != nil {
			return err
		}
		return client.devicePost(context.Background(), sim.NativeID(), "install", map[string]any{"artifact_id": artifactID})
	}
	artifact, err := a.validateArtifactPath(artifactPath, sim.Platform)
	if err != nil {
		return err
	}
	if sim.Platform == "android" {
		return installAndroidAPK(sim.Serial, artifact)
	}
	return installIOSApp(sim.NativeID(), artifact)
}

func (a *App) launchBundleForSim(ctx *sdk.AppCtx, sim *Sim, bundleID string) error {
	if sim.IsRemote() {
		client, err := a.ensureRemoteWorker(ctx, sim.InstanceID)
		if err != nil {
			return err
		}
		return client.devicePost(context.Background(), sim.NativeID(), "launch", map[string]any{"bundle_id": bundleID})
	}
	if sim.Platform == "android" {
		return launchAndroid(sim.Serial, bundleID, "")
	}
	return launchIOS(sim.NativeID(), bundleID)
}

func filepathBase(value string) string {
	if i := strings.LastIndexAny(value, `/\\`); i >= 0 {
		return value[i+1:]
	}
	return value
}

func (a *App) refreshRemoteSim(ctx *sdk.AppCtx, sim *Sim) *Sim {
	client, err := a.ensureRemoteWorker(ctx, sim.InstanceID)
	if err != nil {
		sim.Error = err.Error()
		return sim
	}
	var out struct {
		Device workerDevice `json:"device"`
	}
	err = client.getJSON(context.Background(), "/v1/devices/"+url.PathEscape(sim.NativeID())+"/state", &out)
	if err != nil {
		sim.Error = err.Error()
		return sim
	}
	if out.Device.Status != "" && out.Device.Status != sim.Status {
		_ = dbUpdateSim(ctx.AppDB(), sim.ID, map[string]any{"status": out.Device.Status, "pid": out.Device.PID, "serial": out.Device.Serial, "error": out.Device.Error})
		sim.Status, sim.PID, sim.Serial, sim.Error = out.Device.Status, out.Device.PID, out.Device.Serial, out.Device.Error
	}
	return sim
}
