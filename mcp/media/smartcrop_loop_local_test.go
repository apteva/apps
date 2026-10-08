package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// Exact production frames/thumbnail pairs and scene references live outside
// the public repo. Assertions use independently reviewed gesture bounds.
func TestLoopNativeAndDirectPoseLocalRegression(t *testing.T) {
	root := os.Getenv("LOOP_CROP_FIXTURE_DIR")
	if root == "" {
		t.Skip("set LOOP_CROP_FIXTURE_DIR")
	}
	manifest, e := os.ReadFile(filepath.Join(root, "manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	var names []string
	json.Unmarshal(manifest, &names)
	h := sha256.New()
	for _, name := range names {
		b, e := os.ReadFile(filepath.Join(root, name))
		if e != nil {
			t.Fatal(e)
		}
		h.Write([]byte(name))
		h.Write(b)
	}
	if got := fmt.Sprintf("%x", h.Sum(nil)); got != "ba4a801b24ddd9b165b03855c1dc1f16a6426d477e75682edbb97457ed8b8970" {
		t.Fatalf("capture changed: %s", got)
	}
	var ds []DerivationRow
	data, _ := os.ReadFile(filepath.Join(root, "all-derivations.json"))
	if json.Unmarshal(data, &ds) != nil {
		t.Fatal("invalid derivations")
	}
	var files []StorageFile
	for _, d := range ds {
		if d.Kind != "keyframe" && d.Kind != "thumbnail" {
			continue
		}
		id, _ := strconv.ParseInt(d.StorageFileID, 10, 64)
		name := d.FileID + ".jpg"
		if d.Kind == "keyframe" {
			name = fmt.Sprintf("%s-%d.jpg", d.FileID, d.PositionMs)
		}
		files = append(files, StorageFile{ID: id, Name: name, Folder: "/.media/" + d.Kind + "/", ContentType: "image/jpeg", Source: "media-derivation"})
	}
	var representative atomic.Bool
	nearby := map[string]bool{}
	for _, d := range ds {
		if d.FileID == "91905" && d.Kind == "keyframe" && absInt64(d.PositionMs-101000) <= 20000 {
			nearby[d.StorageFileID] = true
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/content") {
			parts := strings.Split(r.URL.Path, "/")
			name := parts[len(parts)-2] + ".jpg"
			if representative.Load() && nearby[parts[len(parts)-2]] {
				name = "94491.jpg"
			}
			http.ServeFile(w, r, filepath.Join(root, name))
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
 *.analysis.jpg) cp "$LOOP_CROP_FIXTURE_DIR/tracking/$position.analysis.jpg" "$arg" ;;
 *.detail.jpg) cp "$LOOP_CROP_FIXTURE_DIR/tracking/$position.detail.jpg" "$arg" ;;
 esac
done
`
	if e := os.WriteFile(bin, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithConfig(map[string]string{"smart_crop_engine": "legacy", "ffmpeg_path": bin}))
	sc := &storageClient{base: srv.URL, httpClient: srv.Client()}
	probe := sampleVideoProbe()
	probe.DurationMs = 429498
	if e := upsertMedia(app.AppDB(), testProj, "91905", probe, "source", "", "source.mov"); e != nil {
		t.Fatal(e)
	}
	app.AppDB().Exec(`UPDATE media SET probe_at='2026-10-07T00:00:00Z' WHERE file_id='91905'`)
	for _, f := range []struct {
		id               string
		at               int64
		left, right, top int
	}{{"94482", 101000, 822, 1190, 306}, {"94490", 221000, 786, 1368, 0}} {
		p := sampleImageProbe()
		p.Width, p.Height = 1920, 1080
		if e := upsertMedia(app.AppDB(), testProj, f.id, p, "native-"+f.id, "", f.id+".png"); e != nil {
			t.Fatal(e)
		}
		rid, e := insertRender(app.AppDB(), testProj, "extract_frame", []string{"91905"}, map[string]any{"at_ms": f.at}, "native.png", "", "")
		if e != nil {
			t.Fatal(e)
		}
		app.AppDB().Exec(`UPDATE renders SET status='ok',output_file_id=?,completed_at='2026-10-08T17:00:00Z' WHERE id=?`, f.id, rid)
	}
	for _, d := range ds {
		if d.Kind != "keyframe" && d.Kind != "thumbnail" {
			continue
		}
		id, _ := strconv.ParseInt(d.StorageFileID, 10, 64)
		if e := upsertDerivation(app.AppDB(), testProj, d.FileID, d.Kind, id, d.Width, d.Height, d.PositionMs); e != nil {
			t.Fatal(e)
		}
	}
	ctx := context.WithValue(context.Background(), renderSourcesKey{}, map[string]string{"91905": "fixture"})
	for _, f := range []struct {
		id               string
		at               int64
		left, right, top int
	}{{"94482", 101000, 822, 1224, 306}, {"94490", 221000, 786, 1368, 0}} {
		var wins []cropWindow
		for _, direct := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/direct=%v", f.id, direct), func(t *testing.T) {
				op, fid := "crop", f.id
				p := map[string]any{"crop_mode": "smart", "fit_mode": "crop", "target_ratio": "9:16", "output_width": 540, "require_action_preservation": true}
				if direct {
					op, fid = "extract_frame", "91905"
					p["at_ms"] = f.at
				}
				raw, _ := json.Marshal(p)
				out := preprocessSmartCrop(ctx, app, sc, testProj, op, []string{fid}, raw)
				var resolved struct {
					CropX int            `json:"crop_x"`
					CropY int            `json:"crop_y"`
					CropW int            `json:"crop_w"`
					CropH int            `json:"crop_h"`
					Audit smartCropAudit `json:"crop_diagnostics"`
				}
				json.Unmarshal(out, &resolved)
				win := cropWindow{X: resolved.CropX, Y: resolved.CropY, W: resolved.CropW, H: resolved.CropH}
				wins = append(wins, win)
				t.Logf("crop=%+v coverage=%s extents=%+v", win, resolved.Audit.Coverage, resolved.Audit.Extents)
				if win.X > f.left || win.X+win.W < f.right || win.Y > f.top || win.Y+win.H != 1080 {
					t.Errorf("reviewed head/hands/body/feet clipped: %s", out)
				}
				if resolved.Audit.Coverage != "sampled_extent_fits" || !cropRetainsSampledExtents(&resolved.Audit) {
					t.Errorf("pose not retained: %s", out)
				}
				if _, e := applyCropCompositionPolicy(out); e != nil {
					t.Errorf("strict crop policy rejected supported pose: %v", e)
				}
				if resolved.Audit.SceneSourceID != "91905" || resolved.Audit.SceneAtMs == nil || *resolved.Audit.SceneAtMs != f.at {
					t.Errorf("exact evidence identity lost: %s", out)
				}
				if f.id == "94490" {
					if len(resolved.Audit.Extents) != 1 || resolved.Audit.Extents[0].ForegroundBounds == nil || resolved.Audit.Extents[0].ForegroundBounds.W <= win.W {
						t.Errorf("broad foreground diagnostic lost: %s", out)
					}
				}
				if dir := os.Getenv("LOOP_CROP_PREVIEW_DIR"); dir != "" {
					os.MkdirAll(dir, 0700)
					name := fmt.Sprintf("%s-direct-%v.png", f.id, direct)
					cmd := exec.Command("ffmpeg", "-y", "-v", "error", "-i", filepath.Join(root, f.id+".png"), "-vf", fmt.Sprintf("crop=%d:%d:%d:%d,scale=540:960", win.W, win.H, win.X, win.Y), "-frames:v", "1", filepath.Join(dir, name))
					if b, e := cmd.CombinedOutput(); e != nil {
						t.Fatalf("render: %s %v", b, e)
					}
					os.WriteFile(filepath.Join(dir, name+".json"), out, 0600)
				}
			})
		}
		if len(wins) == 2 && (absInt(wins[0].X-wins[1].X) > 12 || absInt(wins[0].W-wins[1].W) > 36 || absInt(wins[0].Y-wins[1].Y) > 48) {
			t.Errorf("direct/native pose decisions differ: %+v", wins)
		}
	}
	t.Run("representative-storyboard-is-not-exact-pose", func(t *testing.T) {
		// All nearby cached frames show the other pose, including at the exact
		// requested timestamp. A stable storyboard must still sample the source.
		representative.Store(true)
		app.AppDB().Exec(`UPDATE media SET source_sha256='representative-fixture' WHERE file_id='91905'`)
		raw := []byte(`{"crop_mode":"smart","fit_mode":"crop","target_ratio":"9:16","at_ms":101000,"require_action_preservation":true}`)
		out := preprocessSmartCrop(ctx, app, sc, testProj, "extract_frame", []string{"91905"}, raw)
		var p extractFrameParams
		json.Unmarshal(out, &p)
		var a struct {
			D smartCropAudit `json:"crop_diagnostics"`
		}
		json.Unmarshal(out, &a)
		t.Logf("method=%s crop=%d,%d %dx%d", a.D.Method, p.CropX, p.CropY, p.CropW, p.CropH)
		if !strings.HasSuffix(a.D.Method, ":storyboard") || !strings.Contains(a.D.Method, "exact-scene-foreground") {
			t.Fatalf("did not exercise stable representative-frame path: %s", out)
		}
		exact := false
		for _, e := range a.D.Evidence {
			if e.Origin == "source" && e.AtMs == 101000 {
				exact = true
			}
		}
		if !exact || p.CropX > 822 || p.CropX+p.CropW < 1224 || p.CropY > 306 || !cropRetainsSampledExtents(&a.D) {
			t.Fatalf("representative pose displaced requested hand/subject: %s", out)
		}
		if _, e := applyCropCompositionPolicy(out); e != nil {
			t.Fatal(e)
		}
	})

}
