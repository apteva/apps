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
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// Replays native screenshots, their actual production thumbnail pixels and the
// existing parent storyboard. Customer pixels remain outside the public repo.
func TestAlexaNativePortraitLocalRegression(t *testing.T) {
	root := os.Getenv("ALEXA_CROP_FIXTURE_DIR")
	if root == "" {
		t.Skip("set ALEXA_CROP_FIXTURE_DIR")
	}
	data, e := os.ReadFile(filepath.Join(root, "derivations.json"))
	if e != nil {
		t.Fatal(e)
	}
	var ds []DerivationRow
	if json.Unmarshal(data, &ds) != nil {
		t.Fatal("invalid fixture derivations")
	}
	fixtures := []struct {
		id, thumb   string
		at          int64
		left, right int
	}{{"94319", "94320", 351000, 900, 1330}, {"94339", "94340", 411000, 912, 1270}, {"94341", "94342", 471000, 750, 1330}, {"94345", "94346", 486000, 915, 1470}, {"94347", "94348", 511000, 880, 1400}}
	var files []StorageFile
	paths := map[string]string{}
	for _, d := range ds {
		id, _ := strconv.ParseInt(d.StorageFileID, 10, 64)
		files = append(files, StorageFile{ID: id, Name: fmt.Sprintf("90959-%d.jpg", d.PositionMs), ContentType: "image/jpeg", Folder: "/.media/keyframe/", Source: "media-derivation"})
		paths[d.StorageFileID] = d.StorageFileID + ".jpg"
	}
	for _, f := range fixtures {
		id, _ := strconv.ParseInt(f.thumb, 10, 64)
		files = append(files, StorageFile{ID: id, Name: f.id + ".jpg", Folder: "/.media/thumbnail/", ContentType: "image/jpeg", Source: "media-derivation"})
		paths[f.thumb] = f.thumb + ".jpg"
	}
	// Hash-pin the capture as one manifest: derivation identities plus exact pixels.
	h := sha256.New()
	h.Write(data)
	for _, f := range fixtures {
		for _, name := range []string{f.id + ".png", f.thumb + ".jpg"} {
			b, e := os.ReadFile(filepath.Join(root, name))
			if e != nil {
				t.Fatal(e)
			}
			h.Write(b)
		}
	}
	for _, d := range ds {
		b, e := os.ReadFile(filepath.Join(root, d.StorageFileID+".jpg"))
		if e != nil {
			t.Fatal(e)
		}
		h.Write(b)
	}
	if got := fmt.Sprintf("%x", h.Sum(nil)); got != "230f11e30e66ccf8000371eb1d6a846eaa640792e3e7f778e33ee6732f85468a" {
		t.Fatalf("fixture capture changed: %s", got)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/content") {
			parts := strings.Split(r.URL.Path, "/")
			name, ok := paths[parts[len(parts)-2]]
			if !ok {
				http.NotFound(w, r)
				return
			}
			http.ServeFile(w, r, filepath.Join(root, name))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"files": files})
	}))
	defer srv.Close()
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	sc := &storageClient{base: srv.URL, httpClient: srv.Client()}
	probe := sampleVideoProbe()
	probe.DurationMs = 575155
	upsertMedia(app.AppDB(), testProj, "90959", probe, "parent", "", "Alexa.mp4")
	app.AppDB().Exec(`UPDATE media SET probe_at='2026-10-07T00:00:00Z' WHERE file_id='90959'`)
	for _, d := range ds {
		id, _ := strconv.ParseInt(d.StorageFileID, 10, 64)
		if e := upsertDerivation(app.AppDB(), testProj, "90959", "keyframe", id, d.Width, d.Height, d.PositionMs); e != nil {
			t.Fatal(e)
		}
	}
	for _, f := range fixtures {
		t.Run(f.id, func(t *testing.T) {
			p := sampleImageProbe()
			p.Width = 1920
			p.Height = 1080
			upsertMedia(app.AppDB(), testProj, f.id, p, "image-"+f.id, "", f.id+".png")
			thumb, _ := strconv.ParseInt(f.thumb, 10, 64)
			upsertDerivation(app.AppDB(), testProj, f.id, "thumbnail", thumb, 320, 0, 0)
			rid, e := insertRender(app.AppDB(), testProj, "extract_frame", []string{"90959"}, map[string]any{"at_ms": f.at}, "native.png", "", "")
			if e != nil {
				t.Fatal(e)
			}
			app.AppDB().Exec(`UPDATE renders SET status='ok',output_file_id=?,completed_at='2026-10-08T10:00:00Z' WHERE id=?`, f.id, rid)
			raw := []byte(`{"target_ratio":"9:16","crop_mode":"smart","fit_mode":"crop","output_width":540}`)
			resolved := preprocessSmartCrop(context.Background(), app, sc, testProj, "crop", []string{f.id}, raw)
			var params cropParams
			json.Unmarshal(resolved, &params)
			t.Logf("crop=%d,%d %dx%d", params.CropX, params.CropY, params.CropW, params.CropH)
			if params.CropX > f.left || params.CropX+params.CropW < f.right {
				t.Fatalf("subject/defining pose clipped: %s", resolved)
			}
			var out struct {
				Audit smartCropAudit `json:"crop_diagnostics"`
			}
			json.Unmarshal(resolved, &out)
			if out.Audit.SceneSourceID != "90959" || out.Audit.SceneAtMs == nil || *out.Audit.SceneAtMs != f.at || !strings.Contains(out.Audit.Method, "native-scene-foreground") {
				t.Fatalf("missing scene provenance: %s", resolved)
			}
			if f.id == "94319" {
				if params.CropH >= 1080 || params.CropY+params.CropH != 1080 || float64(402-params.CropY)/float64(params.CropH) >= 0.25 {
					t.Fatalf("P01 excessive headroom or bottom clipping: %s", resolved)
				}
				if !cropRetainsSampledExtents(&out.Audit) {
					t.Fatalf("composition displaced supported pose: %s", resolved)
				}
			}
			if out.Audit.Coverage == "unknown" {
				t.Fatalf("scene evidence not diagnosed: %s", resolved)
			}
			// Render the actual native frame for human comparison with the production output.
			if dir := os.Getenv("ALEXA_CROP_PREVIEW_DIR"); dir != "" {
				os.MkdirAll(dir, 0700)
				path := filepath.Join(dir, f.id+".png")
				cmd := exec.Command("ffmpeg", "-y", "-v", "error", "-i", filepath.Join(root, f.id+".png"), "-vf", fmt.Sprintf("crop=%d:%d:%d:%d,scale=540:960", params.CropW, params.CropH, params.CropX, params.CropY), "-frames:v", "1", path)
				if b, e := cmd.CombinedOutput(); e != nil {
					t.Fatalf("preview: %s %v", b, e)
				}
			}
			// An identically sized transformed frame must never borrow parent coordinates.
			app.AppDB().Exec(`UPDATE renders SET resolved_params='{"at_ms":351000,"target_ratio":"9:16","output_width":1920}' WHERE id=?`, rid)
			row, _ := getMedia(app.AppDB(), testProj, f.id)
			if nativeSmartCropImageScene(app, testProj, row) != nil {
				t.Fatal("transformed frame accepted as native")
			}
		})
	}
}
