package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
	"github.com/apteva/apps/mcp/computer/internal/browser/fileupload"
)

// phaseComp is a backend whose upload stops at a chosen phase.
type phaseComp struct {
	*fakeComp
	phase string
}

func (p *phaseComp) LastUploadPhase() string { return p.phase }

func TestUploadFailureReportsDispatch(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("not really a video"))
	}))
	defer source.Close()
	for _, tc := range []struct {
		name, phase string
		err         error
		want        []string
	}{
		{
			// Run 8830: the browser died while the page's input took the file.
			name: "browser died while assigning", phase: fileupload.PhaseAssigning,
			err:  errors.New("upload_file: websocket: close 1006 (abnormal closure)"),
			want: []string{"session_unhealthy", "upload_phase=assigning", "site_upload_dispatched=unknown", "may already have reached the site", "existing draft", "browser_session"},
		},
		{
			name: "provider staging failed", phase: fileupload.PhaseStaging,
			err:  errors.New("upload_file: browserbase upload: HTTP 500: busy"),
			want: []string{"backend_error", "upload_phase=staging", "site_upload_dispatched=false", "never reached the site"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			comp := &phaseComp{fakeComp: &fakeComp{display: backends.DisplaySize{Width: 1000, Height: 600}, png: []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, url: "https://workspace.example/upload"}, phase: tc.phase}
			fail := func(a backends.Action) error {
				if a.Type == "upload_file" {
					return tc.err
				}
				return nil
			}
			comp.executeHook, comp.executeActionHook = fail, fail
			app := appWithSession("br_up", comp, "browserbase")
			ctx := tk.NewAppCtx(t, "apteva.yaml")
			_, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_up", "action": "upload_file", "selector": "input[type=file]", "source_url": source.URL, "filename": "clip.mp4"})
			if err == nil {
				t.Fatal("want an error")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("missing %q in %v", want, err)
				}
			}
		})
	}
}

func TestUploadSuccessReportsDispatch(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data"))
	}))
	defer source.Close()
	comp := &phaseComp{fakeComp: &fakeComp{display: backends.DisplaySize{Width: 1000, Height: 600}, png: []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, url: "https://workspace.example/upload"}, phase: fileupload.PhaseAssigned}
	app := appWithSession("br_ok", comp, "browserbase")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	out, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_ok", "action": "upload_file", "selector": "input[type=file]", "source_url": source.URL, "filename": "clip.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["site_upload_dispatched"] != true || m["upload_phase"] != fileupload.PhaseAssigned || m["uploaded"] != true {
		t.Fatalf("success fields: %v %v %v", m["site_upload_dispatched"], m["upload_phase"], m["uploaded"])
	}
}

func TestUploadPreCheckFailureHasNoPhase(t *testing.T) {
	comp := &phaseComp{fakeComp: &fakeComp{display: backends.DisplaySize{Width: 1000, Height: 600}, png: []byte{0x89, 0x50, 0x4e, 0x47}, url: "https://workspace.example/upload"}, phase: fileupload.PhaseAssigning}
	app := appWithSession("br_pre", comp, "browserbase")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	// No file source: rejected before the backend runs, so a phase left over
	// from an earlier upload must not appear.
	_, err := app.toolComputerUse(ctx, map[string]any{"session_id": "br_pre", "action": "upload_file", "selector": "input[type=file]"})
	if err == nil || strings.Contains(err.Error(), "upload_phase=") {
		t.Fatalf("pre-check failure: %v", err)
	}
}
