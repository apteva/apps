package main

import (
	"bytes"
	stdBase64 "encoding/base64"

	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestCompositionRevisionPreventsStaleSaveAndRender(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml").WithProject("owner")
	id := auditInsert(t, ctx)
	app := &App{}
	got, err := app.toolCompositionUpdate(ctx, map[string]any{"id": id, "patch": map[string]any{"name": "new", "expected_revision": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got.(map[string]any)["revision"] != int64(2) {
		t.Fatalf("revision: %v", got)
	}
	if _, err = app.toolCompositionUpdate(ctx, map[string]any{"id": id, "patch": map[string]any{"name": "stale", "expected_revision": 1}}); err == nil {
		t.Fatal("stale update succeeded")
	}
	if _, err = app.toolCompositionRender(ctx, map[string]any{"id": id, "wait": false, "expected_revision": 1}); err == nil {
		t.Fatal("stale render succeeded")
	}
	if _, err = app.toolCompositionRender(ctx, map[string]any{"id": id, "wait": false, "expected_revision": 2}); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledRenderCannotResumeOrFail(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml").WithProject("owner")
	id := auditInsert(t, ctx)
	rid, err := createRenderRow(ctx, id, "owner", "auto", auditEdit, auditOutput, "rendering", "rendering")
	if err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithCancel(context.Background())
	defer cancel()
	unregister := registerRenderCancel(rid, cancel)
	defer unregister()
	if _, err := cancelQueuedRender(ctx, rid, "owner"); err != nil {
		t.Fatal(err)
	}
	if c.Err() == nil {
		t.Fatal("active executor not cancelled")
	}
	setRenderProgress(ctx, rid, id, "owner", "rendering", "uploading", 90, nil)
	deferRenderForAI(ctx, rid, id, "owner", []string{"job"})
	failRender(ctx, rid, id, "owner", fmt.Errorf("cancelled encoder"), "")
	var status string
	_ = ctx.AppDB().QueryRow(`SELECT status FROM renders WHERE id=?`, rid).Scan(&status)
	if status != "cancelled" {
		t.Fatal(status)
	}
	if _, err := cancelQueuedRender(ctx, rid, "owner"); err != nil {
		t.Fatal("repeated cancellation", err)
	}
}

func TestExplicitZeroVolumeSurvivesStoredRoundTrip(t *testing.T) {
	for _, value := range []string{"", `,"volume":0`} {
		var clip Clip
		if err := json.Unmarshal([]byte(`{"asset":{"type":"audio","src":"storage:1"},"length":2`+value+`}`), &clip); err != nil {
			t.Fatal(err)
		}
		want := 1.0
		if value != "" {
			want = 0
		}
		raw, _ := json.Marshal(clip)
		var round Clip
		_ = json.Unmarshal(raw, &round)
		if clipVolume(round) != want {
			t.Fatalf("%s: got %g want %g", raw, clipVolume(round), want)
		}
		var soundtrack Soundtrack
		_ = json.Unmarshal([]byte(`{"src":"storage:1"`+value+`}`), &soundtrack)
		if soundtrackVolume(&soundtrack) != want {
			t.Fatalf("soundtrack zero/default changed")
		}
		var audio V2Audio
		_ = json.Unmarshal([]byte(`{"src":"storage:1","duration":2`+value+`}`), &audio)
		track, _, err := v2AudioTrack(&V2Composition{Audio: []V2Audio{{Asset: "audio", Duration: audio.Duration, Volume: audio.Volume}}}, map[string]V2Asset{"audio": {ID: "audio", Type: "audio", Src: "storage:1"}})
		if err != nil {
			t.Fatal(err)
		}
		if clipVolume(track.Clips[0]) != want {
			t.Fatal("V2 conversion volume changed")
		}
	}
}

type chunkUploadPlatform struct {
	tk.BasePlatformClient
	t        *testing.T
	data     bytes.Buffer
	parts    int
	aborted  bool
	failPart bool
}

func (p *chunkUploadPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	if app != "storage" || args["_project_id"] != "owner" {
		p.t.Fatal("unscoped upload", app, args["_project_id"])
	}
	var result any = map[string]any{}
	switch tool {
	case "storage_upload_init":
		result = map[string]any{"upload_id": "upload-test", "part_size": 1 << 20}
	case "storage_upload_part":
		if p.failPart {
			return fmt.Errorf("part transport unavailable")
		}
		part, err := stdBase64.StdEncoding.DecodeString(args["content_base64"].(string))
		if err != nil {
			p.t.Fatal(err)
		}
		if len(part) > 1<<20 {
			p.t.Fatal("unbounded upload chunk")
		}
		p.parts++
		if args["part_number"] != p.parts {
			p.t.Fatal("parts out of order")
		}
		p.data.Write(part)
	case "storage_upload_complete":
		result = map[string]any{"file": map[string]any{"id": 456}}
	case "storage_abort_upload":
		p.aborted = true
	default:
		p.t.Fatal("unexpected tool", tool)
	}
	raw, _ := json.Marshal(result)
	return json.Unmarshal(raw, out)
}
func TestLargeUploadUsesBoundedPartsAndAbortsFailures(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			data := bytes.Repeat([]byte{0x5a}, (2<<20)+37)
			path := filepath.Join(t.TempDir(), "out.mp4")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			platform := &chunkUploadPlatform{t: t, failPart: fail}
			ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform)).WithProject("owner")
			id := saveRenderOutputContext(context.Background(), ctx, path, "mp4", "owner", 1)
			if fail {
				if id != 0 || !platform.aborted {
					t.Fatal("failed upload was not aborted")
				}
				return
			}
			if id != 456 || platform.parts != 3 || !bytes.Equal(data, platform.data.Bytes()) || platform.aborted {
				t.Fatal("upload content or completion mismatch")
			}
		})
	}
}

func TestRemoteChunkUploadHasAuthentication(t *testing.T) {
	script := remoteStorageUploadScriptFragment
	i := strings.Index(script, `--data-binary "@$PART_FILE"`)
	if i < 0 {
		t.Fatal("missing chunk PUT")
	}
	put := strings.LastIndex(script[:i], "-X PUT")
	if put < 0 || !strings.Contains(script[put:i], `Authorization: Bearer $STORAGE_TOKEN`) {
		t.Fatal("chunk PUT authentication missing")
	}

}

var _ sdk.PlatformClient = (*chunkUploadPlatform)(nil)

func TestCacheProjectScopeAndRange(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml").WithProject("owner")
	prior := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = prior })
	id := auditInsert(t, ctx)
	rid, err := createRenderRow(ctx, id, "owner", "local", auditEdit, auditOutput, "complete", "complete")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "db.sqlite"))
	src := filepath.Join(t.TempDir(), "out.mp4")
	_ = os.WriteFile(src, []byte("0123456789"), 0600)
	if err := writeLocalCacheFromPath(rid, src, "mp4"); err != nil {
		t.Fatal(err)
	}
	for _, pid := range []string{"owner", "other"} {
		req := httptest.NewRequest("GET", fmt.Sprintf("/cache/%d?project_id=owner", rid), nil)
		req.Header.Set("X-Apteva-Project-ID", pid)
		req.Header.Set("Range", "bytes=2-5")
		w := httptest.NewRecorder()
		(&App{}).handleCacheGet(w, req)
		if pid == "owner" {
			if w.Code != 206 || w.Body.String() != "2345" {
				t.Fatalf("range: %d %q", w.Code, w.Body.String())
			}
		} else if w.Code != 404 {
			t.Fatal("foreign project cache access", w.Code)
		}
	}
}

func TestBrowserRenderWaitsForHTTPImage(t *testing.T) {
	if _, err := chromeExecutable(); err != nil {
		t.Skip(err)
	}
	if _, err := exec.LookPath(browserNodePath()); err != nil {
		t.Skip(err)
	}
	fixture := image.NewRGBA(image.Rect(0, 0, 64, 64))
	draw.Draw(fixture, fixture.Bounds(), &image.Uniform{color.RGBA{255, 0, 0, 255}}, image.Point{}, draw.Src)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(900 * time.Millisecond)
		w.Header().Set("Content-Type", "image/png")
		_ = png.Encode(w, fixture)
	}))
	defer server.Close()
	spec := auditShapeSpec()
	spec.Output.Renderer = "browser"
	spec.Scenes[0].Duration = .125
	spec.Scenes[0].Elements = []V2Element{{Type: "image", Src: server.URL + "/image.png", Width: 64.0, Height: 64.0}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, _, err := renderV2Browser(ctx, tk.NewAppCtx(t, "apteva.yaml"), spec, "owner")
	if result.Cleanup != nil {
		defer result.Cleanup()
	}
	if err != nil {
		t.Fatal(err)
	}
	pixel, err := exec.Command(ffmpegPath(), "-v", "error", "-i", result.LocalPath, "-vf", "scale=1:1", "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	if len(pixel) < 3 || pixel[0] < 200 || pixel[1] > 40 {
		t.Fatalf("first frame missed delayed HTTP image: %v", pixel)
	}
}
