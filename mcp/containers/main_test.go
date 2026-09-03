package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestManifestValid(t *testing.T) {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if err := sdk.ValidateManifest(m); err != nil {
		t.Fatalf("validate manifest: %v", err)
	}
	if m.Name != "containers" {
		t.Fatalf("name=%q", m.Name)
	}
	gotScopes := map[string]bool{}
	for _, scope := range m.Scopes {
		gotScopes[string(scope)] = true
	}
	for _, want := range []string{"project", "global"} {
		if !gotScopes[want] {
			t.Fatalf("manifest missing %s scope", want)
		}
	}
	for _, dep := range m.Requires.Integrations {
		if dep.Role == "certs" {
			t.Fatal("containers manifest still declares unused Certs integration")
		}
	}
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "containers.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	sort.Strings(files)
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read migration %s: %v", file, err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatalf("apply migration %s: %v", file, err)
		}
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestManifestFileValid(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	m, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	if err := sdk.ValidateManifest(m); err != nil {
		t.Fatalf("validate apteva.yaml: %v", err)
	}
	if m.Name != "containers" {
		t.Fatalf("name=%q", m.Name)
	}
	gotScopes := map[string]bool{}
	for _, scope := range m.Scopes {
		gotScopes[string(scope)] = true
	}
	for _, want := range []string{"project", "global"} {
		if !gotScopes[want] {
			t.Fatalf("manifest missing %s scope", want)
		}
	}
}

func TestNormalizeRunSpecDefaults(t *testing.T) {
	spec, err := normalizeRunSpec(RunSpec{
		Name:    "demo-nginx",
		Image:   "nginx:alpine",
		Ports:   []PortSpec{{ContainerPort: 80}},
		Volumes: []VolumeSpec{{Name: "data", MountPath: "/data"}},
		Files:   []FileSpec{{Path: "/data/apteva.yaml", Content: "server:\n  registration: locked\n"}},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if spec.RestartPolicy != "unless-stopped" {
		t.Fatalf("restart_policy=%q", spec.RestartPolicy)
	}
	if spec.PullPolicy != "missing" {
		t.Fatalf("pull_policy=%q", spec.PullPolicy)
	}
	if spec.HealthPath != "/" {
		t.Fatalf("health_path=%q", spec.HealthPath)
	}
	if spec.Ports[0].BindAddr != "127.0.0.1" || spec.Ports[0].Protocol != "tcp" {
		t.Fatalf("port defaults not applied: %+v", spec.Ports[0])
	}
	if spec.Files[0].Mode != "0600" || spec.Files[0].Path != "/data/apteva.yaml" {
		t.Fatalf("file defaults not applied: %+v", spec.Files[0])
	}
}

func TestNormalizeRunSpecRemoteDefaults(t *testing.T) {
	spec, err := normalizeRunSpec(RunSpec{
		Name:       "demo-nginx",
		Image:      "nginx:alpine",
		InstanceID: 7,
		Ports:      []PortSpec{{ContainerPort: 80}},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if spec.HostID != 7 || spec.InstanceID != 7 {
		t.Fatalf("target ids not normalized: host=%d instance=%d", spec.HostID, spec.InstanceID)
	}
	if spec.Ports[0].BindAddr != "0.0.0.0" {
		t.Fatalf("remote bind_addr=%q, want 0.0.0.0", spec.Ports[0].BindAddr)
	}
}

func TestNormalizeRunSpecRejectsUnsafeInputs(t *testing.T) {
	bad := []RunSpec{
		{Name: "Bad Name", Image: "nginx"},
		{Name: "ok", Image: ""},
		{Name: "ok", Image: "nginx", HostID: -1},
		{Name: "ok", Image: "nginx", HostID: 7, InstanceID: 8},
		{Name: "ok", Image: "nginx", PullPolicy: "sometimes"},
		{Name: "ok", Image: "nginx", Ports: []PortSpec{{ContainerPort: 70000}}},
		{Name: "ok", Image: "nginx", Volumes: []VolumeSpec{{Name: "data", MountPath: "relative"}}},
		{Name: "ok", Image: "nginx", Volumes: []VolumeSpec{{Name: "data", MountPath: "/data"}}, Files: []FileSpec{{Path: "relative", Content: "x"}}},
		{Name: "ok", Image: "nginx", Volumes: []VolumeSpec{{Name: "data", MountPath: "/data"}}, Files: []FileSpec{{Path: "/etc/apteva.yaml", Content: "x"}}},
		{Name: "ok", Image: "nginx", Volumes: []VolumeSpec{{Name: "data", MountPath: "/data"}}, Files: []FileSpec{{Path: "/data/apteva.yaml", Content: "x", ContentBase64: "eA=="}}},
		{Name: "ok", Image: "nginx", Volumes: []VolumeSpec{{Name: "data", MountPath: "/data"}}, Files: []FileSpec{{Path: "/data/apteva.yaml", Content: "x", Mode: "9999"}}},
	}
	for _, spec := range bad {
		if _, err := normalizeRunSpec(spec); err == nil {
			t.Fatalf("expected error for %+v", spec)
		}
	}
}

func TestDockerRunArgsIncludesPullPolicy(t *testing.T) {
	args, err := dockerRunArgs(RunSpec{
		Name:          "demo",
		Image:         "ghcr.io/apteva/apteva:latest",
		RestartPolicy: "unless-stopped",
		PullPolicy:    "always",
		Ports:         []PortSpec{{BindAddr: "127.0.0.1", HostPort: 18080, ContainerPort: 5280, Protocol: "tcp"}},
	}, "containers-demo", "containers-demo")
	if err != nil {
		t.Fatalf("docker run args: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--pull always") {
		t.Fatalf("run args missing pull policy: %v", args)
	}
	if args[len(args)-1] != "ghcr.io/apteva/apteva:latest" {
		t.Fatalf("image should remain final arg: %v", args)
	}
}

func TestNormalizeRunSpecAcceptsBase64FileContent(t *testing.T) {
	spec, err := normalizeRunSpec(RunSpec{
		Name:    "demo",
		Image:   "nginx",
		Volumes: []VolumeSpec{{Name: "data", MountPath: "/data"}},
		Files: []FileSpec{{
			Path:          "/data/config.json",
			ContentBase64: base64.StdEncoding.EncodeToString([]byte(`{"ok":true}`)),
			Mode:          "0640",
		}},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	writes, err := resolveFileWrites(RunSpec{
		Name:    spec.Name,
		Image:   spec.Image,
		Volumes: []VolumeSpec{{Name: "data", DockerVolumeName: "containers-demo-data", MountPath: "/data"}},
		Files:   spec.Files,
	})
	if err != nil {
		t.Fatalf("resolve writes: %v", err)
	}
	if len(writes) != 1 || string(writes[0].Content) != `{"ok":true}` || writes[0].RelPath != "config.json" {
		t.Fatalf("unexpected writes: %+v", writes)
	}
}

func TestListWorkloadsHidesDestroyedByDefault(t *testing.T) {
	db := testDB(t)
	running := &Workload{
		ID: "wrk_running", Name: "running", Kind: "container", Image: "nginx:alpine",
		Status: StatusRunning, DesiredStatus: StatusRunning, ContainerName: "containers-running",
		NetworkName: "containers-running", HealthStatus: "healthy", HealthPath: "/",
		ConfigJSON: `{}`, EnvJSON: `{}`, ResourcesJSON: `{}`, RestartPolicy: "unless-stopped",
	}
	destroyed := &Workload{
		ID: "wrk_destroyed", Name: "destroyed", Kind: "container", Image: "nginx:alpine",
		Status: StatusRunning, DesiredStatus: StatusRunning, ContainerName: "containers-destroyed",
		NetworkName: "containers-destroyed", HealthStatus: "healthy", HealthPath: "/",
		ConfigJSON: `{}`, EnvJSON: `{}`, ResourcesJSON: `{}`, RestartPolicy: "unless-stopped",
	}
	if err := insertWorkload(db, running, nil, nil); err != nil {
		t.Fatalf("insert running: %v", err)
	}
	if err := insertWorkload(db, destroyed, nil, nil); err != nil {
		t.Fatalf("insert destroyed: %v", err)
	}
	if err := deleteWorkloadRows(db, destroyed.ID); err != nil {
		t.Fatalf("destroy workload: %v", err)
	}
	rows, err := listWorkloads(db, "")
	if err != nil {
		t.Fatalf("list workloads: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != running.ID {
		t.Fatalf("default list = %+v, want only %s", rows, running.ID)
	}
	destroyedRows, err := listWorkloads(db, StatusDestroyed)
	if err != nil {
		t.Fatalf("list destroyed: %v", err)
	}
	if len(destroyedRows) != 1 || destroyedRows[0].ID != destroyed.ID {
		t.Fatalf("destroyed list = %+v, want only %s", destroyedRows, destroyed.ID)
	}
	if destroyedRows[0].HealthStatus != StatusDestroyed || destroyedRows[0].LastError != "" {
		t.Fatalf("destroyed state not cleaned up: %+v", destroyedRows[0])
	}
}

func TestDestroyedWorkloadNameCanBeReused(t *testing.T) {
	db := testDB(t)
	first := testWorkload("wrk_first", "demo", StatusRunning)
	if err := insertWorkload(db, first, nil, nil); err != nil {
		t.Fatalf("insert first: %v", err)
	}
	if err := deleteWorkloadRows(db, first.ID); err != nil {
		t.Fatalf("destroy first: %v", err)
	}
	second := testWorkload("wrk_second", "demo", StatusRunning)
	if err := insertWorkload(db, second, nil, nil); err != nil {
		t.Fatalf("reuse destroyed name: %v", err)
	}
	third := testWorkload("wrk_third", "demo", StatusRunning)
	if err := insertWorkload(db, third, nil, nil); err == nil {
		t.Fatalf("expected duplicate active name to fail")
	}
}

func TestDestroyDoesNotHideWorkloadWhenDockerCleanupFails(t *testing.T) {
	db := testDB(t)
	w := testWorkload("wrk_running", "demo", StatusRunning)
	if err := insertWorkload(db, w, nil, nil); err != nil {
		t.Fatalf("insert workload: %v", err)
	}
	app := &App{backend: fakeDockerBackend{removeErr: errors.New("docker unavailable")}}
	err := app.destroyWorkload(context.Background(), nil, db, w.ID, false)
	if err == nil {
		t.Fatalf("expected destroy failure")
	}
	got, err := getWorkload(db, w.ID)
	if err != nil {
		t.Fatalf("get workload: %v", err)
	}
	if got.Status != StatusError || !strings.Contains(got.LastError, "docker unavailable") {
		t.Fatalf("workload was not kept visible with error: %+v", got)
	}
	rows, err := listWorkloads(db, "")
	if err != nil {
		t.Fatalf("list workloads: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != w.ID {
		t.Fatalf("failed destroy should remain visible, got %+v", rows)
	}
}

func TestDestroyIgnoresAlreadyMissingDockerResources(t *testing.T) {
	db := testDB(t)
	w := testWorkload("wrk_running", "demo", StatusRunning)
	if err := insertWorkload(db, w, nil, []VolumeSpec{{Name: "data", DockerVolumeName: "containers-demo-data", MountPath: "/data"}}); err != nil {
		t.Fatalf("insert workload: %v", err)
	}
	app := &App{backend: fakeDockerBackend{
		removeErr:        errors.New("docker rm: No such container: containers-demo"),
		removeNetworkErr: errors.New("docker network rm: No such network: containers-demo"),
		removeVolumeErr:  errors.New("docker volume rm: No such volume: containers-demo-data"),
	}}
	if err := app.destroyWorkload(context.Background(), nil, db, w.ID, true); err != nil {
		t.Fatalf("destroy missing resources should succeed: %v", err)
	}
	got, err := getWorkload(db, w.ID)
	if err != nil {
		t.Fatalf("get workload: %v", err)
	}
	if got.Status != StatusDestroyed {
		t.Fatalf("status=%q, want destroyed", got.Status)
	}
}

func TestStartRuntimeCleansNetworkAndVolumesWhenRunFails(t *testing.T) {
	db := testDB(t)
	w := testWorkload("wrk_failed_start", "demo", StatusCreating)
	volumes := []VolumeSpec{{Name: "data", DockerVolumeName: "containers-demo-data", MountPath: "/data"}}
	if err := insertWorkload(db, w, nil, volumes); err != nil {
		t.Fatalf("insert workload: %v", err)
	}
	var removedNetworks []string
	var removedVolumes []string
	app := &App{backend: fakeDockerBackend{
		runErr:          errors.New("docker run: no matching manifest for linux/amd64"),
		removedNetworks: &removedNetworks,
		removedVolumes:  &removedVolumes,
	}}
	spec := RunSpec{
		Name:          "demo",
		Image:         "ghcr.io/apteva/apteva:latest",
		RestartPolicy: "unless-stopped",
		Volumes:       volumes,
	}
	err := app.startWorkloadRuntime(context.Background(), nil, db, w.ID, spec, w.ContainerName, w.NetworkName)
	if err == nil || !strings.Contains(err.Error(), "no matching manifest") {
		t.Fatalf("expected image pull/run failure, got %v", err)
	}
	if len(removedNetworks) != 1 || removedNetworks[0] != w.NetworkName {
		t.Fatalf("removed networks = %#v, want %q", removedNetworks, w.NetworkName)
	}
	if len(removedVolumes) != 1 || removedVolumes[0] != volumes[0].DockerVolumeName {
		t.Fatalf("removed volumes = %#v, want %q", removedVolumes, volumes[0].DockerVolumeName)
	}
	got, err := getWorkload(db, w.ID)
	if err != nil {
		t.Fatalf("get workload: %v", err)
	}
	if got.Status != StatusError || got.ContainerID != "" || !strings.Contains(got.LastError, "no matching manifest") {
		t.Fatalf("failed start not marked cleanly: %+v", got)
	}
}

func TestPrepareWorkloadSanitizesFileContentsFromConfigJSON(t *testing.T) {
	db := testDB(t)
	app := &App{backend: fakeDockerBackend{}}
	w, _, err := app.prepareWorkload(nil, db, RunSpec{
		Name:    "demo",
		Image:   "nginx:alpine",
		Volumes: []VolumeSpec{{Name: "data", MountPath: "/data"}},
		Files: []FileSpec{{
			Path:    "/data/apteva.yaml",
			Content: "top-secret-config",
			Mode:    "0600",
			Secret:  true,
		}},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if strings.Contains(w.ConfigJSON, "top-secret-config") || strings.Contains(w.ConfigJSON, "content") {
		t.Fatalf("config_json leaked file content: %s", w.ConfigJSON)
	}
	if !strings.Contains(w.ConfigJSON, "/data/apteva.yaml") || !strings.Contains(w.ConfigJSON, `"secret":true`) {
		t.Fatalf("config_json missing file metadata: %s", w.ConfigJSON)
	}
	got, err := getWorkload(db, w.ID)
	if err != nil {
		t.Fatalf("get workload: %v", err)
	}
	if strings.Contains(got.ConfigJSON, "top-secret-config") || strings.Contains(got.ConfigJSON, "content") {
		t.Fatalf("stored config_json leaked file content: %s", got.ConfigJSON)
	}
}

func TestStartRuntimeWritesFilesBeforeRun(t *testing.T) {
	db := testDB(t)
	w := testWorkload("wrk_write", "demo", StatusCreating)
	volumes := []VolumeSpec{{Name: "data", DockerVolumeName: "containers-demo-data", MountPath: "/data"}}
	if err := insertWorkload(db, w, nil, volumes); err != nil {
		t.Fatalf("insert workload: %v", err)
	}
	var ops []string
	var writes []VolumeFileWrite
	app := &App{backend: fakeDockerBackend{ops: &ops, writes: &writes}}
	spec := RunSpec{
		Name:          "demo",
		Image:         "nginx:alpine",
		RestartPolicy: "unless-stopped",
		Volumes:       volumes,
		Files: []FileSpec{{
			Path:    "/data/apteva.yaml",
			Content: "server:\n  public_url: https://agent.example.com\n",
			Mode:    "0600",
			Secret:  true,
		}},
	}
	if err := app.startWorkloadRuntime(context.Background(), nil, db, w.ID, spec, w.ContainerName, w.NetworkName); err != nil {
		t.Fatalf("start runtime: %v", err)
	}
	wantOps := []string{
		"network:containers-demo",
		"volume:containers-demo-data",
		"write:containers-demo-data:apteva.yaml",
		"run:containers-demo",
	}
	if strings.Join(ops, "|") != strings.Join(wantOps, "|") {
		t.Fatalf("ops=%#v, want %#v", ops, wantOps)
	}
	if len(writes) != 1 || writes[0].VolumeName != "containers-demo-data" || writes[0].RelPath != "apteva.yaml" || string(writes[0].Content) == "" || writes[0].Mode != "0600" {
		t.Fatalf("unexpected writes: %+v", writes)
	}
}

func TestStartRuntimeCleansNetworkAndVolumesWhenFileWriteFails(t *testing.T) {
	db := testDB(t)
	w := testWorkload("wrk_write_failed", "demo", StatusCreating)
	volumes := []VolumeSpec{{Name: "data", DockerVolumeName: "containers-demo-data", MountPath: "/data"}}
	if err := insertWorkload(db, w, nil, volumes); err != nil {
		t.Fatalf("insert workload: %v", err)
	}
	var ops []string
	var removedNetworks []string
	var removedVolumes []string
	app := &App{backend: fakeDockerBackend{
		writeErr:        errors.New("write failed"),
		ops:             &ops,
		removedNetworks: &removedNetworks,
		removedVolumes:  &removedVolumes,
	}}
	spec := RunSpec{
		Name:          "demo",
		Image:         "nginx:alpine",
		RestartPolicy: "unless-stopped",
		Volumes:       volumes,
		Files:         []FileSpec{{Path: "/data/apteva.yaml", Content: "secret-value", Mode: "0600", Secret: true}},
	}
	err := app.startWorkloadRuntime(context.Background(), nil, db, w.ID, spec, w.ContainerName, w.NetworkName)
	if err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("expected write failure, got %v", err)
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("write failure leaked secret content: %v", err)
	}
	if strings.Contains(strings.Join(ops, "|"), "run:") {
		t.Fatalf("run should not happen after write failure: %#v", ops)
	}
	if len(removedNetworks) != 1 || removedNetworks[0] != w.NetworkName {
		t.Fatalf("removed networks = %#v, want %q", removedNetworks, w.NetworkName)
	}
	if len(removedVolumes) != 1 || removedVolumes[0] != volumes[0].DockerVolumeName {
		t.Fatalf("removed volumes = %#v, want %q", removedVolumes, volumes[0].DockerVolumeName)
	}
}

func TestHealthPollRetriesErrorWorkloads(t *testing.T) {
	db := testDB(t)
	w := testWorkload("wrk_error", "demo", StatusError)
	if err := insertWorkload(db, w, nil, nil); err != nil {
		t.Fatalf("insert workload: %v", err)
	}
	app := &App{backend: fakeDockerBackend{inspectState: &ContainerState{ID: "cid", Running: true}}}
	if err := app.pollHealth(context.Background(), nil, db); err != nil {
		t.Fatalf("poll health: %v", err)
	}
	got, err := getWorkload(db, w.ID)
	if err != nil {
		t.Fatalf("get workload: %v", err)
	}
	if got.Status != StatusRunning || got.HealthStatus != "running" || got.LastError != "" {
		t.Fatalf("workload did not recover: %+v", got)
	}
}

func TestWorkloadUsageMeasuresVolumeStorage(t *testing.T) {
	db := testDB(t)
	w := testWorkload("wrk_usage", "usage", StatusRunning)
	if err := insertWorkload(db, w, nil, []VolumeSpec{
		{Name: "data", DockerVolumeName: "containers-usage-data", MountPath: "/data"},
		{Name: "cache", DockerVolumeName: "containers-usage-cache", MountPath: "/cache"},
	}); err != nil {
		t.Fatalf("insert workload: %v", err)
	}
	app := &App{backend: fakeDockerBackend{volumeUsage: map[string]int64{
		"containers-usage-data":  512,
		"containers-usage-cache": 128,
	}}}
	usage, err := app.workloadUsage(context.Background(), nil, db, w.ID)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if usage.WorkloadID != w.ID || len(usage.Metrics) != 3 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	total := usage.Metrics[0]
	if total.FeatureKey != "containers.storage.bytes" || total.Kind != "gauge" || total.Quantity != 640 || total.Source != "docker_volume_total" {
		t.Fatalf("unexpected total metric: %+v", total)
	}
	got, err := getWorkload(db, w.ID)
	if err != nil {
		t.Fatalf("get workload: %v", err)
	}
	sizes := map[string]int64{}
	for _, v := range got.Volumes {
		sizes[v.Name] = v.SizeBytes
	}
	if sizes["data"] != 512 || sizes["cache"] != 128 {
		t.Fatalf("volume sizes not persisted: %+v", sizes)
	}
}

func TestDockerErrorRedactsEnvValues(t *testing.T) {
	err := formatDockerError([]string{"run", "-e", "SECRET=value", "--env", "TOKEN=abc", "--env=PASS=def", "nginx", "sh", "-c", "use command-secret"}, "failed")
	msg := err.Error()
	for _, secret := range []string{"value", "abc", "def", "command-secret"} {
		if strings.Contains(msg, secret) {
			t.Fatalf("secret %q leaked in %q", secret, msg)
		}
	}
	for _, redacted := range []string{"SECRET=<redacted>", "TOKEN=<redacted>", "--env=PASS=<redacted>"} {
		if !strings.Contains(msg, redacted) {
			t.Fatalf("missing redaction %q in %q", redacted, msg)
		}
	}
}

func TestRemoteDockerUsesInstancesRunCommand(t *testing.T) {
	platform := &containersPlatformStub{}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, nil, sdk.Config{}, platform, nil)
	remote := RemoteDocker{app: ctx, instanceID: 7}
	cid, err := remote.Run(context.Background(), RunSpec{
		Name:          "demo",
		Image:         "nginx:alpine",
		RestartPolicy: "unless-stopped",
		PullPolicy:    "always",
		Ports:         []PortSpec{{BindAddr: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}},
		Env:           map[string]string{"PORT": "80"},
	}, "containers-demo", "containers-demo")
	if err != nil {
		t.Fatalf("remote run: %v", err)
	}
	if cid != "cid123" {
		t.Fatalf("cid=%q", cid)
	}
	if len(platform.calls) != 1 {
		t.Fatalf("calls=%d, want 1", len(platform.calls))
	}
	call := platform.calls[0]
	if call.appName != "instances" || call.tool != "instance_run_command" || call.input["id"] != int64(7) {
		t.Fatalf("unexpected call: %+v", call)
	}
	cmd, _ := call.input["cmd"].(string)
	for _, want := range []string{"'docker' 'run'", "'--pull' 'always'", "'-p' '0.0.0.0:8080:80/tcp'", "'nginx:alpine'"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("remote docker command missing %q in %q", want, cmd)
		}
	}
}

func TestRemoteDockerWritesVolumeFileWithoutContentInRunCommand(t *testing.T) {
	platform := &containersPlatformStub{}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, nil, sdk.Config{}, platform, nil)
	remote := RemoteDocker{app: ctx, instanceID: 7}
	secret := "super-secret-password"
	if err := remote.WriteVolumeFile(context.Background(), "containers-demo-data", "secrets/password", []byte(secret), "0400"); err != nil {
		t.Fatalf("write volume file: %v", err)
	}
	if len(platform.calls) < 2 {
		t.Fatalf("calls=%d, want at least 2", len(platform.calls))
	}
	if platform.calls[0].tool != "instance_upload_file" {
		t.Fatalf("first call = %+v, want upload", platform.calls[0])
	}
	runCall := platform.calls[1]
	if runCall.tool != "instance_run_command" {
		t.Fatalf("second call = %+v, want run command", runCall)
	}
	cmd, _ := runCall.input["cmd"].(string)
	encoded := base64.StdEncoding.EncodeToString([]byte(secret))
	if strings.Contains(cmd, secret) || strings.Contains(cmd, encoded) {
		t.Fatalf("remote run command leaked file content: %q", cmd)
	}
	for _, want := range []string{"'docker' 'run'", "'containers-demo-data:/target'", "'/target/secrets/password'", "'0400'"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("remote write command missing %q in %q", want, cmd)
		}
	}
}

func TestPrepareWorkloadStoresRemoteTargetAndPublicURL(t *testing.T) {
	db := testDB(t)
	platform := &containersPlatformStub{publicIPv4: "203.0.113.10"}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{}, platform, nil)
	app := &App{backend: fakeDockerBackend{}}
	w, spec, err := app.prepareWorkload(ctx, db, RunSpec{
		Name:   "demo",
		Image:  "nginx:alpine",
		HostID: 7,
		Ports:  []PortSpec{{ContainerPort: 80, HostPort: 18080}},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if w.HostID != 7 || w.InstanceID != 7 || spec.HostID != 7 || spec.InstanceID != 7 {
		t.Fatalf("remote target not stored: workload=%+v spec=%+v", w, spec)
	}
	if w.PublicURL != "http://203.0.113.10:18080" || w.HealthURL != "http://203.0.113.10:18080/" {
		t.Fatalf("remote urls wrong: public=%q health=%q", w.PublicURL, w.HealthURL)
	}
	got, err := getWorkload(db, w.ID)
	if err != nil {
		t.Fatalf("get workload: %v", err)
	}
	if got.HostID != 7 || got.InstanceID != 7 {
		t.Fatalf("db target not stored: %+v", got)
	}
}

func testWorkload(id, name, status string) *Workload {
	return &Workload{
		ID: id, Name: name, Kind: "container", Image: "nginx:alpine",
		Status: status, DesiredStatus: StatusRunning, ContainerName: "containers-" + name,
		NetworkName: "containers-" + name, HealthStatus: "unknown", HealthPath: "/",
		ConfigJSON: `{}`, EnvJSON: `{}`, ResourcesJSON: `{}`, RestartPolicy: "unless-stopped",
	}
}

type containersPlatformCall struct {
	appName string
	tool    string
	input   map[string]any
}

type containersPlatformStub struct {
	tk.BasePlatformClient
	calls      []containersPlatformCall
	publicIPv4 string
}

func (p *containersPlatformStub) CallAppResult(appName, tool string, input map[string]any, out any) error {
	p.calls = append(p.calls, containersPlatformCall{appName: appName, tool: tool, input: input})
	var raw []byte
	switch tool {
	case "instance_run_command":
		raw, _ = json.Marshal(map[string]any{"output": "cid123\n", "exit_code": 0})
	case "instance_upload_file":
		raw, _ = json.Marshal(map[string]any{"bytes_written": 12})
	case "instance_get":
		raw, _ = json.Marshal(map[string]any{"instance": map[string]any{"public_ipv4": p.publicIPv4, "status": "ready"}})
	default:
		raw, _ = json.Marshal(map[string]any{})
	}
	return json.Unmarshal(raw, out)
}

type fakeDockerBackend struct {
	removeErr        error
	removeNetworkErr error
	removeVolumeErr  error
	runErr           error
	writeErr         error
	inspectState     *ContainerState
	inspectErr       error
	volumeUsage      map[string]int64
	removedNetworks  *[]string
	removedVolumes   *[]string
	ops              *[]string
	writes           *[]VolumeFileWrite
}

func (f fakeDockerBackend) Probe(context.Context) error { return nil }
func (f fakeDockerBackend) CreateNetwork(_ context.Context, name string) error {
	if f.ops != nil {
		*f.ops = append(*f.ops, "network:"+name)
	}
	return nil
}
func (f fakeDockerBackend) CreateVolume(_ context.Context, name string) error {
	if f.ops != nil {
		*f.ops = append(*f.ops, "volume:"+name)
	}
	return nil
}
func (f fakeDockerBackend) WriteVolumeFile(_ context.Context, volumeName, relPath string, content []byte, mode string) error {
	if f.ops != nil {
		*f.ops = append(*f.ops, "write:"+volumeName+":"+relPath)
	}
	if f.writes != nil {
		*f.writes = append(*f.writes, VolumeFileWrite{VolumeName: volumeName, RelPath: relPath, Content: content, Mode: mode})
	}
	return f.writeErr
}
func (f fakeDockerBackend) Run(_ context.Context, _ RunSpec, containerName, _ string) (string, error) {
	if f.ops != nil {
		*f.ops = append(*f.ops, "run:"+containerName)
	}
	if f.runErr != nil {
		return "", f.runErr
	}
	return "cid", nil
}
func (f fakeDockerBackend) Start(context.Context, string) error   { return nil }
func (f fakeDockerBackend) Stop(context.Context, string) error    { return nil }
func (f fakeDockerBackend) Restart(context.Context, string) error { return nil }
func (f fakeDockerBackend) Remove(context.Context, string, bool) error {
	return f.removeErr
}
func (f fakeDockerBackend) RemoveNetwork(_ context.Context, name string) error {
	if f.removedNetworks != nil {
		*f.removedNetworks = append(*f.removedNetworks, name)
	}
	return f.removeNetworkErr
}
func (f fakeDockerBackend) RemoveVolume(_ context.Context, name string) error {
	if f.removedVolumes != nil {
		*f.removedVolumes = append(*f.removedVolumes, name)
	}
	return f.removeVolumeErr
}
func (f fakeDockerBackend) VolumeUsage(_ context.Context, name string) (int64, error) {
	if f.volumeUsage != nil {
		return f.volumeUsage[name], nil
	}
	return 0, nil
}
func (f fakeDockerBackend) Logs(context.Context, string, int) (string, error) {
	return "", nil
}
func (f fakeDockerBackend) Inspect(context.Context, string) (*ContainerState, error) {
	if f.inspectErr != nil {
		return nil, f.inspectErr
	}
	if f.inspectState != nil {
		return f.inspectState, nil
	}
	return &ContainerState{ID: "cid", Running: true}, nil
}
