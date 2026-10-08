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

	tk "github.com/apteva/app-sdk/testkit"
)

// Replays the exact production thumbnails for portrait renders 7514–7518.
// Private pixels live outside git. Only Storage transport is substituted;
// submission preprocessing, derivation validation, caches and plans are real.
func TestHollySmartCropPortraitLocalRegression(t *testing.T) {
	root := os.Getenv("HOLLY_PORTRAIT_FIXTURE_DIR")
	if root == "" {
		t.Skip("set HOLLY_PORTRAIT_FIXTURE_DIR")
	}
	hashes := map[string]string{
		"89639.png": "e962fecf54e18cdcb9e9b4b154526f1fa56dde03d0f41faabbf7cf5356d7065c",
		"89659.png": "44aa41cdfd54b2978e72bd65fbe4360c1245348bf0018aeb518932050ba94c35",
		"89661.png": "7ee8f7290af79dcc747f2c9239f41cd473b48f112a0e195941d5fe9b297270bf",
		"89663.png": "9f6a783b8db52cce816236dcafed901630cce921806d363799d9e0547f7829e4",
		"89675.png": "0374ef9a6395d18e831502ccec379c19a8674de2666d1e13bf9f6744f95b3886",
		"89640.jpg": "7b7aa4d65ddc9e7689caf38814d6716b5a33b8f195b87e5ff79dff248c4157b2",
		"89660.jpg": "693f05e85f4969c10610381663ed26a3261e51e9796f7fca76b3f191edbe4e43",
		"89662.jpg": "256d6ce58010a1512076722aa4c9967072dbe0477e10b6cc45dbdd208df3ddde",
		"89664.jpg": "42c430dca1b5a9cfb7c6c3f9d34d90d265520d6957fb524a41d8a7ad5a87ceed",
		"89676.jpg": "087ea3695ac77029f726a4e655c504b25a7481e4be9470fba980b6ed57132699",
	}
	fixtures := []struct {
		source, thumb, output string
		left, right, top      int
		zoom                  bool
	}{
		{"89639", "89640", "89777", 920, 1270, 390, true},
		{"89659", "89660", "89779", 1000, 1330, 390, false},
		{"89661", "89662", "89781", 950, 1295, 402, true},
		{"89663", "89664", "89783", 952, 1245, 444, false},
		{"89675", "89676", "89785", 990, 1290, 432, true},
	}
	baselineX := map[string]int{"89639": 800, "89659": 1014, "89661": 1008, "89663": 1038, "89675": 1008}
	var files []StorageFile
	byID := map[string]string{}
	for _, f := range fixtures {
		for _, name := range []string{f.source + ".png", f.thumb + ".jpg"} {
			hash := hashes[name]
			if hash == "" {
				t.Fatalf("missing hash %s", name)
			}
			assertFileSHA256(t, filepath.Join(root, name), hash)
		}
		id, _ := strconv.ParseInt(f.thumb, 10, 64)
		files = append(files, StorageFile{ID: id, Name: f.source + ".jpg", Folder: "/.media/thumbnail/", ContentType: "image/jpeg", Source: "media-derivation"})
		byID[f.thumb] = f.thumb + ".jpg"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/content") {
			parts := strings.Split(r.URL.Path, "/")
			name, ok := byID[parts[len(parts)-2]]
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
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithConfig(map[string]string{"smart_crop_engine": "legacy"}), tk.WithProjectID(testProj))
	sc := &storageClient{base: srv.URL, httpClient: srv.Client()}
	for _, f := range fixtures {
		t.Run(f.output, func(t *testing.T) {
			p := sampleImageProbe()
			p.Width, p.Height = 1920, 1080
			if err := upsertMedia(app.AppDB(), testProj, f.source, p, "", "/holly/", f.source+".png"); err != nil {
				t.Fatal(err)
			}
			id, _ := strconv.ParseInt(f.thumb, 10, 64)
			if err := upsertDerivation(app.AppDB(), testProj, f.source, "thumbnail", id, 320, 180, 0); err != nil {
				t.Fatal(err)
			}
			request := json.RawMessage(`{"target_ratio":"9:16","crop_mode":"smart","fit_mode":"crop","output_width":540}`)
			resolved := preprocessSmartCrop(context.Background(), app, sc, testProj, "crop", []string{f.source}, request)
			var params cropParams
			if err := json.Unmarshal(resolved, &params); err != nil {
				t.Fatal(err)
			}
			t.Logf("source=%s window=%d,%d %dx%d", f.source, params.CropX, params.CropY, params.CropW, params.CropH)
			if params.CropX > f.left || params.CropX+params.CropW < f.right || params.CropY > f.top || params.CropY+params.CropH < 1080 {
				t.Fatalf("head/body clipped: %s", resolved)
			}
			if f.zoom && (params.CropY < 200 || params.CropH >= 1000) {
				t.Fatalf("excessive ceiling: %s", resolved)
			}
			if !f.zoom && (params.CropW != 606 || params.CropH != 1080) {
				t.Fatalf("wide gesture zoomed: %s", resolved)
			}
			again := preprocessSmartCrop(context.Background(), app, sc, testProj, "crop", []string{f.source}, request)
			if string(again) != string(resolved) {
				t.Fatalf("cache changed result: %s vs %s", resolved, again)
			}
			for _, mode := range []string{"center", "contain"} {
				raw := json.RawMessage(fmt.Sprintf(`{"target_ratio":"9:16","crop_mode":%q,"fit_mode":%q,"output_width":540}`, map[string]string{"center": "center", "contain": "smart"}[mode], map[string]string{"center": "crop", "contain": "contain"}[mode]))
				out := preprocessSmartCrop(context.Background(), app, sc, testProj, "crop", []string{f.source}, raw)
				var other cropParams
				json.Unmarshal(out, &other)
				if mode == "center" && (other.CropW != 606 || other.CropH != 1080 || other.CropY != 0 || other.CropX != 656) {
					t.Fatalf("center mode changed: %s", out)
				}
				if mode == "contain" && other.CropW != 0 {
					t.Fatalf("contain mode acquired a crop: %s", out)
				}
			}
			video := sampleVideoProbe()
			video.HasAudio = false
			if err := upsertMedia(app.AppDB(), testProj, f.source, video, "", "/holly/", f.source+".mov"); err != nil {
				t.Fatal(err)
			}
			videoWin, err := computeSmartCropStillV2(context.Background(), app, sc, testProj, f.source, 9, 16, smartCropTarget{})
			if err != nil || videoWin.W != 606 || videoWin.H != 1080 || videoWin.Y != 0 || videoWin.X != baselineX[f.source] {
				t.Fatalf("video geometry changed: %+v %v", videoWin, err)
			}
			// Restore image metadata for the preview and any subsequent operations.
			if err := upsertMedia(app.AppDB(), testProj, f.source, p, "", "/holly/", f.source+".png"); err != nil {
				t.Fatal(err)
			}

			if out := os.Getenv("HOLLY_PORTRAIT_PREVIEW_DIR"); out != "" {
				if err := os.MkdirAll(out, 0700); err != nil {
					t.Fatal(err)
				}
				plan, err := planCrop([]string{f.source}, resolved, f.output+"-fixed.png", ".png")
				if err != nil {
					t.Fatal(err)
				}
				args := append([]string(nil), plan.Args...)
				for i := range args {
					if args[i] == "{input}" {
						args[i] = filepath.Join(root, f.source+".png")
					}
				}
				args = append(args, filepath.Join(out, plan.Filename))
				if output, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
					t.Fatalf("render preview: %v: %s", err, output)
				}
				if err := os.WriteFile(filepath.Join(out, f.output+"-params.json"), resolved, 0600); err != nil {
					t.Fatal(err)
				}
				probe, err := runProbe(context.Background(), "ffprobe", filepath.Join(out, plan.Filename))
				if err != nil || probe.Width != 540 || probe.Height != 960 {
					t.Fatal(fmt.Sprintf("preview geometry: %+v %v", probe, err))
				}
			}
		})
	}
}
