package main

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type mobileCloudPlatform struct {
	tk.BasePlatformClient
	connections     map[int64]string
	responses       map[string]string
	calls           []integrationCall
	connectionCalls []int64
}

func (p *mobileCloudPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	bindings := []any{}
	for id := range p.connections {
		bindings = append(bindings, id)
	}
	return &sdk.InstallIdentity{InstallID: 42, PublicURL: "https://deploy.test", Bindings: map[string]any{"cloud_build": map[string]any{"ids": bindings}}}, nil
}
func (p *mobileCloudPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: p.connections[id], Status: "active"}, nil
}
func (p *mobileCloudPlatform) ExecuteIntegrationTool(id int64, name string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calls = append(p.calls, integrationCall{Tool: name, Input: input})
	p.connectionCalls = append(p.connectionCalls, id)
	raw, ok := p.responses[name]
	if !ok {
		return nil, errors.New("unexpected provider tool: " + name)
	}
	return &sdk.ExecuteResult{Success: true, Status: http.StatusOK, Data: json.RawMessage(raw)}, nil
}

func TestCloudProviderSelectsBoundAccountAndRejectsAmbiguity(t *testing.T) {
	p := &mobileCloudPlatform{connections: map[int64]string{71: "bitrise", 72: "bitrise", 73: "appcircle"}}
	withCloudBuildContext(t, p)
	if _, err := cloudIntegrationFor(buildBackendBitrise); err == nil {
		t.Fatal("ambiguous account was selected")
	}
	bound, err := cloudIntegrationForConfig(buildBackendBitrise, cloudBuildConfig{ConnectionID: 72})
	if err != nil || bound.ConnectionID != 72 {
		t.Fatalf("bound=%+v err=%v", bound, err)
	}
	for _, id := range []int64{73, 99} {
		if _, err := cloudIntegrationForConfig(buildBackendBitrise, cloudBuildConfig{ConnectionID: id}); err == nil {
			t.Fatalf("unbound/wrong provider account %d accepted", id)
		}
	}
	if _, err := cloudIntegrationFor(buildBackendAppcircle); err != nil {
		t.Fatal(err)
	}
}

func TestMobileProviderSubmissionUsesSharedContractAndExactJobIdentity(t *testing.T) {
	for _, provider := range []string{buildBackendBitrise, buildBackendAppcircle} {
		t.Run(provider, func(t *testing.T) {
			p := &mobileCloudPlatform{connections: map[int64]string{77: provider}, responses: map[string]string{"trigger_build": `{"build_slug":"build-1","application":{"slug":"wrong-app"}}`, "start_build_with_environment": `{"taskId":"task-1","buildId":"build-1"}`}}
			withCloudBuildContext(t, p)
			bound, _ := cloudIntegrationFor(provider)
			backend, _ := cloudBackendFor(provider)
			cfg := cloudBuildConfig{AppID: "adapter", ProfileID: "profile", ConfigurationID: "config", CommitID: "commit", WorkflowID: "native", Branch: "main", SourceMode: "bundle", ArtifactName: "apteva-build", Variables: map[string]string{"APTEVA_BUILD_ID": "wrong", "LITERAL": "$HOME"}}
			d := &Deployment{ID: 1, ProjectID: "p1", Name: "recipe", Framework: "go", TargetKind: "service", TargetConfigJSON: `{"pipeline":{"prepare":[{"name":"export","command":["sh","-ec","echo recipe"]}]}}`}
			job, err := backend.Submit(t.Context(), bound, cfg, d, &Build{ID: 42}, &sourceCapsule{URL: "https://deploy.test/source", SHA256: "source-hash", Size: 12, Format: "zip-v1"})
			if err != nil {
				t.Fatal(err)
			}
			var values map[string]string
			if provider == buildBackendBitrise {
				if job.ID != "build-1" {
					t.Fatal(job.ID)
				}
				params := p.calls[0].Input["build_params"].(map[string]any)
				values = map[string]string{}
				for _, item := range params["environments"].([]map[string]any) {
					if item["is_expand"] != false {
						t.Fatal("shell expansion enabled")
					}
					values[item["key"].(string)] = item["value"].(string)
				}
			} else {
				identity, err := parseAppcircleJob(job.ID)
				if err != nil || identity.TaskID != "task-1" || identity.CommitID != "commit" {
					t.Fatalf("identity=%+v err=%v", identity, err)
				}
				values = p.calls[0].Input["environment"].(map[string]string)
			}
			if values["APTEVA_BUILD_ID"] != "42" || values["LITERAL"] != "$HOME" || values["APTEVA_SOURCE_SHA256"] != "source-hash" {
				t.Fatal(values)
			}
			decoded, _ := base64.StdEncoding.DecodeString(values["APTEVA_BUILD_SPEC_B64"])
			if !strings.Contains(string(decoded), "pipeline") {
				t.Fatal("recipe missing from build spec")
			}
		})
	}
}

func TestMobileProviderStatusesNeverTreatAbortedOrUnknownAsSuccess(t *testing.T) {
	for _, item := range []struct{ provider, raw, want string }{
		{"bitrise", `{"data":{"status":0}}`, "running"}, {"bitrise", `{"data":{"status":1}}`, "succeeded"}, {"bitrise", `{"data":{"status":2}}`, "failed"}, {"bitrise", `{"data":{"status":3}}`, "cancelled"}, {"bitrise", `{"data":{"status":4}}`, "cancelled"},
		{"appcircle", `0`, "succeeded"}, {"appcircle", `{"status":90}`, "queued"}, {"appcircle", `{"status":91}`, "running"}, {"appcircle", `{"status":92}`, "finishing"}, {"appcircle", `{"status":3}`, "timeout"},
	} {
		got, err := providerStatus(item.provider, json.RawMessage(item.raw))
		if err != nil || got != item.want {
			t.Fatalf("%s %s -> %s %v", item.provider, item.raw, got, err)
		}
	}
	for _, provider := range []string{"bitrise", "appcircle"} {
		for _, raw := range []string{`{}`, `null`, `{"status":123}`, `{"status":"unexpected"}`} {
			if _, err := providerStatus(provider, json.RawMessage(raw)); err == nil {
				t.Fatalf("invalid status accepted: %s", raw)
			}
		}
	}
}

func TestAppcircleKeepsResolvedBuildIdentityWithoutQueueDependency(t *testing.T) {
	p := &mobileCloudPlatform{connections: map[int64]string{77: "appcircle"}, responses: map[string]string{"get_build_queue_item": `{"buildId":"built","status":91}`, "get_build_status": `{"status":0}`, "cancel_build": `{}`}}
	withCloudBuildContext(t, p)
	bound, _ := cloudIntegrationFor("appcircle")
	build := &Build{ExternalJobID: mustJSON(appcircleJob{TaskID: "task", CommitID: "commit", BuildID: "built"})}
	// Already-resolved jobs remain inspectable after queue entries disappear.
	status, err := (appcircleBuildBackend{}).Inspect(context.Background(), bound, cloudBuildConfig{}, build)
	if err != nil || status.Status != "succeeded" || p.calls[0].Tool != "get_build_status" {
		t.Fatalf("status=%+v err=%v calls=%+v", status, err, p.calls)
	}
	if err := (appcircleBuildBackend{}).Cancel(t.Context(), bound, cloudBuildConfig{}, build); err != nil {
		t.Fatal(err)
	}
	if p.calls[1].Input["taskId"] != "task" {
		t.Fatal(p.calls[1])
	}
}

func TestBitriseArtifactUsesNamedArchiveAndFreshDownloadURL(t *testing.T) {
	p := &mobileCloudPlatform{connections: map[int64]string{77: "bitrise"}, responses: map[string]string{"list_build_artifacts": `{"data":[{"slug":"ipa","title":"Moonhorde.ipa"},{"slug":"sealed","title":"apteva-build.zip"}]}`, "get_build_artifact": `{"data":{"expiring_download_url":"https://signed.test/sealed.zip"}}`}}
	withCloudBuildContext(t, p)
	bound, _ := cloudIntegrationFor("bitrise")
	artifact, err := (bitriseBuildBackend{}).Artifact(t.Context(), bound, cloudBuildConfig{AppID: "app", ArtifactName: "apteva-build"}, &Build{ExternalJobID: "job"}, nil)
	if err != nil || artifact.URL != "https://signed.test/sealed.zip" || !artifact.Archive || len(artifact.Headers) != 0 {
		t.Fatalf("artifact=%+v err=%v", artifact, err)
	}
	if p.calls[1].Input["artifact_slug"] != "sealed" {
		t.Fatal(p.calls)
	}
}

func TestAppcircleSigningPreservesUnrelatedConfigurationAndMasksSecrets(t *testing.T) {
	p := &mobileCloudPlatform{connections: map[int64]string{77: "appcircle"}, responses: map[string]string{"list_build_variable_groups": `[]`, "create_build_variable_group": `{"id":"managed"}`, "get_build_configuration": `{"configurationId":"config","environmentVariables":["unrelated"],"platformSetting":{"xcodeVersion":"26"},"autoPublish":false}`, "update_build_configuration": `{}`}}
	withCloudBuildContext(t, p)
	bound, _ := cloudIntegrationFor("appcircle")
	result, err := (appcircleSigningProvider{}).ProvisionSigningSecrets(t.Context(), bound, cloudBuildConfig{ProfileID: "profile", ConfigurationID: "config"}, &Deployment{ID: 1, EnvironmentID: 2}, mobileSigningSecrets{Platform: "android", AndroidKeystoreBase64: "private-key", AndroidStorePassword: "password"})
	if err != nil || result.SecretRef != "managed" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, v := range p.calls[1].Input["variables"].([]map[string]any) {
		if v["isSecret"] != true {
			t.Fatal("signing variable is not secret")
		}
	}
	update := p.calls[3].Input["configuration"].(map[string]any)
	if mustJSON(update["environmentVariables"]) != `["unrelated","managed"]` || update["platformSetting"] == nil {
		t.Fatal(update)
	}
	if strings.Contains(result.ConfigJSON, "password") || strings.Contains(result.ConfigJSON, "private-key") {
		t.Fatal("secret persisted in provider metadata")
	}
}

func TestProviderContainerRequiresExactlyOneSealedArchive(t *testing.T) {
	for _, names := range [][]string{{"artifacts/apteva-build.zip", "logs/log.txt"}, {"logs/log.txt"}, {"a/apteva-build.zip", "b/apteva-build.zip"}} {
		file := filepath.Join(t.TempDir(), "provider.zip")
		out, _ := os.Create(file)
		z := zip.NewWriter(out)
		for _, name := range names {
			w, _ := z.Create(name)
			_, _ = w.Write([]byte("sealed-bytes"))
		}
		_ = z.Close()
		_ = out.Close()
		inner, err := extractProviderArtifactArchive(file, "apteva-build.zip")
		if len(names) == 2 && names[0] == "artifacts/apteva-build.zip" {
			if err != nil {
				t.Fatal(err)
			}
			body, _ := os.ReadFile(inner)
			if string(body) != "sealed-bytes" {
				t.Fatal(string(body))
			}
		} else if err == nil {
			t.Fatal("missing or ambiguous artifact accepted")
		}
	}
}

func TestAppcircleQueueUsesItsOwnStatusAndRejectsMissingBuildEvidence(t *testing.T) {
	for code, want := range map[int]string{0: "queued", 1: "running", 2: "finishing", 91: "cancelled", 92: "failed"} {
		got, err := appcircleQueueStatus(json.RawMessage(mustJSON(map[string]int{"queueItemStatus": code})))
		if err != nil || got != want {
			t.Fatalf("%d -> %s %v", code, got, err)
		}
	}
	for _, raw := range []string{`{}`, `{"queueItemStatus":null}`, `{"queueItemStatus":90}`, `{"queueItemStatus":99}`} {
		if _, err := appcircleQueueStatus(json.RawMessage(raw)); err == nil {
			t.Fatal(raw)
		}
	}
}
func TestManagedSigningCannotBeOverriddenByPublicProviderVariables(t *testing.T) {
	for _, provider := range []string{"bitrise", "appcircle", "codemagic"} {
		_, err := parseCloudBuildConfig(provider, `{"variables":{"CERTIFICATE_PRIVATE_KEY":"override"}}`)
		if err == nil || !strings.Contains(err.Error(), "managed signing") {
			t.Fatal(provider, err)
		}
	}
}

func TestMobileCloudBackendSetupPersistsSelectedProviderAccount(t *testing.T) {
	for _, provider := range []string{"bitrise", "appcircle"} {
		t.Run(provider, func(t *testing.T) {
			p := &mobileCloudPlatform{connections: map[int64]string{77: provider}, responses: map[string]string{"get_app": `{}`, "get_build_configuration": `{}`}}
			ctx := withCloudBuildContext(t, p)
			d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{Name: "mobile", TargetKind: "ios", SourceKind: "code", SourceRef: "source", Framework: "ios", BuildBackend: "local", TargetConfigJSON: `{"bundle_id":"com.example","pipeline":{"outputs":["Named.ipa"]}}`})
			if err != nil {
				t.Fatal(err)
			}
			env, err := dbEnsureProductionEnvironment(ctx.AppDB(), d)
			if err != nil {
				t.Fatal(err)
			}
			effective := effectiveDeploymentForEnvironment(d, env)
			result, err := (&App{}).setupCloudBackend(t.Context(), effective, cloudBackendSetupInput{Provider: provider, ConnectionID: 77, ConfigJSON: `{"app_id":"app","branch":"main","profile_id":"profile","configuration_id":"config","workflow_id":"native","commit_id":"commit"}`})
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := parseCloudBuildConfig(provider, result.ConfigJSON)
			if err != nil || cfg.ConnectionID != 77 || cfg.SourceMode != "bundle" || cfg.ArtifactFile != "Named.ipa" {
				t.Fatal(cfg, err)
			}
			saved, err := dbGetEnvironment(ctx.AppDB(), env.ID)
			if err != nil || saved.BuildBackend != provider || !strings.Contains(saved.BuildBackendJSON, `"connection_id":77`) {
				t.Fatal(saved, err)
			}
		})
	}
}
