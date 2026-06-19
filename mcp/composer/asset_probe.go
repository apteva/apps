package main

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func probeAssetDurationSeconds(app *sdk.AppCtx, src string) float64 {
	url, err := resolveAssetLocal(app, src)
	if err != nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffprobePath(),
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		url,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}
