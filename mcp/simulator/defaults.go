package main

import (
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	defaultAndroidImage     = "system-images;android-34;google_apis;x86_64"
	defaultAndroidDevice    = "pixel_6"
	defaultEmulatorArgs     = "-no-window -no-audio -no-snapshot-save"
	defaultIOSRuntime       = ""
	defaultIOSDeviceType    = "iPhone-15-Pro"
	defaultGradleExtraArgs  = ""
	defaultXcodeExtraArgs   = ""
	defaultIDBCompanionPath = ""
)

func configDefault(key string) string {
	switch key {
	case "android_image":
		return defaultAndroidImage
	case "android_device_type":
		return defaultAndroidDevice
	case "emulator_extra_args":
		return defaultEmulatorArgs
	case "ios_runtime":
		return defaultIOSRuntime
	case "ios_device_type":
		return defaultIOSDeviceType
	case "gradle_extra_args":
		return defaultGradleExtraArgs
	case "xcodebuild_extra_args":
		return defaultXcodeExtraArgs
	case "idb_companion_path":
		return defaultIDBCompanionPath
	default:
		return ""
	}
}

func configOrDefault(ctx *sdk.AppCtx, key string) string {
	if ctx != nil {
		if v := strings.TrimSpace(ctx.Config().Get(key)); v != "" {
			return v
		}
	}
	return configDefault(key)
}

func valueOrConfigDefault(ctx *sdk.AppCtx, val, key string) string {
	if v := strings.TrimSpace(val); v != "" {
		return v
	}
	return configOrDefault(ctx, key)
}
