package main

import (
	"context"
	"encoding/json"
	"errors"
	tk "github.com/apteva/app-sdk/testkit"
	"image/png"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCropAuditEvidenceAndDecisionCache(t *testing.T) {
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/content") {
			downloads.Add(1)
			_ = png.Encode(w, portraitCompositionFixture(160, false))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": []StorageFile{{ID: 2, Name: "1.png", Folder: "/.media/thumbnail/", ContentType: "image/png", Source: "media-derivation"}}})
	}))
	defer srv.Close()
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithConfig(map[string]string{"smart_crop_engine": "legacy"}), tk.WithProjectID(testProj))
	sc := &storageClient{base: srv.URL, httpClient: srv.Client()}
	p := sampleImageProbe()
	p.Width, p.Height = 1920, 1080
	if e := upsertMedia(app.AppDB(), testProj, "1", p, strings.Repeat("a", 64), "/", "1.png"); e != nil {
		t.Fatal(e)
	}
	if e := upsertDerivation(app.AppDB(), testProj, "1", "thumbnail", 2, 320, 180, 0); e != nil {
		t.Fatal(e)
	}
	raw := []byte(`{"target_ratio":"9:16","output_width":540}`)
	out := preprocessSmartCrop(context.Background(), app, sc, testProj, "crop", []string{"1"}, raw)
	var resolved struct {
		Diagnostics smartCropAudit `json:"crop_diagnostics"`
	}
	if e := json.Unmarshal(out, &resolved); e != nil {
		t.Fatal(e)
	}
	a := &resolved.Diagnostics
	if a.AppVersion != app.Manifest().Version || a.AlgorithmVersion != legacySmartCropAlgorithmVersion || a.Effective == nil || len(a.Evidence) != 1 || a.Evidence[0].FileID != "2" {
		t.Fatalf("missing provenance: %s", out)
	}
	for i := 0; i < 2; i++ {
		again := preprocessSmartCrop(context.Background(), app, sc, testProj, "crop", []string{"1"}, raw)
		var first, second any
		_ = json.Unmarshal(out, &first)
		_ = json.Unmarshal(again, &second)
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("cache lost provenance: %s", again)
		}
	}
	if downloads.Load() != 1 {
		t.Fatalf("cache reanalysed pixels %d times", downloads.Load())
	}
	previous := globalCtx
	globalCtx = app
	t.Cleanup(func() { globalCtx = previous })
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "test")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/smartcrop?project_id="+testProj, strings.NewReader(`{"file_id":"1","operation":"crop","target_ratio":"9:16"}`))
	(&App{}).handleSmartCropPreview(w, r)
	var preview struct {
		Crop struct {
			W int `json:"crop_w"`
			H int `json:"crop_h"`
			X int `json:"crop_x"`
			Y int `json:"crop_y"`
		} `json:"crop"`
		Diagnostics smartCropAudit `json:"crop_diagnostics"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &preview); e != nil || w.Code != 200 {
		t.Fatalf("preview failed: %d %s", w.Code, w.Body.String())
	}
	if preview.Crop.W != a.Effective.W || preview.Crop.H != a.Effective.H || preview.Crop.X != a.Effective.X || preview.Crop.Y != a.Effective.Y || preview.Diagnostics.AlgorithmVersion != a.AlgorithmVersion {
		t.Fatalf("preview does not match rendering: %s vs %s", w.Body.String(), out)
	}
	if out := preprocessSmartCrop(context.Background(), app, sc, testProj, "crop", []string{"1"}, []byte(`{"target_ratio":"9:16","fit_mode":"contain"}`)); strings.Contains(string(out), "crop_diagnostics") {
		t.Fatalf("contain acquired invented crop decision: %s", out)
	}
}
func TestCropAuditConcurrentEvidenceAndFallback(t *testing.T) {
	a := &smartCropAudit{AppVersion: "test", AlgorithmVersion: smartCropAlgorithmVersion, Coverage: "unknown"}
	ctx := context.WithValue(context.Background(), smartCropAuditKey{}, a)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recordSmartCropEvidence(ctx, "source", 42, "")
			recordSmartCropFallback(ctx, "source_sampling_unavailable")
		}()
	}
	wg.Wait()
	out := attachSmartCropAudit([]byte(`{"crop_w":606,"crop_h":1080,"crop_x":400,"crop_path":[{"at_ms":0,"x":400},{"at_ms":1000,"x":420}]}`), a)
	if len(a.Evidence) != 1 || len(a.Fallbacks) != 1 || len(a.Path) != 2 || a.Effective.X != 400 {
		t.Fatalf("incomplete audit: %s", out)
	}
}

func TestCropAuditCenterFallback(t *testing.T) {
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithConfig(map[string]string{"smart_crop_engine": "legacy"}), tk.WithProjectID(testProj))
	p := sampleImageProbe()
	p.Width, p.Height = 1920, 1080
	if e := upsertMedia(app.AppDB(), testProj, "1", p, "", "/", "1.png"); e != nil {
		t.Fatal(e)
	}
	out := preprocessSmartCrop(context.Background(), app, &storageClient{}, testProj, "crop", []string{"1"}, []byte(`{"target_ratio":"9:16"}`))
	var resolved struct {
		Diagnostics smartCropAudit `json:"crop_diagnostics"`
	}
	if e := json.Unmarshal(out, &resolved); e != nil {
		t.Fatal(e)
	}
	a := &resolved.Diagnostics
	if a.Method != "v1:center-fallback" || a.Effective == nil || len(a.Fallbacks) < 2 {
		t.Fatalf("fallback has no traceable reason: %s", out)
	}
	if smartCropFailureReason(errors.New("sample source: https://private.invalid/?token=secret")) != "source_sampling_unavailable" {
		t.Fatal("unstructured source failure")
	}
}
