package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestCloudPipelineSiblingPreparationAndAttestedImport(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	capsule := t.TempDir()
	capsule, _ = filepath.EvalSymlinks(capsule)
	app := filepath.Join(capsule, "app")
	sibling := filepath.Join(capsule, "dependency")
	for _, dir := range []string{app, sibling} {
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sibling, "pin"), []byte("pinned"), 0644); err != nil {
		t.Fatal(err)
	}
	dist := t.TempDir()
	envFile := filepath.Join(t.TempDir(), "env")
	p := &commandPipeline{Prepare: []commandStage{{Name: "export", Command: []string{"sh", "-ec", `test "$(cat ../dependency/pin)" = pinned; test "$RECIPE_ENV" = recipe; test "$DEPLOY_SOURCE_DIR" = "$PWD"; mkdir -p generated/native; touch generated/native/project`}, Env: map[string]string{"RECIPE_ENV": "recipe"}, Outputs: []string{"generated/native/project"}}}, BuildDirectory: "generated/native", Outputs: []string{"packages/Example.ipa", "reports/export.json"}, Tests: []commandStage{{Name: "final-artifact", Command: []string{"sh", "-ec", `test -f packages/Example.ipa; test -f "$DEPLOY_SOURCE_DIR/../dependency/pin"; test "$DEPLOY_ARTIFACT_DIR" = "$PWD"`}}}}
	target := mustJSON(map[string]any{"pipeline": p})
	spec := runnerBuildSpec{BuildSubdir: "app", TargetKind: "ios", TargetConfigJSON: target}
	t.Setenv("APTEVA_BUILD_SPEC_B64", base64.StdEncoding.EncodeToString([]byte(mustJSON(spec))))
	t.Setenv("APTEVA_SOURCE_BUILD_SUBDIR", "app")
	args := []string{"--source", capsule, "--artifact", dist}
	if err := runCloudPipeline(append([]string{"prepare"}, append(args, "--env-file", envFile)...)); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "APTEVA_NATIVE_BUILD_DIR="+filepath.Join(app, "generated/native")) || !strings.Contains(string(env), "APTEVA_OUTPUT_PRIMARY=packages/Example.ipa") {
		t.Fatalf("env=%s", env)
	}
	for _, dir := range []string{"packages", "reports"} {
		if err = os.Mkdir(filepath.Join(dist, dir), 0750); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range p.Outputs {
		if err = os.WriteFile(filepath.Join(dist, file), []byte("final signed bytes"), 0640); err != nil {
			t.Fatal(err)
		}
	}
	if err = writeArtifactManifest(dist, artifactManifest{Platform: "ios", Primary: "packages/Example.ipa"}); err != nil {
		t.Fatal(err)
	}
	if err = runCloudPipeline(append([]string{"finalize"}, args...)); err != nil {
		t.Fatal(err)
	}
	if err = runCloudPipeline(append([]string{"verify"}, args...)); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "result.zip")
	if err = zipDirectoryTree(dist, "", archive); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, archive) }))
	defer server.Close()
	imported := t.TempDir()
	deployment, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{Name: "pipeline-import", TargetKind: "ios", SourceKind: "local", SourceRef: app, Framework: "ios"})
	if err != nil {
		t.Fatal(err)
	}
	build, err := dbCreateBuild(ctx.AppDB(), deployment.ID, "ios", "")
	if err != nil {
		t.Fatal(err)
	}
	build.ArtifactPath, build.TargetConfigJSON = imported, target
	if err = (&App{}).downloadAndStageCloudArtifact(nil, &Deployment{TargetKind: "ios"}, build, &cloudArtifact{URL: server.URL, Archive: true, FileName: "packages/Example.ipa"}, "file", imported); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(imported, artifactManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	build.ArtifactManifestJSON = string(body)
	if err = sealBuildArtifact(build); err != nil {
		t.Fatal(err)
	}
	_, evidence, err := verifiedBuildEvidence(build)
	if err != nil || evidence == nil || len(evidence.Tests) != 1 || evidence.Tests[0] != "final-artifact" {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	if _, err = checkReleasePolicy(&Deployment{TargetConfigJSON: target}, build, releaseOptions{}, false); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(imported, "packages/Example.ipa"), []byte("modified"), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err = checkReleasePolicy(&Deployment{TargetConfigJSON: target}, build, releaseOptions{}, false); err == nil {
		t.Fatal("modified artifact released without policy")
	}
	if _, _, err = verifiedBuildEvidence(build); err == nil {
		t.Fatal("modified artifact accepted")
	}
}

func TestCloudPipelineFailuresStopAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name           string
		prepare, tests []commandStage
		want           string
	}{
		{name: "failure", prepare: []commandStage{{Name: "prepare-fail", Command: []string{"sh", "-c", "echo failing-stage-log; exit 7"}}}, want: "prepare-fail failed"},
		{name: "timeout", prepare: []commandStage{{Name: "prepare-timeout", Command: []string{"sh", "-c", "sleep 30"}, TimeoutSeconds: 1}}, want: "prepare-timeout failed"},
		{name: "missing-output", prepare: []commandStage{{Name: "prepare-output", Command: []string{"true"}, Outputs: []string{"missing"}}}, want: "missing output"},
		{name: "test-failure", tests: []commandStage{{Name: "artifact-fail", Command: []string{"false"}}}, want: "artifact-fail failed"},
		{name: "artifact-mutation", tests: []commandStage{{Name: "artifact-mutate", Command: []string{"sh", "-c", "echo changed > Example.ipa"}}}, want: "tests modified release artifacts"},
		{name: "manifest-mutation", tests: []commandStage{{Name: "manifest-mutate", Command: []string{"sh", "-c", "echo '{}' > .apteva-artifact.json"}}}, want: "tests modified release artifact manifest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, dist := t.TempDir(), t.TempDir()
			p := &commandPipeline{Prepare: tc.prepare, Tests: tc.tests, Outputs: []string{"Example.ipa"}}
			if err := os.WriteFile(filepath.Join(dist, "Example.ipa"), []byte("signed"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := writeArtifactManifest(dist, artifactManifest{Platform: "ios", Primary: "Example.ipa"}); err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			_, err := prepareCommandPipeline(context.Background(), p, src, dist, nil, &logs)
			if err == nil {
				err = finalizeCommandPipeline(context.Background(), p, src, dist, nil, &logs)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v logs=%s", err, logs.String())
			}
			if tc.name == "failure" && !strings.Contains(logs.String(), "failing-stage-log") {
				t.Fatal("failing stage logs lost")
			}
			manifest, e := readArtifactManifestFile(filepath.Join(dist, artifactManifestFilename))
			if e == nil && manifest.Pipeline != nil {
				t.Fatal("failure left passing evidence")
			}
		})
	}
}

func TestCloudPipelineEvidenceRejectsMismatchAndMissingOutputs(t *testing.T) {
	dist, src := t.TempDir(), t.TempDir()
	p := &commandPipeline{Outputs: []string{"Example.ipa"}}
	if err := os.WriteFile(filepath.Join(dist, "Example.ipa"), []byte("signed"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeArtifactManifest(dist, artifactManifest{Primary: "Example.ipa"}); err != nil {
		t.Fatal(err)
	}
	if err := finalizeCommandPipeline(context.Background(), p, src, dist, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	original, err := readArtifactManifestFile(filepath.Join(dist, artifactManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*artifactManifest){
		func(m *artifactManifest) { m.Pipeline = nil },
		func(m *artifactManifest) { m.Pipeline.ConfigSHA256 = "wrong" },
		func(m *artifactManifest) { m.Pipeline.Tests = []string{"unrequested"} },
		func(m *artifactManifest) { m.Pipeline.CompletedAt = "" },
		func(m *artifactManifest) { m.Primary = "other.ipa" },
	} {
		body, _ := json.Marshal(original)
		var m artifactManifest
		json.Unmarshal(body, &m)
		mutate(&m)
		if err = validatePipelineEvidence(p, dist, m); err == nil {
			t.Fatal("invalid evidence accepted")
		}
	}
	os.Remove(filepath.Join(dist, "Example.ipa"))
	// Rehash to isolate the declared-output check from the integrity check.
	original.Pipeline.ArtifactSHA256, _ = artifactTreeDigest(dist)
	if err = validatePipelineEvidence(p, dist, original); err == nil {
		t.Fatal("missing output accepted")
	}
}
