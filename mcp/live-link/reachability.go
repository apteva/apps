package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// A tunnel process that is running does not prove the public URL works.
// zrok in particular keeps its agent connected while its public frontend
// answers "share … not found" (for example when the account is over its
// plan limits). The reachability loop probes the public URL while a run is
// up so the panel only says "Live" when the URL actually reaches the target.

const (
	reachabilityFastInterval = 5 * time.Second
	reachabilityFastWindow   = time.Minute
	reachabilitySlowInterval = time.Minute
	reachabilityTimeout      = 10 * time.Second
)

const zrokShareNotFoundHint = "zrok's public frontend cannot reach this share (\"share not found\"). " +
	"The zrok2 process is running, but zrok is not routing traffic to it. This usually means the zrok " +
	"account is over its plan limits (bandwidth or share count); zrok restores routing once the usage " +
	"window clears. Check the account's usage, or switch to Cloudflare Quick or Cloudflare Named."

const zrokShareUnauthorizedHint = "zrok refused to create the share (401 shareUnauthorized). " +
	"This usually means the zrok account is over its plan limits (bandwidth or share count); " +
	"zrok allows new shares again once the usage window clears. Check the account's usage, " +
	"or switch to Cloudflare Quick or Cloudflare Named."

// probePublicURL is swapped in tests.
var probePublicURL = func(ctx context.Context, publicURL string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, publicURL, nil)
	if err != nil {
		return 0, nil, err
	}
	// A non-browser user agent keeps provider interstitials (zrok, ngrok)
	// out of the way, so the probe sees what API clients see.
	req.Header.Set("User-Agent", "apteva-live-link/0.6 (reachability)")
	client := &http.Client{
		Timeout: reachabilityTimeout,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	return resp.StatusCode, body, nil
}

// classifyProbe decides whether a public URL response came from the target
// (reachable, whatever its status) or from the tunnel provider's own error
// page (not reachable).
func classifyProbe(status int, body []byte, err error) (bool, string) {
	if err != nil {
		var urlErr interface{ Timeout() bool }
		if errors.As(err, &urlErr) && urlErr.Timeout() {
			return false, "public URL did not answer within " + reachabilityTimeout.String()
		}
		return false, "public URL not reachable: " + err.Error()
	}
	lower := strings.ToLower(string(body))
	if strings.Contains(lower, "are you running zrok") ||
		(status == http.StatusNotFound && strings.Contains(lower, "zrok") && strings.Contains(lower, "not found")) {
		return false, zrokShareNotFoundHint
	}
	switch status {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 530:
		return false, fmt.Sprintf("tunnel provider answered HTTP %d: the tunnel is not connected to the target", status)
	}
	return true, ""
}

// explainExitReason adds a plain-language explanation to provider errors
// whose raw text does not say what to do.
func explainExitReason(reason string) string {
	if strings.Contains(reason, "shareUnauthorized") && !strings.Contains(reason, zrokShareUnauthorizedHint) {
		return zrokShareUnauthorizedHint + " Raw error: " + reason
	}
	return reason
}

// runReachabilityLoop probes the active run's public URL until ctx ends.
func (a *App) runReachabilityLoop(ctx context.Context) {
	timer := time.NewTimer(reachabilityFastInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		next := reachabilitySlowInterval
		snap := a.mgr.Snapshot()
		if snap.Status == StatusRunning && snap.PublicURL != "" {
			if time.Since(snap.StartedAt) < reachabilityFastWindow || snap.Reachable == nil || !*snap.Reachable {
				next = reachabilityFastInterval
			}
			probeCtx, cancel := context.WithTimeout(ctx, reachabilityTimeout+2*time.Second)
			status, body, err := probePublicURL(probeCtx, snap.PublicURL)
			cancel()
			if ctx.Err() != nil {
				return
			}
			ok, detail := classifyProbe(status, body, err)
			if changed := a.mgr.SetReachability(snap.RunID, ok, detail); changed && globalCtx != nil {
				globalCtx.Logger().Info("public URL reachability changed",
					"run_id", snap.RunID, "reachable", ok, "detail", detail)
				globalCtx.Emit("tunnel.reachability", map[string]any{
					"run_id": snap.RunID, "reachable": ok, "detail": detail,
				})
			}
		} else {
			next = reachabilityFastInterval
		}
		timer.Reset(next)
	}
}
