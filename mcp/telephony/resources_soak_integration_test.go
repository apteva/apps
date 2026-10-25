//go:build integration

package main

import (
	"context"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Optional reproducible resource-policy stress layered on the existing real
// browser -> WebRTC -> SIP soak. No provider or running app is contacted.
func startResourceSoak(t *testing.T, app *App, ctx *sdk.AppCtx, output string) func() {
	if os.Getenv("TELEPHONY_RESOURCE_SOAK") != "1" {
		return func() {}
	}
	size := soakEnvInt(t, "TELEPHONY_RESOURCE_SOAK_RX", 256<<10, 0, 4<<20)
	reset, e := ctx.SetResourcePolicyForTest(sdk.ResourcePolicy{TotalConcurrency: 4, Network: map[string]sdk.SocketPolicy{"sip_media": {ReceiveBytes: size}, "webrtc_media": {ReceiveBytes: size}}})
	if e != nil {
		t.Fatal(e)
	}
	c, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var reads, failures atomic.Uint64
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-c.Done():
					return
				case <-ticker.C:
					e := ctx.WithWorkload(c, "diagnostics", func(query context.Context) error {
						now := time.Now()
						_, e := app.db().audioDashboard(query, "project-a", audioDashboardFilter{From: now.Add(-time.Hour).UnixMilli(), Until: now.Add(time.Second).UnixMilli(), Limit: 50}, now, &app.dashboardExtras)
						return e
					})
					if e == nil {
						reads.Add(1)
					} else if c.Err() == nil {
						failures.Add(1)
					}
				}
			}
		}()
	}
	return func() {
		cancel()
		wg.Wait()
		raw, _ := json.MarshalIndent(map[string]any{"diagnostic_reads": reads.Load(), "errors": failures.Load(), "resources": ctx.ResourceStatus()}, "", "  ")
		_ = os.WriteFile(filepath.Join(output, "resources.json"), raw, 0600)
		reset()
		if reads.Load() == 0 {
			t.Error("resource stress did no diagnostic work")
		}
		if failures.Load() != 0 {
			t.Errorf("resource stress diagnostic errors: %d", failures.Load())
		}
	}
}
