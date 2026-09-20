package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestProcedureSourceHashStable(t *testing.T) {
	first := map[string]any{
		"runtime": "python-3.13-media", "entrypoint": "render.py",
		"files":    map[string]any{"helper.py": "VALUE = 1\n", "render.py": "print('ok')\n"},
		"manifest": map[string]any{"parameters": map[string]any{"type": "object"}},
	}
	second := map[string]any{
		"manifest":   map[string]any{"parameters": map[string]any{"type": "object"}},
		"files":      map[string]any{"render.py": "print('ok')\n", "helper.py": "VALUE = 1\n"},
		"entrypoint": "render.py", "runtime": "python-3.13-media",
	}
	_, _, _, _, _, _, hashA, err := normalizeProcedureSource(first)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _, _, _, hashB, err := normalizeProcedureSource(second)
	if err != nil {
		t.Fatal(err)
	}
	if hashA != hashB {
		t.Fatalf("source hashes differ: %s != %s", hashA, hashB)
	}
	second["files"].(map[string]any)["render.py"] = "print('changed')\n"
	_, _, _, _, _, _, hashC, _ := normalizeProcedureSource(second)
	if hashC == hashA {
		t.Fatal("source edit did not change hash")
	}
}

func TestProcedureSourceRejectsTraversalAndOversize(t *testing.T) {
	_, _, _, _, _, _, _, err := normalizeProcedureSource(map[string]any{
		"runtime": "python-3.13-media", "entrypoint": "../render.py", "files": map[string]any{"../render.py": "pass"},
	})
	if err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("expected traversal rejection, got %v", err)
	}
	_, _, _, _, _, _, _, err = normalizeProcedureSource(map[string]any{
		"runtime": "python-3.13-media", "entrypoint": "render.py", "files": map[string]any{"render.py": strings.Repeat("x", maxProcedureFileBytes+1)},
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size rejection, got %v", err)
	}
}

func TestProcedureRevisionConflictAndReferenceValidation(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a")).WithProject("project-a")
	app := &App{}
	created, err := app.toolProcedureCreate(ctx, map[string]any{
		"name": "Required hero", "runtime": "python-3.13-media", "entrypoint": "render.py", "source": "print('one')",
		"manifest": map[string]any{"inputs": map[string]any{"hero": map[string]any{"kind": "image", "required": true}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	procedureID := created.(map[string]any)["procedure"].(*ProcedureRecord).ID
	if _, err := app.toolProcedureRevisionCreate(ctx, map[string]any{
		"id": procedureID, "expected_revision": 1, "runtime": "python-3.13-media", "entrypoint": "render.py", "source": "print('two')",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolProcedureRevisionCreate(ctx, map[string]any{
		"id": procedureID, "expected_revision": 1, "runtime": "python-3.13-media", "entrypoint": "render.py", "source": "print('stale')",
	}); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected immutable revision conflict, got %v", err)
	}

	edit := `{"timeline":{"tracks":[{"type":"visual","clips":[{"asset":{"type":"procedural","procedure":{"procedure_id":` + fmt.Sprint(procedureID) + `,"revision":1,"output_kind":"video"}},"start":0,"length":1}]}]}}`
	validated, err := app.toolCompositionValidate(ctx, map[string]any{"edit_json": edit})
	if err != nil {
		t.Fatal(err)
	}
	result := validated.(CompositionValidation)
	if result.Valid || len(result.Errors) == 0 || !strings.Contains(result.Errors[0], "required procedure input") {
		t.Fatalf("expected required input validation error, got %+v", result)
	}
}

func TestValidateEditAcceptsProceduralClipAndRejectsBadInputName(t *testing.T) {
	valid := `{"timeline":{"tracks":[{"type":"visual","clips":[{"uid":"coded","asset":{"type":"procedural","procedure":{"procedure_id":7,"revision":2,"output_kind":"video","inputs":{"hero":{"src":"storage:12"}}}},"start":0,"length":3}] }]}}`
	if _, err := parseEditJSON(valid); err != nil {
		t.Fatalf("procedural clip should validate: %v", err)
	}
	invalid := strings.Replace(valid, `"hero"`, `"../hero"`, 1)
	if _, err := parseEditJSON(invalid); err == nil || !strings.Contains(err.Error(), "input name") {
		t.Fatalf("expected input-name rejection, got %v", err)
	}
}

func TestRunProcedureLocalPython(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	if _, err := exec.LookPath(ffprobePath()); err != nil {
		t.Skip("ffprobe not available")
	}
	t.Setenv("COMPOSER_PROCEDURAL_PYTHON_PATH", python)
	ctx := tk.NewAppCtx(t, "apteva.yaml").WithProject("project-a")
	source := `import json, os
out = os.environ["COMPOSER_OUTPUT"]
job = json.load(open(os.environ["COMPOSER_JOB"]))
assert job["parameters"]["title"] == "Hello"
open(os.path.join(out, "main.ppm"), "wb").write(b"P6\n1 1\n255\n\xff\x00\x00")
json.dump({"api_version":"composer/procedural/v1","artifacts":[{"name":"main","kind":"image","path":"main.ppm","duration":2.5}]}, open(os.path.join(out, "result.json"), "w"))
`
	revision := &ProcedureRevisionRecord{ProcedureID: 1, Revision: 1, Runtime: "python-3.13-media", Entrypoint: "render.py", Target: "still", OutputKind: "image", Files: map[string]string{"render.py": source}}
	result, err := runProcedureLocal(context.Background(), ctx, revision, &ProceduralAsset{Parameters: map[string]any{"title": "Hello"}}, defaultOutput(), procedureTimeline{JobID: "test:python", Duration: 2.5, FPS: 30})
	if result.Cleanup != nil {
		defer result.Cleanup()
	}
	if err != nil {
		t.Fatalf("local Python procedure: %v; logs=%s", err, result.Logs)
	}
	data, err := os.ReadFile(result.LocalPath)
	if err != nil || !strings.HasPrefix(string(data), "P6\n1 1\n255\n") || result.DurationSeconds != 2.5 {
		t.Fatalf("unexpected artifact: data=%q duration=%v err=%v", data, result.DurationSeconds, err)
	}
}

func TestRunProcedureLocalBunAndGo(t *testing.T) {
	tests := []struct {
		name       string
		executable string
		runtime    string
		entrypoint string
		source     string
	}{
		{
			name: "bun", executable: "bun", runtime: "bun-1-media", entrypoint: "render.ts",
			source: `const out = process.env.COMPOSER_OUTPUT!;
await Bun.write(out + "/main.ppm", new Uint8Array([80,54,10,49,32,49,10,50,53,53,10,255,0,0]));
await Bun.write(out + "/result.json", JSON.stringify({api_version:"composer/procedural/v1",artifacts:[{name:"main",kind:"image",path:"main.ppm",duration:1}]}));`,
		},
		{
			name: "go", executable: "go", runtime: "go-1.25-media", entrypoint: "main.go",
			source: `package main
import ("os"; "path/filepath")
func main(){ out:=os.Getenv("COMPOSER_OUTPUT"); os.WriteFile(filepath.Join(out,"main.ppm"),[]byte("P6\n1 1\n255\n\xff\x00\x00"),0600); os.WriteFile(filepath.Join(out,"result.json"),[]byte("{\"api_version\":\"composer/procedural/v1\",\"artifacts\":[{\"name\":\"main\",\"kind\":\"image\",\"path\":\"main.ppm\",\"duration\":1}]}"),0600) }`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := exec.LookPath(ffprobePath()); err != nil {
				t.Skip("ffprobe not available")
			}
			binary, err := exec.LookPath(test.executable)
			if err != nil {
				t.Skip(test.executable + " not available")
			}
			switch test.runtime {
			case "bun-1-media":
				t.Setenv("COMPOSER_PROCEDURAL_BUN_PATH", binary)
			case "go-1.25-media":
				t.Setenv("COMPOSER_PROCEDURAL_GO_PATH", binary)
			}
			ctx := tk.NewAppCtx(t, "apteva.yaml").WithProject("project-a")
			revision := &ProcedureRevisionRecord{Runtime: test.runtime, Entrypoint: test.entrypoint, Target: "still", OutputKind: "image", Files: map[string]string{test.entrypoint: test.source}}
			result, err := runProcedureLocal(context.Background(), ctx, revision, &ProceduralAsset{}, defaultOutput(), procedureTimeline{JobID: "test:" + test.name, Duration: 1, FPS: 30})
			if result.Cleanup != nil {
				defer result.Cleanup()
			}
			if err != nil {
				t.Fatalf("local %s procedure: %v; logs=%s", test.name, err, result.Logs)
			}
		})
	}
}

func TestRunProcedureLocalTimeout(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	t.Setenv("COMPOSER_PROCEDURAL_PYTHON_PATH", python)
	t.Setenv("COMPOSER_PROCEDURAL_TIMEOUT_SECONDS", "1")
	ctx := tk.NewAppCtx(t, "apteva.yaml").WithProject("project-a")
	revision := &ProcedureRevisionRecord{Runtime: "python-3.13-media", Entrypoint: "render.py", Target: "clip", OutputKind: "video", Files: map[string]string{"render.py": "import time\ntime.sleep(10)\n"}}
	started := time.Now()
	result, err := runProcedureLocal(context.Background(), ctx, revision, &ProceduralAsset{}, defaultOutput(), procedureTimeline{JobID: "test:timeout", Duration: 1, FPS: 30})
	if result.Cleanup != nil {
		defer result.Cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("expected timeout, got %v", err)
	}
	if time.Since(started) > 4*time.Second {
		t.Fatalf("timeout took too long: %v", time.Since(started))
	}
}

func TestReadProcedureResultRejectsEscapingPath(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "output")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outside.mp4"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := `{"api_version":"composer/procedural/v1","artifacts":[{"name":"main","kind":"video","path":"../outside.mp4"}]}`
	if err := os.WriteFile(filepath.Join(output, "result.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readProcedureResult(output, "main", "video", 100); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("expected path escape rejection, got %v", err)
	}
}

func TestProcedureCacheKeyCoversCodeInputsParametersAndOutput(t *testing.T) {
	revision := &ProcedureRevisionRecord{ProcedureID: 4, Revision: 2, Runtime: "python-3.13-media", Target: "clip", OutputKind: "video", SourceHash: "source-a"}
	binding := &ProceduralAsset{ProcedureID: 4, Revision: 2, OutputKind: "video", Parameters: map[string]any{"color": "blue"}, Inputs: map[string]*ProcedureInput{"hero": {Kind: "image", Src: "storage:10"}}}
	output := Output{Format: "mp4", Resolution: "hd", Aspect: "16:9", FPS: 30}
	clip := Clip{Length: 3}
	base := procedureMaterializationCacheKey(revision, binding, output, clip)
	assertChanges := func(name string, mutate func()) {
		t.Helper()
		mutate()
		if got := procedureMaterializationCacheKey(revision, binding, output, clip); got == base {
			t.Fatalf("%s did not change cache key", name)
		}
	}
	assertChanges("source", func() { revision.SourceHash = "source-b" })
	revision.SourceHash = "source-a"
	assertChanges("input", func() { binding.Inputs["hero"].Src = "storage:11" })
	binding.Inputs["hero"].Src = "storage:10"
	assertChanges("parameters", func() { binding.Parameters["color"] = "red" })
	binding.Parameters["color"] = "blue"
	assertChanges("fps", func() { output.FPS = 60 })
	output.FPS = 30
	assertChanges("resolution", func() { output.Resolution = "fullhd" })
}

type procedureStoragePlatform struct {
	tk.BasePlatformClient
	uploads int
}

func (p *procedureStoragePlatform) CallAppResult(app, tool string, _ map[string]any, out any) error {
	if app != "storage" || tool != "files_upload" {
		return errors.New("unexpected platform call " + app + "/" + tool)
	}
	p.uploads++
	encoded, _ := json.Marshal(map[string]any{"id": int64(700 + p.uploads)})
	return json.Unmarshal(encoded, out)
}

func TestMaterializeProceduralAssetCachesOrdinaryClip(t *testing.T) {
	platform := &procedureStoragePlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform)).WithProject("project-a")
	created, err := (&App{}).toolProcedureCreate(ctx, map[string]any{
		"name": "Counter", "runtime": "python-3.13-media", "entrypoint": "render.py", "source": "pass", "output_kind": "video",
	})
	if err != nil {
		t.Fatal(err)
	}
	procedureID := created.(map[string]any)["procedure"].(*ProcedureRecord).ID
	runs := 0
	app := &App{procedureRunner: func(_ context.Context, _ *sdk.AppCtx, _ *ProcedureRevisionRecord, _ *ProceduralAsset, _ Output, _ procedureTimeline) (procedureRunResult, error) {
		runs++
		dir := t.TempDir()
		path := filepath.Join(dir, "clip.mp4")
		if err := os.WriteFile(path, []byte("video"), 0o600); err != nil {
			return procedureRunResult{}, err
		}
		return procedureRunResult{LocalPath: path, Kind: "video", DurationSeconds: 3}, nil
	}}
	makeEdit := func() *Edit {
		return &Edit{Timeline: Timeline{Tracks: []Track{
			{Type: "visual", Clips: []Clip{
				{UID: "coded", Asset: Asset{Type: "procedural", Procedure: &ProceduralAsset{
					ProcedureID: procedureID,
					Revision:    1,
					OutputKind:  "video",
					Parameters:  map[string]any{"color": "blue"},
				}}, Length: 3},
			}},
		}}}
	}
	output := defaultOutput()
	first := makeEdit()
	changed, err := app.materializeProceduralAssets(context.Background(), ctx, first, output, 10, "project-a")
	if err != nil || !changed {
		t.Fatalf("first materialization changed=%v err=%v", changed, err)
	}
	if got := first.Timeline.Tracks[0].Clips[0].Asset.Src; got != "storage:701" {
		t.Fatalf("materialized src = %q", got)
	}
	second := makeEdit()
	changed, err = app.materializeProceduralAssets(context.Background(), ctx, second, output, 10, "project-a")
	if err != nil || !changed {
		t.Fatalf("cached materialization changed=%v err=%v", changed, err)
	}
	if runs != 1 || platform.uploads != 1 || second.Timeline.Tracks[0].Clips[0].Asset.Type != "video" {
		t.Fatalf("cache miss: runs=%d uploads=%d clip=%+v", runs, platform.uploads, second.Timeline.Tracks[0].Clips[0])
	}
}
