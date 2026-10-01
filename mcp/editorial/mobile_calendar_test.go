package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestNativeCalendarSurfaceMatchesManifest(t *testing.T) {
	document, e := os.ReadFile("ui/surfaces/editorial-calendar.json")
	if e != nil {
		t.Fatal(e)
	}
	surface, e := sdk.ParseNativeSurface(document)
	if e != nil {
		t.Fatalf("parse native calendar: %v", e)
	}
	if surface.ID != "editorial-calendar" || surface.Presentation != "widget" || surface.Context.Scope != sdk.NativeSurfaceContextProject {
		t.Fatalf("unexpected native calendar identity: %+v", surface)
	}

	manifest := (&App{}).Manifest()
	var component *sdk.UIComponent
	for i := range manifest.Provides.UIComponents {
		if manifest.Provides.UIComponents[i].Name == surface.ID {
			component = &manifest.Provides.UIComponents[i]
			break
		}
	}
	if component == nil || component.Native == nil {
		t.Fatalf("native calendar is not advertised: %+v", manifest.Provides.UIComponents)
	}
	if component.Native.Schema != surface.Schema || component.Native.Entry != "/ui/surfaces/editorial-calendar.json" {
		t.Fatalf("native descriptor mismatch: %+v", component.Native)
	}

	summary := surface.DataSources["summary"]
	wantSettings := map[string]string{
		"date_field":       "$settings.date_field",
		"brand_id":         "$settings.brand_id",
		"include_releases": "$settings.show_releases",
		"horizon_days":     "$settings.horizon_days",
	}
	for key, want := range wantSettings {
		if got := summary.Request.Query[key]; got != want {
			t.Fatalf("summary query %s=%v want %s", key, got, want)
		}
	}
	if summary.Request.Path != "/mobile/calendar-summary" || summary.Response.Item != "$" {
		t.Fatalf("unexpected summary source: %+v", summary)
	}
	if len(surface.Blocks) != 1 || surface.Blocks[0].Type != "list" || surface.Blocks[0].Empty == nil || surface.Blocks[0].Empty.Title != "Nothing scheduled" {
		t.Fatalf("native agenda is incomplete: %+v", surface.Blocks)
	}
	registered := false
	for _, route := range (&App{}).HTTPRoutes() {
		if route.Pattern == "/mobile/calendar-summary" && route.Method == http.MethodGet {
			registered = true
		}
	}
	if !registered {
		t.Fatal("native calendar summary route is not registered")
	}
}

func requestMobileCalendar(t *testing.T, a *App, target, headerProject string) (int, mobileCalendarSummary, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if headerProject != "" {
		request.Header.Set("X-Apteva-Project-ID", headerProject)
	}
	recorder := httptest.NewRecorder()
	a.mobileCalendarSummary(recorder, request)
	var summary mobileCalendarSummary
	if recorder.Code == http.StatusOK {
		if e := json.Unmarshal(recorder.Body.Bytes(), &summary); e != nil {
			t.Fatal(e)
		}
	}
	return recorder.Code, summary, recorder.Body.String()
}

func TestMobileCalendarSummaryIsNativeReadyAndProjectScoped(t *testing.T) {
	a, c := testApp(t, nil)
	a.now = func() time.Time { return time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC) }
	s, e := getSettings(c.AppDB(), "alpha")
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.dispatch(c, "settings_update", map[string]any{
		"revision": s.Revision, "statuses": s.Statuses, "formats": s.Formats, "channels": s.Channels,
		"timezone": "Europe/Madrid", "due_time": "09:30",
		"brands": []Brand{{ID: "apteva", Name: "Apteva", Color: "#123456"}, {ID: "other", Name: "Other"}},
	})
	if e != nil {
		t.Fatal(e)
	}
	article := create(t, a, c, map[string]any{
		"title": "September campaign", "brand_id": "apteva", "format": "article", "planned_at": "2026-09-24", "deadline": "2026-09-22",
	})
	if _, e = a.dispatch(c, "releases_create", map[string]any{
		"item_id": article.ID, "channel": "Instagram", "planned_at": "2026-09-23T10:00:00Z",
	}); e != nil {
		t.Fatal(e)
	}
	create(t, a, c, map[string]any{"title": "Other brand", "brand_id": "other", "planned_at": "2026-09-23"})
	create(t, a, c, map[string]any{"title": "Outside horizon", "brand_id": "apteva", "planned_at": "2026-09-30"})
	create(t, a, c.WithProject("beta"), map[string]any{"title": "Secret beta", "planned_at": "2026-09-23"})

	code, summary, body := requestMobileCalendar(t, a,
		"/mobile/calendar-summary?horizon_days=7&limit=20&brand_id=apteva&include_releases=true&project_id=beta", "beta")
	if code != http.StatusOK {
		t.Fatalf("summary status=%d body=%s", code, body)
	}
	if summary.Events == nil || len(summary.Events) != 2 || summary.Truncated {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if got := summary.Events[0]; got.ID == "" || got.Title != "September campaign" || got.Detail != "Instagram · Planned" || got.BrandName != "Apteva" || got.KindLabel != "Release" || got.At != "2026-09-23T10:00:00Z" {
		t.Fatalf("release is not native-ready: %+v", got)
	}
	if got := summary.Events[1]; got.ID != "item:"+strconv.FormatInt(article.ID, 10) || got.At != "2026-09-24T09:30:00+02:00" || got.Detail != "Article · Idea" || got.KindLabel != "Content" {
		t.Fatalf("content is not native-ready: %+v", got)
	}
	for _, event := range summary.Events {
		if strings.Contains(event.Title, "Secret") || strings.Contains(event.Title, "Other brand") || strings.Contains(event.Title, "Outside") {
			t.Fatalf("project or brand filter leaked: %+v", summary.Events)
		}
	}
	code, summary, body = requestMobileCalendar(t, a,
		"/mobile/calendar-summary?horizon_days=7&limit=20&brand_id=apteva&date_field=deadline&include_releases=true", "")
	if code != http.StatusOK || len(summary.Events) != 1 || summary.Events[0].ID != "item:"+strconv.FormatInt(article.ID, 10) || summary.Events[0].At != "2026-09-22T09:30:00+02:00" {
		t.Fatalf("deadline filter was not applied: status=%d summary=%+v body=%s", code, summary, body)
	}

	code, summary, body = requestMobileCalendar(t, a,
		"/mobile/calendar-summary?horizon_days=7&limit=1&brand_id=apteva&include_releases=true", "")
	if code != http.StatusOK || len(summary.Events) != 1 || !summary.Truncated {
		t.Fatalf("limit was not applied: status=%d summary=%+v body=%s", code, summary, body)
	}

	code, summary, body = requestMobileCalendar(t, a,
		"/mobile/calendar-summary?horizon_days=7&brand_id=missing&include_releases=false", "")
	if code != http.StatusOK || summary.Events == nil || len(summary.Events) != 0 || !strings.Contains(body, `"events":[]`) {
		t.Fatalf("empty summary must return an array: status=%d summary=%+v body=%s", code, summary, body)
	}
}

func TestMobileCalendarSummaryRequiresInjectedProjectAndClampsSettings(t *testing.T) {
	a, c := testApp(t, nil)
	a.ctx = c.WithProject("")
	a.now = func() time.Time { return time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC) }

	code, _, _ := requestMobileCalendar(t, a, "/mobile/calendar-summary?project_id=alpha", "")
	if code != http.StatusBadRequest {
		t.Fatalf("query project context authorized summary: %d", code)
	}
	code, summary, body := requestMobileCalendar(t, a, "/mobile/calendar-summary?horizon_days=999&limit=999", "alpha")
	if code != http.StatusOK || summary.Events == nil {
		t.Fatalf("injected project failed: status=%d summary=%+v body=%s", code, summary, body)
	}
	if got := boundedMobileInt("999", 30, 1, 365); got != 365 {
		t.Fatalf("horizon clamp=%d", got)
	}
	if got := boundedMobileInt("0", 12, 1, 20); got != 1 {
		t.Fatalf("limit clamp=%d", got)
	}
	code, _, body = requestMobileCalendar(t, a, "/mobile/calendar-summary?include_releases=perhaps", "alpha")
	if code != http.StatusBadRequest || !strings.Contains(body, "include_releases must be true or false") {
		t.Fatalf("invalid release filter status=%d body=%s", code, body)
	}
}
