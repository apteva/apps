package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// These are tier 3 tests: model decisions drive the browser, while Go checks
// outcomes independently. Live runs require an explicitly configured test
// creator/context; local fixture coverage runs with the normal tier 3 gate.
func TestLLMPatreonReliabilityFixtureLive(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_LLM_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_LLM_TESTS=1")
	}
	backend := envDefault("COMPUTER_LLM_BROWSER_BACKEND", "local")
	if backend == "browserbase" && !browserbaseCredentialsAvailable() {
		t.Fatal("Browserbase credentials required")
	}
	sc := tk.SpawnSidecar(t, ".")
	c := &localComputerMCPClient{sidecar: sc}
	// No saved context is needed for this deterministic fixture.
	opened := sc.MCP("browser_session", map[string]any{"action": "open", "backend": backend, "url": patreonReliabilityDataURL()})
	sid := stringValue(opened["session_id"])
	if sid == "" {
		t.Fatalf("open: %v", opened)
	}
	defer closePatreonTestSession(t, c, sid)
	shot := runPatreonAgent(t, c, sid, "On this disposable publishing fixture, enable the audience switch named Free members and paid members (its label has additional explanatory text), then enable Schedule post. Do not activate the final Schedule button. Verify both switches are on and finish.", 12)
	for _, name := range []string{"Free members and paid members", "Schedule post"} {
		target := findLiveTarget(t, mapsFromAny(shot["som"]), name, false)
		if !boolFromAny(target["checked"]) {
			t.Fatalf("agent did not enable %s: %v", name, target)
		}
	}
	draft := c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "wait_for", "timeout_ms": 500, "conditions": []any{map[string]any{"type": "selector_present", "selector": "#status[data-state=draft]"}}})
	if !boolFromAny(draft["matched"]) {
		t.Fatal("agent committed instead of configuring")
	}
}

func TestLLMPatreonMediaPublishLive(t *testing.T) {
	requirePatreonTier3(t)
	c := newLocalComputerMCPClient(t)
	opened := c.call(t, "browser_session", map[string]any{"action": "open", "context_id": os.Getenv("COMPUTER_PATREON_CONTEXT_ID"), "url": requireLiveEnv(t, "COMPUTER_PATREON_CREATOR_URL")})
	sid := stringValue(opened["session_id"])
	if sid == "" {
		t.Fatalf("open: %v", opened)
	}
	defer closePatreonTestSession(t, c, sid)
	title := "Computer tier 3 media " + time.Now().UTC().Format("20060102-150405")
	goal := fmt.Sprintf("This is the user's disposable Patreon test creator. Create and publish exactly ONE video post titled %q, using the embed URL %s. Use the Create post flow, select video and embed URL. Verify media loaded and the draft saved before publishing. Publishing to this test creator is authorized. Do not add the media again if it has loaded. Do not send messages, change account settings, or create additional posts. Verify the final post URL, exact title, and rendered media before finishing.", title, patreonMediaPublishFixtureURL)
	shot := runPatreonAgent(t, c, sid, goal, 30)
	u := firstNonEmpty(stringValue(shot["current_url"]), stringValue(shot["url"]))
	if !strings.Contains(u, "/posts/") || strings.Contains(u, "/edit") || strings.Contains(u, "/new") {
		t.Fatalf("agent did not reach published post: %s", u)
	}
	verified := c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "wait_for", "match": "all", "timeout_ms": 15000, "conditions": []any{map[string]any{"type": "text_present", "value": title}, map[string]any{"type": "media_present"}}})
	if !boolFromAny(verified["matched"]) {
		t.Fatalf("published title/media not verified: %s", mustJSON(patreonEvidence(verified)))
	}
	assertRealMediaLoaded(t, verified, false)
	t.Logf("Verified published test post: %s", u)
}

// Exercise the combination that separate media-publish and text-schedule tests
// cannot prove: the same Bunny video survives scheduling and is automatically
// published when its deadline arrives. All mutations are chosen by the model;
// the harness only observes, reloads, and checks the immutable post identity.
func TestLLMPatreonScheduledVideoPublicationLive(t *testing.T) {
	requirePatreonTier3(t)
	c := newLocalComputerMCPClient(t)
	opened := c.call(t, "browser_session", map[string]any{"action": "open", "context_id": os.Getenv("COMPUTER_PATREON_CONTEXT_ID"), "url": requireLiveEnv(t, "COMPUTER_PATREON_CREATOR_URL")})
	sid := stringValue(opened["session_id"])
	if sid == "" {
		t.Fatalf("open: %v", opened)
	}
	defer closePatreonTestSession(t, c, sid)
	writePatreonEvidence(t, "opened.json", []byte(mustJSON(patreonEvidence(opened))))
	if intFromAny(opened["effective_timeout_seconds"]) < 1800 {
		t.Fatal("scheduled video requires the normal long session lifetime")
	}
	zone := stringValue(opened["effective_timezone"])
	if zone == "" {
		t.Fatal("browser did not report its timezone; cannot safely choose a near-term schedule")
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	title := "Computer tier 3 scheduled video " + time.Now().UTC().Format("20060102-150405")
	var draft map[string]any
	if !t.Run("prepare_video", func(t *testing.T) {
		goal := fmt.Sprintf("On this disposable Patreon test creator, create exactly ONE video draft titled %q using embed URL %s. Verify that the Bunny player loaded and the draft saved. Do not add the URL again once media has loaded. Stop in this draft editor BEFORE Publish or Schedule. Do not create other posts or change account settings.", title, patreonMediaPublishFixtureURL)
		draft = runPatreonAgent(t, c, sid, goal, 20)
		assertPatreonTitle(t, draft, title)
		assertRealMediaLoaded(t, draft, false)
		if stringValue(draft["draft_save_state"]) != "saved" {
			t.Fatalf("video draft not saved: %s", mustJSON(patreonEvidence(draft)))
		}
	}) {
		return
	}
	draftURL := firstNonEmpty(stringValue(draft["current_url"]), stringValue(draft["url"]))
	host, postID := patreonPostIdentity(draftURL)
	if postID == "" || !strings.HasSuffix(draftURL, "/edit") {
		t.Fatalf("video draft has no editable post identity: %s", draftURL)
	}
	// Choose the deadline after media preparation, leaving time for model-driven
	// settings and commit. Minute precision matches the site's time control.
	scheduledAt := time.Now().In(location).Add(6 * time.Minute).Truncate(time.Minute).Add(time.Minute)
	date, clock := scheduledAt.Format("2006-01-02"), scheduledAt.Format("3:04 PM")
	writePatreonEvidence(t, "schedule.json", []byte(mustJSON(map[string]any{"title": title, "post_id": postID, "draft_url": draftURL, "timezone": zone, "scheduled_at": scheduledAt.Format(time.RFC3339)})))
	t.Logf("Scheduling Bunny video post=%s at %s (%s)", postID, scheduledAt.Format(time.RFC3339), zone)
	assertIdentity := func(t *testing.T, result map[string]any) {
		t.Helper()
		actualHost, actualID := patreonPostIdentity(firstNonEmpty(stringValue(result["current_url"]), stringValue(result["url"])))
		if actualHost != host || actualID != postID {
			t.Fatalf("observation belongs to another post: host=%s id=%s, want %s/%s", actualHost, actualID, host, postID)
		}
	}
	if !t.Run("configure_schedule", func(t *testing.T) {
		goal := fmt.Sprintf("Configure this existing video draft %q for Free access and Set publish date %s at %s in the page timezone (%s). Preserve the loaded Bunny video. Verify the values and stop BEFORE the final Schedule or Publish action. Do not create another draft.", title, date, clock, zone)
		shot := runPatreonAgent(t, c, sid, goal, 16)
		assertIdentity(t, shot)
		assertPatreonTitle(t, shot, title)
		assertRealMediaLoaded(t, shot, false)
		shot = observePatreonScheduleFields(t, c, sid, shot)
		for kind, want := range map[string]string{"date": date, "time": scheduledAt.Format("15:04")} {
			target := findTemporalLiveTarget(t, mapsFromAny(shot["som"]), kind, kind == "date")
			if stringValue(target["current_value"]) != want {
				t.Fatalf("schedule %s=%v, want %s", kind, target["current_value"], want)
			}
		}
		audience := c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "wait_for", "timeout_ms": 1000, "conditions": []any{map[string]any{"type": "selector_present", "selector": `input[value="public"]:checked`}}})
		if !boolFromAny(audience["matched"]) {
			t.Fatal("scheduled video did not retain Free access")
		}
		writePatreonEvidence(t, "configured.json", []byte(mustJSON(patreonEvidence(shot))))
	}) {
		return
	}
	if time.Until(scheduledAt) < 2*time.Minute {
		t.Fatal("insufficient time remains to commit and independently verify the scheduled state")
	}
	if !t.Run("commit_and_reload", func(t *testing.T) {
		goal := fmt.Sprintf("The user authorizes final scheduling of this exact disposable video post %q, post ID %s. Its Bunny video, Free access, and publish date %s at %s (%s) have been independently verified. Click the final Schedule action once, complete any required confirmation, dismiss the success dialog with OK, and reopen this same post editor to verify Scheduled for and its loaded video. Do not publish immediately, change the date/time, duplicate the video, or create another post.", title, postID, date, clock, zone)
		runPatreonAgent(t, c, sid, goal, 12)
		for _, stage := range []string{"scheduled-confirmation", "scheduled-after-reload"} {
			if stage == "scheduled-after-reload" {
				c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "reload"})
			}
			result := c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "wait_for", "match": "all", "timeout_ms": 30000, "conditions": []any{
				map[string]any{"type": "text_present", "value": "Scheduled for " + scheduledAt.Format("Jan 2, 2006") + " at " + clock},
				map[string]any{"type": "media_present"},
			}})
			writePatreonEvidence(t, stage+".json", []byte(mustJSON(patreonEvidence(result))))
			if !boolFromAny(result["matched"]) {
				t.Fatalf("scheduled video confirmation failed: %s", mustJSON(patreonEvidence(result)))
			}
			assertIdentity(t, result)
			assertRealMediaLoaded(t, result, false)
			assertPatreonTitle(t, liveScreenshot(t, c, sid), title)
		}
		if !time.Now().Before(scheduledAt) {
			t.Fatal("scheduled state was not verified before the publication deadline")
		}
	}) {
		return
	}
	// No LLM calls or mutating browser actions during the wait: only the site's
	// scheduler can publish the post between these two independently checked states.
	// Patreon uses Update for a published post, Save for a scheduled post, and
	// Publish for a draft. The published editor has no literal Published banner.
	for time.Now().Before(scheduledAt) {
		remaining := time.Until(scheduledAt)
		t.Logf("Waiting for automatic publication in %s", remaining.Round(time.Second))
		time.Sleep(min(remaining, 30*time.Second))
	}
	t.Run("automatic_publication", func(t *testing.T) {
		deadline := scheduledAt.Add(5 * time.Minute)
		published := false
		for attempt := 0; time.Now().Before(deadline); attempt++ {
			c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "reload"})
			result := c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "wait_for", "match": "all", "timeout_ms": 10000, "conditions": []any{
				map[string]any{"type": "text_present", "value": "Update"},
				map[string]any{"type": "text_absent", "value": "Scheduled for"},
				map[string]any{"type": "media_present"},
			}})
			writePatreonEvidence(t, fmt.Sprintf("publication-check-%02d.json", attempt), []byte(mustJSON(patreonEvidence(result))))
			shot := liveScreenshot(t, c, sid)
			writePatreonEvidence(t, fmt.Sprintf("publication-state-%02d.json", attempt), []byte(mustJSON(patreonEvidence(shot))))
			if boolFromAny(result["matched"]) {
				findLiveTarget(t, mapsFromAny(shot["som"]), "Update", true)
				assertIdentity(t, result)
				assertPatreonTitle(t, shot, title)
				assertRealMediaLoaded(t, result, false)
				published = true
				break
			}
			t.Logf("Post has not yet independently confirmed Published; retrying within the five-minute grace period")
			time.Sleep(20 * time.Second)
		}
		if !published {
			t.Fatal("scheduled Bunny video did not automatically reach Published within five minutes of its deadline")
		}
		goal := fmt.Sprintf("This exact video post %q (post ID %s) has now automatically published, independently verified by the harness. Navigate to its published reader-facing post page and verify its exact title and loaded Bunny video. Read-only navigation only: do not edit, save, schedule, or publish anything and do not create another post.", title, postID)
		shot := runPatreonAgent(t, c, sid, goal, 12)
		assertIdentity(t, shot)
		u := firstNonEmpty(stringValue(shot["current_url"]), stringValue(shot["url"]))
		if strings.Contains(u, "/edit") || strings.Contains(u, "/new") {
			t.Fatalf("agent did not reach the published post page: %s", u)
		}
		c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "reload"})
		result := c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "wait_for", "match": "all", "timeout_ms": 30000, "conditions": []any{map[string]any{"type": "text_present", "value": title}, map[string]any{"type": "media_present"}}})
		writePatreonEvidence(t, "published-after-reload.json", []byte(mustJSON(patreonEvidence(result))))
		if !boolFromAny(result["matched"]) {
			t.Fatalf("published title/media missing after reload: %s", mustJSON(patreonEvidence(result)))
		}
		assertIdentity(t, result)
		assertRealMediaLoaded(t, result, false)
		t.Logf("Verified automatically published Bunny video: title=%q url=%s scheduled_at=%s verified_at=%s", title, u, scheduledAt.Format(time.RFC3339), time.Now().Format(time.RFC3339))
	})
}

func TestLLMPatreonSchedulingLive(t *testing.T) {
	requirePatreonTier3(t)
	c := newLocalComputerMCPClient(t)
	opened := c.call(t, "browser_session", map[string]any{"action": "open", "context_id": os.Getenv("COMPUTER_PATREON_CONTEXT_ID"), "url": requireLiveEnv(t, "COMPUTER_PATREON_CREATOR_URL")})
	sid := stringValue(opened["session_id"])
	if sid == "" {
		t.Fatalf("open: %v", opened)
	}
	started := time.Now()
	defer closePatreonTestSession(t, c, sid)
	scheduledDate := time.Now().AddDate(0, 0, 14)
	date := scheduledDate.Format("2006-01-02")
	title := "Computer tier 3 scheduling " + time.Now().UTC().Format("20060102-150405")
	goal := fmt.Sprintf("On this disposable Patreon test creator, create exactly one new text draft titled %q, with body Scheduling test draft. Enable Free access and Set publish date, and set the date to %s and time to 7:00 PM. Verify the configured values. Stop BEFORE the final Schedule or Publish action; leave the draft for inspection. Do not change account settings or create additional drafts.", title, date)
	shot := runPatreonAgent(t, c, sid, goal, 30)
	u := firstNonEmpty(stringValue(shot["current_url"]), stringValue(shot["url"]))
	if !strings.Contains(u, "/edit") {
		t.Fatalf("scheduling should remain a draft: %s", u)
	}
	assertPatreonTitle(t, shot, title)
	final := findLiveTarget(t, mapsFromAny(shot["som"]), "Schedule", true)
	if !boolFromAny(final["dangerous"]) {
		t.Fatalf("Schedule consequence missing: %v", final)
	}
	shot = observePatreonScheduleFields(t, c, sid, shot)
	dateTarget := findTemporalLiveTarget(t, mapsFromAny(shot["som"]), "date", true)
	t.Logf("Configured scheduling draft %s, date target: %s", u, mustJSON(dateTarget))
	// Date/time values are checked against the tool's browser readback, not the model's done claim.
	state := mustJSON(shot["som"])
	if !strings.Contains(state, date) && !strings.Contains(state, time.Now().AddDate(0, 0, 14).Format("01/02/2006")) {
		t.Fatalf("requested date absent from final controls: %s", state)
	}
	if !strings.Contains(state, "7:00 PM") && !strings.Contains(state, "19:00") {
		t.Fatalf("requested time absent from final controls: %s", state)
	}
	audience := c.call(t, "computer_use", map[string]any{
		"session_id": sid, "action": "wait_for", "match": "all", "timeout_ms": 1000,
		"conditions": []any{
			map[string]any{"type": "selector_present", "selector": `input[value="public"]:checked`},
			map[string]any{"type": "text_present", "value": "Scheduling test draft"},
		},
	})
	if !boolFromAny(audience["matched"]) {
		t.Fatalf("Free access and draft body were not preserved: %s", mustJSON(patreonEvidence(audience)))
	}
	// The preparation and commit are separate model phases so the test can
	// independently validate values before authorizing the final consequence.
	t.Run("scheduled_publication", func(t *testing.T) {
		goal := fmt.Sprintf("The user authorizes final scheduling on this disposable Patreon creator. This exact draft, titled %q, has already been independently verified with Free access enabled and publish date %s at 7:00 PM in the page timezone. Activate the final Schedule action and complete any required scheduling confirmation. Schedule exactly this one post, do not publish immediately, create another draft, or change the configured date/time. Once dispatched, inspect the outcome instead of blindly repeating Schedule. Dismiss the success dialog with OK, then verify this same editor shows Scheduled for with the correct date/time and exact title before finishing.", title, date)
		committed := runPatreonAgent(t, c, sid, goal, 12)
		writePatreonEvidence(t, "committed.json", []byte(mustJSON(patreonEvidence(committed))))
		host, postID := patreonPostIdentity(u)
		if postID == "" {
			t.Fatalf("draft URL has no post identity: %s", u)
		}
		conditions := []any{
			map[string]any{"type": "text_present", "value": "Scheduled for " + scheduledDate.Format("Jan 2, 2006") + " at 7:00 PM"},
			map[string]any{"type": "url_contains", "value": postID},
		}
		assertScheduled := func(stage string) {
			t.Helper()
			result := c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "wait_for", "match": "all", "timeout_ms": 30000, "conditions": conditions})
			writePatreonEvidence(t, stage+".json", []byte(mustJSON(patreonEvidence(result))))
			if !boolFromAny(result["matched"]) {
				t.Fatalf("Patreon did not independently confirm scheduled post (%s): %s", stage, mustJSON(patreonEvidence(result)))
			}
			actualHost, actualID := patreonPostIdentity(stringValue(result["current_url"]))
			if actualHost != host || actualID != postID {
				t.Fatalf("scheduled confirmation belongs to another post: %v", result["current_url"])
			}
			readback := liveScreenshot(t, c, sid)
			writePatreonEvidence(t, stage+"-state.json", []byte(mustJSON(patreonEvidence(readback))))
			assertPatreonTitle(t, readback, title)
		}
		assertScheduled("scheduled-confirmation")
		// A reload rules out relying solely on a transient success toast or
		// an optimistic client-side state update.
		c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "reload"})
		assertScheduled("scheduled-after-reload")
		t.Logf("Verified persisted scheduled post: title=%q url=%s date=%s time=19:00", title, firstNonEmpty(stringValue(committed["current_url"]), stringValue(committed["url"])), date)
	})
	// Retain the old live release gate's real five-minute-boundary regression.
	// Most of this time is already spent driving and verifying the composer.
	t.Run("session_lifetime", func(t *testing.T) {
		deadline := started.Add(6*time.Minute + 15*time.Second)
		if remaining := time.Until(deadline); remaining > 0 {
			t.Logf("Checking saved-context session beyond five minutes in %s", remaining.Round(time.Second))
			time.Sleep(remaining)
		}
		status := c.call(t, "browser_session", map[string]any{"action": "status", "session_id": sid})
		writePatreonEvidence(t, "status.json", []byte(mustJSON(patreonEvidence(status))))
		if boolFromAny(status["failed"]) || stringValue(status["status"]) != "active" || intFromAny(status["session_age_seconds"]) <= 300 {
			t.Fatalf("saved-context browser expired during the workflow: %s", mustJSON(patreonEvidence(status)))
		}
	})
}

// Patreon replaces /posts/<id>/edit with /posts/<title>-<id>/edit after
// scheduling. The numeric suffix, not the cosmetic slug, identifies the post.
func patreonPostIdentity(raw string) (string, string) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] != "posts" {
			continue
		}
		segments := strings.Split(parts[i+1], "-")
		id := segments[len(segments)-1]
		if n, err := strconv.ParseUint(id, 10, 64); err == nil && n > 0 {
			return strings.ToLower(u.Hostname()), id
		}
		return "", ""
	}
	return "", ""
}

func TestPatreonPostIdentity(t *testing.T) {
	for _, tc := range []struct{ raw, id string }{
		{"https://www.patreon.com/creator/posts/168676125/edit", "168676125"},
		{"https://www.patreon.com/creator/posts/computer-tier-3-168676125/edit", "168676125"},
		{"https://www.patreon.com/posts/another-post-42", "42"},
		{"https://www.patreon.com/creator?post=168676125", ""},
		{"https://www.patreon.com/posts/no-number/edit", ""},
		{"/posts/168676125/edit", ""},
	} {
		_, id := patreonPostIdentity(tc.raw)
		if id != tc.id {
			t.Errorf("identity(%q)=%q, want %q", tc.raw, id, tc.id)
		}
	}
}

// The agent may finish at Audience after checking the date/time earlier.
// Move only the viewport to observe the configured fields; never set values.
func observePatreonScheduleFields(t *testing.T, c *localComputerMCPClient, sid string, shot map[string]any) map[string]any {
	t.Helper()
	for attempt := 0; attempt < 4; attempt++ {
		hasDate, hasTime := false, false
		for _, target := range mapsFromAny(shot["som"]) {
			hasDate = hasDate || stringValue(target["type"]) == "date"
			hasTime = hasTime || stringValue(target["type"]) == "time"
		}
		if hasDate && hasTime {
			return shot
		}
		var panel string
		for _, region := range mapsFromAny(shot["scroll_regions"]) {
			if !boolFromAny(region["document"]) && strings.Contains(mustJSON(region["content_hints"]), "Set publish date") {
				panel = stringValue(region["id"])
				break
			}
		}
		if panel == "" {
			t.Fatal("scheduling settings panel missing")
		}
		c.call(t, "computer_use", map[string]any{"session_id": sid, "action": "scroll", "target_id": panel, "direction": "down", "amount": 450})
		shot = liveScreenshot(t, c, sid)
	}
	t.Fatal("configured date/time controls not found after scrolling settings")
	return nil
}

// Titles are textarea values, not body text. Read the semantic field value so
// a wrapped or truncated visible label cannot produce a false mismatch.
func assertPatreonTitle(t *testing.T, shot map[string]any, title string) {
	t.Helper()
	target := findLiveTarget(t, mapsFromAny(shot["som"]), "Title", true)
	if stringValue(target["current_value"]) != title {
		t.Fatalf("scheduled post title mismatch: %v", target)
	}
}

func requirePatreonTier3(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_COMPUTER_LLM_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_LLM_TESTS=1")
	}
	if os.Getenv("COMPUTER_PATREON_CREATOR_URL") == "" {
		t.Skip("configure COMPUTER_PATREON_CREATOR_URL and test context for live tier 3")
	}
	if os.Getenv("COMPUTER_PATREON_CONTEXT_ID") == "" && os.Getenv("COMPUTER_PATREON_PROVIDER_CONTEXT_ID") == "" {
		t.Fatal("Patreon test context required")
	}
}

func closePatreonTestSession(t *testing.T, c *localComputerMCPClient, sid string) {
	t.Helper()
	result := c.call(t, "browser_session", map[string]any{"action": "close", "session_id": sid})
	writePatreonEvidence(t, "close.json", []byte(mustJSON(patreonEvidence(result))))
	if boolFromAny(result["failed"]) || boolFromAny(result["cleanup_pending"]) || (!boolFromAny(result["closed"]) && !boolFromAny(result["already_closed"])) {
		t.Errorf("test browser release was not confirmed: %s", mustJSON(patreonEvidence(result)))
	}
}

type patreonAgentDecision struct {
	Action    string `json:"action"`
	Arguments string `json:"arguments_json"`
	Reason    string `json:"reason"`
}

func runPatreonAgent(t *testing.T, c *localComputerMCPClient, sid, goal string, maxSteps int, observers ...func(map[string]any, map[string]any)) map[string]any {
	t.Helper()
	var toolDescription string
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "computer_use" {
			toolDescription = tool.Description + "\nInput schema: " + mustJSON(tool.InputSchema)
		}
	}
	var history []any
	for step := 0; step < maxSteps; step++ {
		shot := liveScreenshot(t, c, sid)
		frame := decodeScreenshot(t, shot)
		writePatreonEvidence(t, fmt.Sprintf("%02d-frame.jpg", step), frame)
		writePatreonEvidence(t, fmt.Sprintf("%02d-state.json", step), []byte(mustJSON(patreonEvidence(shot))))
		prompt := "You are driving Computer through its computer_use tool. Choose ONE next action based on the screenshot and current tool output, or done when the task is verified. Return the exact computer_use arguments as a JSON string; omit session_id (the harness supplies it). No browser scripting or external tools.\nTask: " + goal + "\nTool: " + toolDescription + "\nHistory (field observations describe state before that action): " + mustJSON(history) + "\nCurrent observation: " + mustJSON(patreonEvidence(shot))
		var d patreonAgentDecision
		callComputerLLM(t, frame, prompt, `{"type":"object","additionalProperties":false,"properties":{"action":{"type":"string","enum":["computer_use","done"]},"arguments_json":{"type":"string"},"reason":{"type":"string"}},"required":["action","arguments_json","reason"]}`, &d)
		t.Logf("AGENT step=%d decision=%s reason=%s args=%s", step, d.Action, d.Reason, d.Arguments)
		writePatreonEvidence(t, fmt.Sprintf("%02d-decision.json", step), []byte(mustJSON(d)))
		if d.Action == "done" {
			return shot
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(d.Arguments), &args); err != nil {
			t.Fatalf("agent invalid arguments: %v", err)
		}
		args["session_id"] = sid
		start := time.Now()
		result := c.call(t, "computer_use", args)
		for _, observe := range observers {
			observe(args, result)
		}
		observed := patreonObservedFields(shot)
		entry := map[string]any{"arguments": args, "reason": d.Reason, "observed_fields_before_action": observed, "result": patreonEvidence(result), "elapsed_ms": time.Since(start).Milliseconds()}
		compact := map[string]any{}
		for k, v := range result {
			if k == "error" || k == "error_code" || k == "failed" || k == "text" || k == "current_url" || k == "matched" || k == "timed_out" || k == "checked" || k == "verified" || k == "action_dispatched" || k == "outcome_verified" || k == "draft_save_state" || strings.HasPrefix(k, "text_") || strings.HasPrefix(k, "media_") || strings.HasPrefix(k, "temporal_") {
				compact[k] = patreonEvidence(v)
			}
		}
		history = append(history, map[string]any{"arguments": args, "reason": d.Reason, "observed_fields_before_action": observed, "result": compact})
		writePatreonEvidence(t, fmt.Sprintf("%02d-action.json", step), []byte(mustJSON(entry)))
		t.Logf("RESULT step=%d action=%v elapsed=%s error=%v dispatched=%v verified=%v", step, args["action"], time.Since(start).Round(time.Millisecond), firstNonEmpty(stringValue(result["error_code"]), stringValue(result["error"])), result["action_dispatched"], result["outcome_verified"])
	}
	t.Fatalf("agent exceeded %d actions; inspect decision/state/action artifacts", maxSteps)
	return nil
}

// Keep field evidence when the next screenshot scrolls it out of view, as a
// normal conversation with the complete tool outputs would. IDs/coordinates
// are intentionally excluded: historical observations are not fresh targets.
func patreonObservedFields(shot map[string]any) []any {
	var fields []any
	for _, target := range mapsFromAny(shot["som"]) {
		if target["current_value"] == nil && target["checked"] == nil && !boolFromAny(target["indeterminate"]) {
			continue
		}
		field := map[string]any{}
		for _, key := range []string{"accessible_name", "type", "role", "current_value", "checked", "indeterminate"} {
			if value, ok := target[key]; ok {
				if text, ok := value.(string); ok && len(text) > 512 {
					value = text[:512]
				}
				field[key] = value
			}
		}
		fields = append(fields, field)
		if len(fields) == 20 {
			break
		}
	}
	return fields
}

// Keep semantic evidence while excluding binary images and connection URLs.
func patreonEvidence(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			switch k {
			case "screenshot", "png_b64", "image", "base64", "debug_url", "connect_url", "recording_url":
				continue
			}
			out[k] = patreonEvidence(v)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = patreonEvidence(v)
		}
		return out
	case string:
		if strings.HasPrefix(x, "data:text/html") {
			return "data:text/html,[fixture]"
		}
		return x
	default:
		return v
	}
}

func writePatreonEvidence(t *testing.T, name string, data []byte) {
	t.Helper()
	root := os.Getenv("COMPUTER_LLM_ARTIFACT_DIR")
	if root == "" {
		return
	}
	dir := filepath.Join(root, strings.ReplaceAll(t.Name(), "/", "_"))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}
