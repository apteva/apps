package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestAlexaSeatedReelsLocalRegression(t *testing.T) {
	root := os.Getenv("ALEXA_VIDEO_CROP_FIXTURE_DIR")
	if root == "" {
		t.Skip("set ALEXA_VIDEO_CROP_FIXTURE_DIR")
	}
	data, e := os.ReadFile(filepath.Join(root, "derivations.json"))
	if e != nil {
		t.Fatal(e)
	}
	var ds []DerivationRow
	if json.Unmarshal(data, &ds) != nil {
		t.Fatal("invalid manifest")
	}
	h := sha256.New()
	paths, e := filepath.Glob(filepath.Join(root, "tracking", "*.jpg"))
	if e != nil || len(paths) == 0 {
		t.Fatal("missing captured frames")
	}
	for _, p := range paths {
		h.Write([]byte(filepath.Base(p)))
		b, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		h.Write(b)
	}
	if got := fmt.Sprintf("%x", h.Sum(nil)); got != "9d7bdaf8654bca2d33db44b6528484bfeba8768a70d024618fc12568199634a5" {
		t.Fatalf("capture changed: %s", got)
	}
	h = sha256.New()
	h.Write(data)
	for _, d := range ds {
		b, e := os.ReadFile(filepath.Join(root, d.StorageFileID+".jpg"))
		if e != nil {
			t.Fatal(e)
		}
		h.Write(b)
	}
	if got := fmt.Sprintf("%x", h.Sum(nil)); got != "693e4485e80d8e8b0a0d62029e920cfad908980d40ca9d0917e694d4b5bc6291" {
		t.Fatalf("storyboard changed: %s", got)
	}
	var files []StorageFile
	for _, d := range ds {
		id, _ := strconv.ParseInt(d.StorageFileID, 10, 64)
		files = append(files, StorageFile{ID: id, Name: fmt.Sprintf("90959-%d.jpg", d.PositionMs), Folder: "/.media/keyframe/", ContentType: "image/jpeg", Source: "media-derivation"})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/content") {
			p := strings.Split(r.URL.Path, "/")
			http.ServeFile(w, r, filepath.Join(root, p[len(p)-2]+".jpg"))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"files": files})
	}))
	defer srv.Close()
	bin := filepath.Join(t.TempDir(), "fixture-ffmpeg")
	script := `#!/bin/sh
set -eu
position=''
want_seek=0
for arg do
 if [ "$want_seek" = 1 ]; then position=$(printf '%s' "$arg" | tr -d '.'); want_seek=0; fi
 if [ "$arg" = '-ss' ]; then want_seek=1; fi
 case "$arg" in
 *.analysis.jpg) cp "$ALEXA_VIDEO_CROP_FIXTURE_DIR/tracking/$position.analysis.jpg" "$arg" ;;
 *.detail.jpg) cp "$ALEXA_VIDEO_CROP_FIXTURE_DIR/tracking/$position.detail.jpg" "$arg" ;;
 esac
done
`
	if e := os.WriteFile(bin, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithConfig(map[string]string{"smart_crop_engine": "legacy", "ffmpeg_path": bin}))
	p := sampleVideoProbe()
	p.DurationMs = 575155
	if e := upsertMedia(app.AppDB(), testProj, "90959", p, "fc4b65d963003c8ef4f07ec37fc39c1fe3d73f56788965a453228fdc75263500", "", "source.mov"); e != nil {
		t.Fatal(e)
	}
	for _, d := range ds {
		id, _ := strconv.ParseInt(d.StorageFileID, 10, 64)
		if e := upsertDerivation(app.AppDB(), testProj, "90959", "keyframe", id, d.Width, d.Height, d.PositionMs); e != nil {
			t.Fatal(e)
		}
	}
	sc := &storageClient{base: srv.URL, httpClient: srv.Client()}
	for _, f := range []struct {
		id         string
		start, end int64
	}{{"94308", 324575, 355155}, {"94349", 391245, 427660}, {"94398", 436025, 461160}} {
		t.Run(f.id, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), renderSourcesKey{}, map[string]string{"90959": "fixture"})
			raw, _ := json.Marshal(map[string]any{"start_ms": f.start, "end_ms": f.end, "crop_mode": "smart", "target_ratio": "9:16", "output_width": 540})
			out := preprocessSmartCrop(ctx, app, sc, testProj, "extract_reel", []string{"90959"}, raw)
			var p extractReelParams
			if e := json.Unmarshal(out, &p); e != nil {
				t.Fatal(e)
			}
			t.Logf("%s", out)
			if dir := os.Getenv("ALEXA_VIDEO_CROP_OUTPUT_DIR"); dir != "" {
				os.MkdirAll(dir, 0700)
				os.WriteFile(filepath.Join(dir, f.id+".json"), out, 0600)
			}
			if p.CropH >= 1026 || p.CropY+p.CropH != 1080 || len(p.CropPath) != 0 {
				t.Errorf("no stable tighter composition: %+v", p)
			}
			// Independently reviewed source head/arms/legs bounds, not detector outputs.
			left, right, headTop := 870, 1290, 350
			if f.id != "94308" {
				left, right, headTop = 846, 1284, 340
			}
			if p.CropX > left || p.CropX+p.CropW < right || p.CropY > headTop-48 {
				t.Error("reviewed head/body/pose bounds clipped")
			}
			var a struct {
				D smartCropAudit `json:"crop_diagnostics"`
			}
			json.Unmarshal(out, &a)
			if !strings.Contains(a.D.Method, "stable-composition") || !cropRetainsSampledExtents(&a.D) {
				t.Error("composition clips audited evidence or lacks provenance")
			}
		})
	}
}
