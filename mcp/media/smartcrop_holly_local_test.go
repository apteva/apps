package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// The external fixture keeps the exact production storyboard JPEGs and paired
// 320px/q3 + 640px/q7 tracking frames out of the public repository. Replaying
// those pixels through the complete planner avoids codec drift and duplicated
// test-only tracking logic. It covers render 7486's first-seconds subject loss.
func TestHollySmartCropOpeningLocalRegression(t *testing.T) {
	root := os.Getenv("HOLLY_SMARTCROP_FIXTURE_DIR")
	if root == "" {
		t.Skip("set HOLLY_SMARTCROP_FIXTURE_DIR")
	}
	data, err := os.ReadFile(filepath.Join(root, "derivations.json"))
	if err != nil {
		t.Fatal(err)
	}
	var derivs []DerivationRow
	if err := json.Unmarshal(data, &derivs); err != nil {
		t.Fatal(err)
	}
	byID := map[string]DerivationRow{}
	files := make([]StorageFile, 0, len(derivs))
	for _, d := range derivs {
		byID[d.StorageFileID] = d
		id, err := strconv.ParseInt(d.StorageFileID, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, StorageFile{ID: id, Name: fmt.Sprintf("85686-%d.jpg", d.PositionMs), Folder: "/.media/keyframe/", ContentType: "image/jpeg", Source: "media-derivation"})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/content") {
			parts := strings.Split(r.URL.Path, "/")
			d, ok := byID[parts[len(parts)-2]]
			if !ok {
				http.NotFound(w, r)
				return
			}
			http.ServeFile(w, r, filepath.Join(root, "storyboard", fmt.Sprintf("%d.jpg", d.PositionMs)))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"files": files})
	}))
	defer srv.Close()
	// Only frame extraction is replaced: all selection, identity checks,
	// evidence passes, path smoothing and boundary handling run production code.
	binary := filepath.Join(t.TempDir(), "fixture-ffmpeg")
	script := `#!/bin/sh
set -eu
position=''
want_seek=0
for arg do
  if [ "$want_seek" = 1 ]; then position=$(printf '%s' "$arg" | tr -d '.'); want_seek=0; fi
  if [ "$arg" = '-ss' ]; then want_seek=1; fi
  case "$arg" in
    *.analysis.jpg) cp "$HOLLY_SMARTCROP_FIXTURE_DIR/tracking/$position.analysis.jpg" "$arg" ;;
    *.detail.jpg) cp "$HOLLY_SMARTCROP_FIXTURE_DIR/tracking/$position.detail.jpg" "$arg" ;;
  esac
done
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithConfig(map[string]string{"smart_crop_engine": "legacy", "ffmpeg_path": binary}))
	p := sampleVideoProbe()
	p.DurationMs = 778941
	if err := upsertMedia(app.AppDB(), testProj, "85686", p, "", "/holly/", "IMG_4899.mov"); err != nil {
		t.Fatal(err)
	}
	for _, d := range derivs {
		id, _ := strconv.ParseInt(d.StorageFileID, 10, 64)
		if err := upsertDerivation(app.AppDB(), testProj, "85686", "keyframe", id, d.Width, d.Height, d.PositionMs); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.WithValue(context.Background(), renderSourcesKey{}, map[string]string{"85686": "fixture"})
	sc := &storageClient{base: srv.URL, httpClient: srv.Client()}
	win, path, err := computeSmartCropReelV2(ctx, app, sc, testProj, "85686", 9, 16, smartCropTarget{StartMs: 480705, EndMs: 520705, PreferKeyframe: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("window=%+v path=%v", win, path)
	if len(path) == 0 {
		path = []cropPathPoint{{AtMs: 480705, X: win.X}, {AtMs: 520705, X: win.X}}
	}
	if out := os.Getenv("HOLLY_SMARTCROP_OUTPUT_PATH"); out != "" {
		b, _ := json.MarshalIndent(path, "", "  ")
		if err := os.WriteFile(out, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for at := int64(480705); at <= 485705; at += 250 {
		x, ok := mariaInterpolatedPathX(path, at)
		if !ok || x < 680 || x > 900 {
			t.Errorf("opening subject lost at %dms: x=%d; want [680,900]", at, x)
		}
	}
	if output := os.Getenv("HOLLY_SMARTCROP_PREVIEW_PATH"); output != "" {
		data, err := os.ReadFile(os.Getenv("HOLLY_SMARTCROP_PREVIEW_SOURCE_URL_FILE"))
		if err != nil {
			t.Fatal(err)
		}
		var source struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(data, &source); err != nil || source.URL == "" {
			t.Fatal("preview source URL is missing")
		}
		params, err := json.Marshal(extractReelParams{StartMs: 480705, EndMs: 520705, TargetRatio: "9:16", OutputWidth: 540, CropW: win.W, CropH: win.H, CropX: win.X, CropY: win.Y, CropPath: path})
		if err != nil {
			t.Fatal(err)
		}
		plan, err := planExtractReel([]string{"85686"}, params, "holly-fixed.mp4")
		if err != nil {
			t.Fatal(err)
		}
		args := append([]string(nil), plan.Args...)
		for i := range args {
			if args[i] == "{input}" {
				args[i] = source.URL
			}
		}
		args = append(args, "-c:v", "libx264", "-preset", "fast", "-crf", "18", output)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput(); err != nil {
			t.Fatalf("preview render: %v: %s", err, strings.ReplaceAll(string(out), source.URL, "[source URL]"))
		}
	}
}
