package main

import (
	"regexp"
	"strings"
	"testing"
)

func sandboxSpec(mut func(*RunSpec)) RunSpec {
	s := RunSpec{Name: "sb", Image: "alpine:3.20"}
	if mut != nil {
		mut(&s)
	}
	return s
}

func TestSandboxDefaultsKeepPreviousBehaviour(t *testing.T) {
	spec, err := normalizeRunSpec(sandboxSpec(nil))
	if err != nil {
		t.Fatal(err)
	}
	if spec.NetworkMode != NetworkDefault || spec.PidsLimit != 512 || spec.ReadOnlyRootfs || len(spec.Tmpfs) != 0 {
		t.Fatalf("defaults changed: %+v", spec)
	}
	args, err := dockerRunArgs(spec, "c", "n")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, unwanted := range []string{"--read-only", "--cap-drop", "no-new-privileges", "--tmpfs", "HTTP_PROXY"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("default run args contain %q: %s", unwanted, joined)
		}
	}
	if !strings.Contains(joined, "--pids-limit 512") || !strings.Contains(joined, "--network n") {
		t.Fatalf("default run args: %s", joined)
	}
}

func TestSandboxValidation(t *testing.T) {
	cases := map[string]struct {
		mut  func(*RunSpec)
		want string
	}{
		"unknown mode":           {func(s *RunSpec) { s.NetworkMode = "bridge" }, "unsupported network_mode"},
		"allow without registry": {func(s *RunSpec) { s.NetworkMode = "none"; s.EgressAllow = []string{"a.com"} }, "only valid with network_mode registry_only"},
		"bad allow entry":        {func(s *RunSpec) { s.NetworkMode = "registry_only"; s.EgressAllow = []string{"https://a.com/x"} }, "egress_allow[0]"},
		"bad allow port":         {func(s *RunSpec) { s.NetworkMode = "registry_only"; s.EgressAllow = []string{"a.com:99999"} }, "invalid port"},
		"ports need default":     {func(s *RunSpec) { s.NetworkMode = "none"; s.Ports = []PortSpec{{ContainerPort: 80}} }, "published ports require"},
		"hardened needs user":    {func(s *RunSpec) { s.Hardened = true }, "non-root user"},
		"hardened rejects root":  {func(s *RunSpec) { s.Hardened = true; s.User = "0:0" }, "cannot run as root"},
		"pids too low":           {func(s *RunSpec) { s.PidsLimit = 3 }, "pids_limit"},
		"tmpfs relative":         {func(s *RunSpec) { s.Tmpfs = []TmpfsSpec{{MountPath: "tmp"}} }, "tmpfs[0].mount_path"},
		"tmpfs option injection": {func(s *RunSpec) { s.Tmpfs = []TmpfsSpec{{MountPath: "/tmp,exec"}} }, "tmpfs[0].mount_path"},
		"tmpfs too big":          {func(s *RunSpec) { s.Tmpfs = []TmpfsSpec{{MountPath: "/tmp", SizeMB: 99999}} }, "size_mb"},
		"tmpfs over volume": {func(s *RunSpec) {
			s.Volumes = []VolumeSpec{{Name: "data", MountPath: "/data"}}
			s.Tmpfs = []TmpfsSpec{{MountPath: "/data"}}
		}, "duplicates another mount"},
		"shared must be read-only": {func(s *RunSpec) { s.Volumes = []VolumeSpec{{Name: "data", MountPath: "/data", SharedFrom: "wrk_1"}} }, "requires read_only"},
		"shared and retained": {func(s *RunSpec) {
			s.Volumes = []VolumeSpec{{Name: "data", MountPath: "/data", SharedFrom: "wrk_1", RetainedFrom: "wrk_2", ReadOnly: true}}
		}, "both retained_from and shared_from"},
		"file into read-only volume": {func(s *RunSpec) {
			s.Volumes = []VolumeSpec{{Name: "data", MountPath: "/data", ReadOnly: true}}
			s.Files = []FileSpec{{Path: "/data/x", Content: "x"}}
		}, "read-only volume"},
		"hardened docker host": {func(s *RunSpec) {
			s.Hardened = true
			s.User = "1000"
			s.Env = map[string]string{"DOCKER_HOST": "unix:///x"}
		}, "cannot receive env"},
		"hardened apteva env": {func(s *RunSpec) {
			s.Hardened = true
			s.User = "1000"
			s.Env = map[string]string{"APTEVA_GATEWAY_URL": "x"}
		}, "cannot receive env"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := normalizeRunSpec(sandboxSpec(tc.mut))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestSandboxRejectsPlatformTokenAnywhere(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "tok-super-secret-123")
	for name, mut := range map[string]func(*RunSpec){
		"env": func(s *RunSpec) { s.Env = map[string]string{"X": "Bearer tok-super-secret-123"} },
		"file": func(s *RunSpec) {
			s.Volumes = []VolumeSpec{{Name: "d", MountPath: "/d"}}
			s.Files = []FileSpec{{Path: "/d/t", Content: "tok-super-secret-123"}}
		},
	} {
		if _, err := normalizeRunSpec(sandboxSpec(mut)); err == nil || !strings.Contains(err.Error(), "platform app token") {
			t.Fatalf("%s: err=%v", name, err)
		}
	}
	w := &Workload{}
	in := executionInput{Argv: []string{"curl", "-H", "Authorization: tok-super-secret-123"}}
	if _, err := normalizeExecutionInput(in, w, 60); err == nil || !strings.Contains(err.Error(), "platform app token") {
		t.Fatalf("argv token accepted: %v", err)
	}
	in = executionInput{Argv: []string{"true"}, Env: map[string]string{"APTEVA_X": "1"}}
	if _, err := normalizeExecutionInput(in, &Workload{Hardened: true}, 60); err == nil {
		t.Fatal("hardened execution accepted APTEVA_ env")
	}
	if _, err := normalizeExecutionInput(in, w, 60); err != nil {
		t.Fatalf("non-hardened execution rejected APTEVA_ env: %v", err)
	}
}

func TestSandboxHardenedRunArgs(t *testing.T) {
	spec, err := normalizeRunSpec(sandboxSpec(func(s *RunSpec) {
		s.Hardened = true
		s.User = "1000:1000"
		s.NetworkMode = "registry_only"
		s.Volumes = []VolumeSpec{{Name: "src", MountPath: "/src", ReadOnly: true}, {Name: "out", MountPath: "/out"}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	for i := range spec.Volumes {
		spec.Volumes[i].DockerVolumeName = "c-" + spec.Volumes[i].Name
	}
	if spec.PidsLimit != 256 || !spec.ReadOnlyRootfs || len(spec.Tmpfs) != 1 || spec.Tmpfs[0].MountPath != "/tmp" {
		t.Fatalf("hardened defaults: %+v", spec)
	}
	if strings.Join(spec.EgressAllow, ",") != "registry.npmjs.org" {
		t.Fatalf("default allow list: %v", spec.EgressAllow)
	}
	args, err := dockerRunArgs(spec, "c", "c-int")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-v c-src:/src:ro", "-v c-out:/out ", "--tmpfs /tmp:rw,nosuid,nodev,size=256m", "--read-only",
		"--cap-drop ALL", "--security-opt no-new-privileges", "--pids-limit 256", "--user 1000:1000",
		"-e HTTPS_PROXY=http://apteva-egress:8888", "-e NO_PROXY=localhost,127.0.0.1,::1",
	} {
		if !strings.Contains(joined+" ", want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
}

func TestSandboxNeverBindMountsHostPaths(t *testing.T) {
	for _, name := range []string{"/var/run/docker.sock", "/", "a/b", "x:/y", ".hidden", ""} {
		spec := sandboxSpec(func(s *RunSpec) { s.Volumes = []VolumeSpec{{Name: "v", MountPath: "/v", DockerVolumeName: name}} })
		if _, err := dockerRunArgs(spec, "c", "n"); err == nil {
			t.Fatalf("volume source %q accepted", name)
		}
	}
}

func TestEgressFilterIsAnchored(t *testing.T) {
	_, allow, err := normalizeNetworkMode("registry_only", []string{"Registry.npmjs.org", "*.pkg.dev", "mirror.local:8443", "registry.npmjs.org"})
	if err != nil {
		t.Fatal(err)
	}
	if len(allow) != 3 {
		t.Fatalf("dedupe/lowercase failed: %v", allow)
	}
	var res []*regexp.Regexp
	for _, line := range strings.Split(egressFilter(allow), "\n") {
		res = append(res, regexp.MustCompile(line))
	}
	match := func(host string) bool {
		for _, re := range res {
			if re.MatchString(host) {
				return true
			}
		}
		return false
	}
	for host, want := range map[string]bool{
		"registry.npmjs.org": true, "registry.npmjs.org.evil.com": false, "evilregistry.npmjs.org": false,
		"a.pkg.dev": true, "a.b.pkg.dev": true, "pkg.dev": false, "pkg.dev.evil.com": false,
		"mirror.local": true, "example.com": false,
	} {
		if match(host) != want {
			t.Fatalf("filter(%q)=%v want %v", host, !want, want)
		}
	}
	conf := egressConfig(allow)
	if !strings.Contains(conf, "FilterDefaultDeny Yes") || !strings.Contains(conf, "ConnectPort 443") || !strings.Contains(conf, "ConnectPort 8443") {
		t.Fatalf("config: %s", conf)
	}
}

func TestSandboxOptionsSurviveStorage(t *testing.T) {
	db := testDB(t)
	a := &App{}
	w, _, err := a.prepareWorkload(nil, db, sandboxSpec(func(s *RunSpec) {
		s.Hardened = true
		s.User = "1000"
		s.NetworkMode = "none"
		s.Volumes = []VolumeSpec{{Name: "data", MountPath: "/data", ReadOnly: true}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := getWorkload(db, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.NetworkMode != NetworkNone || !got.Hardened || !got.ReadOnlyRootfs || got.PidsLimit != 256 || len(got.Tmpfs) != 1 {
		t.Fatalf("stored sandbox options: %+v", got)
	}
	if len(got.Volumes) != 1 || !got.Volumes[0].ReadOnly {
		t.Fatalf("stored volume: %+v", got.Volumes)
	}
}

func TestSharedVolumeRequiresLiveOwnedSource(t *testing.T) {
	db := testDB(t)
	a := &App{}
	owner := ownerIdentity{InstallID: 7, AppName: "workspaces"}
	src, _, err := a.prepareOwnedWorkload(nil, db, sandboxSpec(func(s *RunSpec) {
		s.Name = "src"
		s.Volumes = []VolumeSpec{{Name: "data", MountPath: "/data"}}
	}), owner)
	if err != nil {
		t.Fatal(err)
	}
	borrow := func(o ownerIdentity) (*Workload, error) {
		w, _, err := a.prepareOwnedWorkload(nil, db, sandboxSpec(func(s *RunSpec) {
			s.Name = "test-" + strings.ToLower(newExecutionID()[4:10])
			s.Volumes = []VolumeSpec{{Name: "data", MountPath: "/src", ReadOnly: true, SharedFrom: src.ID}}
		}), o)
		return w, err
	}
	if _, err := borrow(ownerIdentity{InstallID: 8, AppName: "other"}); err == nil {
		t.Fatal("another app borrowed the volume")
	}
	w, err := borrow(owner)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := getWorkload(db, w.ID)
	if got.Volumes[0].DockerVolumeName != src.ContainerName+"-data" {
		t.Fatalf("borrowed volume name %q", got.Volumes[0].DockerVolumeName)
	}
	if got.Volumes[0].SharedFrom != src.ID || !got.Volumes[0].ReadOnly {
		t.Fatalf("borrowed volume row: %+v", got.Volumes[0])
	}
	if _, err := db.Exec(`UPDATE containers_workloads SET status='destroyed' WHERE id=?`, src.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := borrow(owner); err == nil {
		t.Fatal("borrowed from a destroyed workload")
	}
}
