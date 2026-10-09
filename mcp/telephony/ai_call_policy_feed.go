package main

// The SDK's telemetry channel reconnects transparently. The policy needs the
// connection boundary too: a missed 'working' transition must never be treated
// as caller silence. Use the same public, permission-gated SSE endpoint with
// bounded reads, and invalidate all conversation phases after every disconnect.
// No Core/server/SDK modification or undocumented endpoint is involved.
import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type aiPhaseFeed struct{ cancel context.CancelFunc }

func (a *App) setAIPhaseFeed(agent int64, live bool) {
	a.aiPolicies.mu.RLock()
	defer a.aiPolicies.mu.RUnlock()
	for _, r := range a.aiPolicies.calls {
		if r.agent == agent {
			r.mu.Lock()
			if !r.state.IdleSince.IsZero() || !r.state.ResponseDeadline.IsZero() {
				r.dirty = true
			}
			r.feedLive = live
			r.phase = "unknown"
			r.state.IdleSince = time.Time{}
			r.state.ResponseDeadline = time.Time{}
			r.mu.Unlock()
		}
	}
}
func (a *App) ensureAIPhaseFeed(ctx *sdk.AppCtx, agent int64) {
	gateway := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	token := os.Getenv("APTEVA_APP_TOKEN")
	if gateway == "" || token == "" || agent <= 0 {
		return
	}
	a.aiPolicies.mu.Lock()
	if a.aiPolicies.stopped {
		a.aiPolicies.mu.Unlock()
		return
	}
	if a.aiPolicies.feeds == nil {
		a.aiPolicies.feeds = map[int64]*aiPhaseFeed{}
	}
	if a.aiPolicies.feeds[agent] != nil {
		a.aiPolicies.mu.Unlock()
		return
	}
	feedCtx, cancel := context.WithCancel(context.Background())
	a.aiPolicies.feeds[agent] = &aiPhaseFeed{cancel: cancel}
	a.aiPolicies.mu.Unlock()
	go func() {
		query := url.Values{"agent_id": {strconv.FormatInt(agent, 10)}, "thread_prefix": {"tel-"}, "events": {"realtime.state,realtime.user"}}
		transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 15 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		for attempt := 0; feedCtx.Err() == nil; attempt++ {
			permanent, err := a.readAIPhaseFeed(feedCtx, client, gateway+"/api/apps/callback/telemetry?"+query.Encode(), token, agent)
			a.setAIPhaseFeed(agent, false)
			if feedCtx.Err() != nil {
				return
			}
			// Never log HTTP bodies, tokens, or request URLs. This degrades only silence
			// handling; the independent persisted AI duration limit remains enforced.
			ctx.Logger().Warn("AI conversation phase telemetry unavailable; inactivity paused", "agent", agent, "permanent", permanent, "error", err)
			if permanent {
				return
			}
			timer := time.NewTimer(time.Duration(min(30, 1<<min(attempt, 5))) * time.Second)
			select {
			case <-feedCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}
func (a *App) readAIPhaseFeed(ctx context.Context, client *http.Client, endpoint, token string, agent int64) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return true, fmt.Errorf("invalid phase endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("phase transport unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 || resp.StatusCode == 405, fmt.Errorf("phase endpoint HTTP %d", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return true, fmt.Errorf("phase endpoint did not return SSE")
	}
	a.setAIPhaseFeed(agent, true)
	// Server sends a 25s comment heartbeat. Stop a silent socket after 35s;
	// invalidate phases immediately and require a new state after reconnect.
	watchdog := time.AfterFunc(35*time.Second, func() { a.setAIPhaseFeed(agent, false); resp.Body.Close() })
	defer watchdog.Stop()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
		watchdog.Reset(35 * time.Second)
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event sdk.TelemetryStreamEvent
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) != nil || event.AgentID != agent || !strings.HasPrefix(event.ThreadID, "tel-") {
			continue
		}
		if event.Type == "realtime.state" || event.Type == "realtime.user" {
			a.observeAIPhase(event)
		}
	}
	return false, fmt.Errorf("phase stream ended")
}
func (a *App) pruneAIPhaseFeeds() {
	a.aiPolicies.mu.Lock()
	defer a.aiPolicies.mu.Unlock()
	needed := map[int64]bool{}
	for _, r := range a.aiPolicies.calls {
		needed[r.agent] = true
	}
	for agent, feed := range a.aiPolicies.feeds {
		if !needed[agent] {
			feed.cancel()
			delete(a.aiPolicies.feeds, agent)
		}
	}
}
func (a *App) stopAIPolicies() {
	a.aiPolicies.mu.Lock()
	defer a.aiPolicies.mu.Unlock()
	a.aiPolicies.stopped = true
	for agent, feed := range a.aiPolicies.feeds {
		feed.cancel()
		delete(a.aiPolicies.feeds, agent)
	}
}
