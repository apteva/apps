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
	"sort"
	"strconv"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// Captured August outputs are replayed through the full planner. The external
// fixture contains exact production storyboards and paired source samples;
// neither customer media nor temporary signed URLs belong in the repository.
func TestAdditionalSmartCropProductionLocalRegression(t *testing.T) {
	root := os.Getenv("MEDIA_ADDITIONAL_CROP_FIXTURE_DIR")
	if root == "" {
		t.Skip("set MEDIA_ADDITIONAL_CROP_FIXTURE_DIR")
	}
	fixtures := []struct {
		id       string
		duration int64
		sha      string
	}{
		{"77371", 844988, "161d1cf65f64322376b9108a8c8b55c5ba50c045f168cc5c67ac0f408dd702e2"},
		{"77480", 692985, "e2ca0f1de10da5ffdade50c51320a023b63ee7ce9de9d201802fcf10ad8a89e9"},
		{"77155", 673693, "162da88b48f5336c6fc87441077e88c8e3bc13d77606361f8924d8267d3f25f7"},
	}

	if os.Getenv("MEDIA_ADDITIONAL_CAPTURE_URLS") == "" {
		for id, want := range map[string]string{"77371": "4cb8eed8afbf5d06328692cd2a28f9db7741b103e69ef164304dc7d58cd71cb9", "77480": "ab1b9e85a00bc81b32f92dc821761887ff19cc5bc980d7ad735ed1c498621857", "77155": "87708675a859dc3f5ad86dae947dc0a97edac6ae0fde9e01cb7dc0725c8e96a0"} {
			h := sha256.New()
			var paths []string
			for _, dir := range []string{"storyboard", "tracking"} {
				ps, e := filepath.Glob(filepath.Join(root, id, dir, "*.jpg"))
				if e != nil {
					t.Fatal(e)
				}
				paths = append(paths, ps...)
			}
			sort.Strings(paths)
			for _, p := range paths {
				rel, e := filepath.Rel(filepath.Join(root, id), p)
				if e != nil {
					t.Fatal(e)
				}
				b, e := os.ReadFile(p)
				if e != nil {
					t.Fatal(e)
				}
				h.Write([]byte(filepath.ToSlash(rel) + "\x00"))
				h.Write(b)
			}
			if got := fmt.Sprintf("%x", h.Sum(nil)); got != want {
				t.Fatalf("captured pixels changed for %s: %s", id, got)
			}
		}
	}
	var files []StorageFile
	byID := map[string]string{}
	derivs := map[string][]DerivationRow{}
	for _, f := range fixtures {
		data, err := os.ReadFile(filepath.Join(root, f.id, "derivations.json"))
		if err != nil {
			t.Fatal(err)
		}
		var ds []DerivationRow
		if err := json.Unmarshal(data, &ds); err != nil {
			t.Fatal(err)
		}
		derivs[f.id] = ds
		for _, d := range ds {
			if d.Kind != "keyframe" {
				continue
			}
			id, _ := strconv.ParseInt(d.StorageFileID, 10, 64)
			files = append(files, StorageFile{ID: id, Name: fmt.Sprintf("%s-%d.jpg", f.id, d.PositionMs), Folder: "/.media/keyframe/", ContentType: "image/jpeg", Source: "media-derivation"})
			byID[d.StorageFileID] = filepath.Join(root, f.id, "storyboard", fmt.Sprintf("%d.jpg", d.PositionMs))
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/content") {
			parts := strings.Split(r.URL.Path, "/")
			p, ok := byID[parts[len(parts)-2]]
			if !ok {
				http.NotFound(w, r)
				return
			}
			http.ServeFile(w, r, p)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"files": files})
	}))
	defer srv.Close()
	binary := filepath.Join(t.TempDir(), "fixture-ffmpeg")
	script := `#!/usr/bin/env python3
import sys,os,shutil,pathlib,json,subprocess
args=sys.argv[1:];fid=args[args.index('-i')+1];pos=round(float(args[args.index('-ss')+1])*1000)
root=pathlib.Path(os.environ['MEDIA_ADDITIONAL_CROP_FIXTURE_DIR'])/fid/'tracking';root.mkdir(exist_ok=True)
outputs={kind: next(a for a in args if a.endswith('.'+kind+'.jpg')) for kind in ('analysis','detail')}
if all((root/(str(pos)+'.'+kind+'.jpg')).exists() for kind in outputs):
 for kind,out in outputs.items():shutil.copyfile(root/(str(pos)+'.'+kind+'.jpg'),out)
else:
 urls=json.loads(os.environ.get('MEDIA_ADDITIONAL_CAPTURE_URLS','{}'))
 if fid not in urls:sys.exit('missing captured source sample '+fid+'/'+str(pos))
 args[args.index('-i')+1]=urls[fid]
 result=subprocess.run(['ffmpeg']+args,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
 if result.returncode:sys.exit('source sample extraction failed, exit='+str(result.returncode))
 for kind,out in outputs.items():shutil.copyfile(out,root/(str(pos)+'.'+kind+'.jpg'))
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	app := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithConfig(map[string]string{"ffmpeg_path": binary}))
	sc := &storageClient{base: srv.URL, httpClient: srv.Client()}
	sources := map[string]string{}
	for _, f := range fixtures {
		p := sampleVideoProbe()
		p.DurationMs = f.duration
		p.Rotation = 90
		if err := upsertMedia(app.AppDB(), testProj, f.id, p, f.sha, "/monika/august_2026/", f.id+".MOV"); err != nil {
			t.Fatal(err)
		}
		for _, d := range derivs[f.id] {
			if d.Kind != "keyframe" {
				continue
			}
			id, _ := strconv.ParseInt(d.StorageFileID, 10, 64)
			if err := upsertDerivation(app.AppDB(), testProj, f.id, d.Kind, id, d.Width, d.Height, d.PositionMs); err != nil {
				t.Fatal(err)
			}
		}
		sources[f.id] = f.id
	}
	ctx := context.WithValue(context.Background(), renderSourcesKey{}, sources)
	cases := []struct {
		name, fid, op  string
		at, start, end int64
		minX, maxX     int
	}{
		{"portrait-78221", "77371", "extract_frame", 528750, 0, 0, 300, 500},
		{"reel-78178", "77480", "extract_reel", 448750, 427975, 468210, 180, 470},
		{"portrait-78327", "77155", "extract_frame", 525000, 0, 0, 720, 820},
		{"dance-78201", "77480", "extract_reel", 538000, 527130, 558560, 0, 1314},
		{"dance-78351", "77155", "extract_reel", 497811, 486855, 513470, 0, 1314},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			params := map[string]any{"target_ratio": "9:16", "crop_mode": "smart", "output_width": 606}
			if c.op == "extract_frame" {
				params["at_ms"] = c.at
			} else {
				params["start_ms"], params["end_ms"], params["output_width"] = c.start, c.end, 1080
			}
			raw, _ := json.Marshal(params)
			resolved := preprocessSmartCrop(ctx, app, sc, testProj, c.op, []string{c.fid}, raw)
			var p extractReelParams
			if err := json.Unmarshal(resolved, &p); err != nil {
				t.Fatal(err)
			}
			x := p.CropX
			if len(p.CropPath) > 0 {
				var ok bool
				x, ok = mariaInterpolatedPathX(p.CropPath, c.at)
				if !ok {
					t.Fatalf("missing crop at %d", c.at)
				}
			}
			var audit struct {
				Diagnostics smartCropAudit `json:"crop_diagnostics"`
			}
			if err := json.Unmarshal(resolved, &audit); err != nil {
				t.Fatal(err)
			}
			if audit.Diagnostics.AppVersion != app.Manifest().Version || audit.Diagnostics.AlgorithmVersion != smartCropAlgorithmVersion || audit.Diagnostics.Effective == nil || len(audit.Diagnostics.Evidence) == 0 {
				t.Fatalf("missing decision provenance: %s", resolved)
			}
			if strings.HasPrefix(c.name, "dance-") && (audit.Diagnostics.Coverage != "exceeds_crop_width" || audit.Diagnostics.Recommendation == "") {
				t.Fatalf("wide action limitation missing: %s", resolved)
			}
			if want := map[string]string{"dance-78201": "01732545fba89f4ca4887cc095cf4b03e0f4a77585bf8b90f8a00a21a4d94a95", "dance-78351": "804e19e9bbde709785d616fc2bbe6d136249de21e729c0971db4f218b5e3b59e"}[c.name]; want != "" {
				path, _ := json.Marshal(p.CropPath)
				if got := fmt.Sprintf("%x", sha256.Sum256(path)); got != want {
					t.Fatalf("diagnostic-only dance path changed: %s", path)
				}
			}

			if c.name == "portrait-78221" {
				exactEvidence := false
				for _, e := range audit.Diagnostics.Evidence {
					if e.Origin == "source" && e.AtMs == c.at {
						exactEvidence = true
					}
				}
				if !exactEvidence {
					t.Fatal("missing exact source frame evidence")
				}
				if (!strings.Contains(audit.Diagnostics.Method, "exact-extent") && !strings.Contains(audit.Diagnostics.Method, "exact-scene-foreground")) || len(audit.Diagnostics.Extents) != 1 || audit.Diagnostics.Extents[0].AtMs != c.at {
					t.Fatalf("requested instant silently fell back to storyboard: %s", resolved)
				}
			}
			if c.name == "reel-78178" {
				for at := c.start; at <= c.end; at += 500 {
					px := p.CropX
					if len(p.CropPath) > 0 {
						px, _ = mariaInterpolatedPathX(p.CropPath, at)
					}
					if px < c.minX || px > c.maxX {
						t.Errorf("reclining head not retained at %d: x=%d", at, px)
					}
				}
			}
			t.Logf("at %d x=%d coverage=%s method=%s", c.at, x, audit.Diagnostics.Coverage, audit.Diagnostics.Method)
			if x < c.minX || x > c.maxX {
				t.Errorf("supported head/limb geometry lost: x=%d want [%d,%d]", x, c.minX, c.maxX)
			}
			if out := os.Getenv("MEDIA_ADDITIONAL_CROP_OUTPUT_DIR"); out != "" {
				if err := os.MkdirAll(out, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(out, c.name+"-params.json"), resolved, 0600); err != nil {
					t.Fatal(err)
				}
				if c.op == "extract_frame" {
					src := filepath.Join(root, c.fid, "exact", fmt.Sprintf("%d.png", c.at))
					dst := filepath.Join(out, c.name+".png")
					args := []string{"-v", "error", "-y", "-i", src, "-vf", fmt.Sprintf("crop=%d:%d:%d:%d,scale=606:1080", p.CropW, p.CropH, x, p.CropY), dst}
					if b, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
						t.Fatalf("preview: %v %s", err, b)
					}
				}
			}
		})
	}
}
