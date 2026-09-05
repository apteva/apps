package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunnerBackendReceivesGenericExecutionRequirements(t *testing.T) {
	const token = "runner-execution-requirements-0123456789"
	t.Setenv(defaultRunnerTokenEnv, token)
	var received runnerJobRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if err := json.NewDecoder(req.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(runnerJobResponse{ID: "job-1", Status: "queued"})
	}))
	defer server.Close()

	cfg := cloudBuildConfig{
		RunnerURL: server.URL, RunnerTokenEnv: defaultRunnerTokenEnv,
		MachineClass:     "performance",
		SoftwareVersions: map[string]string{"xcode": "26.6"},
	}
	d := &Deployment{ProjectID: "p1", Name: "generic", TargetKind: "service", Framework: "blank"}
	build := &Build{ID: 42}
	capsule := &sourceCapsule{
		URL: "https://deploy.example/source.zip", SHA256: "abc", Size: 123, Format: sourceCapsuleFormat,
	}
	if _, err := (runnerBuildBackend{}).Submit(context.Background(), nil, cfg, d, build, capsule); err != nil {
		t.Fatal(err)
	}
	if received.Build.MachineClass != "performance" ||
		received.Build.SoftwareVersions["xcode"] != "26.6" {
		t.Fatalf("build requirements=%+v", received.Build)
	}
}

func TestUnsignedSmokeBuildDoesNotRequireSigningIdentity(t *testing.T) {
	for _, platform := range []string{"android", "ios"} {
		d := &Deployment{TargetKind: platform, Framework: platform, TargetConfigJSON: `{"smoke_only":true}`}
		credentials, err := (&App{}).mobileSigningBuildCredentials(d)
		if err != nil || len(credentials.AndroidSigning) != 0 || len(credentials.IOSSigning) != 0 {
			t.Fatalf("%s smoke credentials: %+v %v", platform, credentials, err)
		}
	}
}
