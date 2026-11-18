package main

import (
	"archive/tar"
	"context"
	"strings"
	"testing"
	"time"
)

// Real-Docker tests for 0.7.0. Opt in with RUN_CONTAINERS_TESTS=1.

func runAs(t *testing.T, ctx context.Context, container, controlUser, user, umask, session, script string) (*Execution, string, int) {
	t.Helper()
	d := LocalDocker{}
	id := newExecutionID()
	runtimeID, err := d.StartExecution(ctx, executionRuntimeSpec{ExecutionID: id, ContainerName: container, User: user, ControlUser: controlUser, Umask: umask, SessionKey: session, Argv: []string{"/bin/sh", "-c", script}, StatefulCommand: session != ""})
	if err != nil {
		t.Fatalf("start as %q: %v", user, err)
	}
	execUser := user
	if user == controlUser {
		execUser = ""
	}
	e := &Execution{ID: id, RuntimeContainerID: runtimeID, RuntimeContainerName: container, SessionKey: session, User: execUser}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		state, err := d.InspectExecution(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		if !state.Running {
			out, _, _, err := d.ExecutionOutput(ctx, e)
			if err != nil {
				t.Fatalf("output as %q: %v", user, err)
			}
			return e, strings.TrimSpace(out), state.ExitCode
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("command as %q did not finish", user)
	return nil, "", 0
}

func TestDockerCommandsRunAsChosenUsers(t *testing.T) {
	a, db, ctx := sandboxApp(t)
	persistentShells.CloseAll()
	t.Cleanup(persistentShells.CloseAll)
	w := sandboxWorkload(t, a, db, ctx, RunSpec{User: "1000:1000", Hardened: true, NetworkMode: NetworkNone,
		Tmpfs: []TmpfsSpec{{MountPath: "/shared", SizeMB: 8}}})

	// A writes with umask 022; B can read but not write, in consecutive commands.
	if _, out, code := runAs(t, ctx, w.ContainerName, w.User, "1000:1000", "022", "", "id -u; echo from-a > /shared/a.txt; ls -ln /shared/a.txt"); code != 0 || !strings.HasPrefix(out, "1000") || !strings.Contains(out, "-rw-r--r--") {
		t.Fatalf("user A: code=%d out=%q", code, out)
	}
	if _, out, code := runAs(t, ctx, w.ContainerName, w.User, "2000:2000", "", "", "id -u; cat /shared/a.txt"); code != 0 || out != "2000\nfrom-a" {
		t.Fatalf("user B read: code=%d out=%q", code, out)
	}
	if _, out, code := runAs(t, ctx, w.ContainerName, w.User, "2000:2000", "", "", "echo x >> /shared/a.txt"); code == 0 || !strings.Contains(out, "Permission denied") {
		t.Fatalf("user B write: code=%d out=%q", code, out)
	}

	// Without a umask commands keep the private 077 default.
	runAs(t, ctx, w.ContainerName, w.User, "1000:1000", "", "", "echo secret > /shared/private.txt")
	if _, _, code := runAs(t, ctx, w.ContainerName, w.User, "2000:2000", "", "", "cat /shared/private.txt"); code == 0 {
		t.Fatal("default umask let another user read the file")
	}

	// Sessions belong to one user; another user needs its own session.
	if _, out, code := runAs(t, ctx, w.ContainerName, w.User, "2000:2000", "027", "b-session", "cd /shared; touch b.txt; id -u"); code != 0 || out != "2000" {
		t.Fatalf("session as B: %d %q", code, out)
	}
	if _, out, code := runAs(t, ctx, w.ContainerName, w.User, "2000:2000", "", "b-session", "pwd; touch b2.txt; ls -ln b2.txt"); code != 0 || !strings.HasPrefix(out, "/shared") || !strings.Contains(out, "-rw-r-----") {
		t.Fatalf("session state or umask lost: %d %q", code, out)
	}
	_, err := LocalDocker{}.StartExecution(ctx, executionRuntimeSpec{ExecutionID: newExecutionID(), ContainerName: w.ContainerName, User: "1000:1000", ControlUser: w.User, SessionKey: "b-session", Argv: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "use another session_key") {
		t.Fatalf("session reused by a different user: %v", err)
	}

	// Cancelling a command runs as its own user, so it can kill its processes
	// even though the workload user (and a capability-less root) could not.
	d := LocalDocker{}
	id := newExecutionID()
	runtimeID, err := d.StartExecution(ctx, executionRuntimeSpec{ExecutionID: id, ContainerName: w.ContainerName, User: "2000:2000", ControlUser: w.User, Argv: []string{"sh", "-c", "(setsid sleep 3001 &); sleep 3002 & wait"}})
	if err != nil {
		t.Fatal(err)
	}
	e := &Execution{ID: id, RuntimeContainerID: runtimeID, RuntimeContainerName: w.ContainerName, User: "2000:2000"}
	time.Sleep(500 * time.Millisecond)
	if err := d.StopExecution(ctx, e); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, _ := dockerCombined(ctx, "exec", w.ContainerName, "sh", "-c", `for p in /proc/[0-9]*; do case "$(tr "\\0" " " < $p/cmdline 2>/dev/null)" in *300[12]*) echo $p;; esac; done`)
		if strings.TrimSpace(out) == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("user B's processes survived cancellation: %s", out)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestDockerImportOwnerAndModes(t *testing.T) {
	_, _, ctx := sandboxApp(t)
	vol := "containers-test-import-" + strings.ToLower(newExecutionID()[4:12])
	if _, err := docker(ctx, "volume", "create", vol); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = docker(context.Background(), "volume", "rm", "-f", vol) })
	if _, err := docker(ctx, "run", "--rm", "-v", vol+":/volume", "alpine:3.20", "sh", "-c", "chmod 750 /volume && mkdir /volume/existing && chmod 755 /volume/existing && chown 7:7 /volume/existing"); err != nil {
		t.Fatal(err)
	}
	// Archive written by uid 501 with a private file, as a Mac would produce.
	archive := testTarGzip(t, []*tar.Header{
		{Name: "pkg/", Typeflag: tar.TypeDir, Mode: 0o755, Uid: 501, Gid: 20},
		{Name: "pkg/index.js", Typeflag: tar.TypeReg, Mode: 0o644, Size: 5, Uid: 501, Gid: 20},
		{Name: "pkg/lib/deep.js", Typeflag: tar.TypeReg, Mode: 0o600, Size: 4, Uid: 501, Gid: 20},
	}, [][]byte{nil, []byte("hello"), []byte("deep")})
	d := LocalDocker{}
	for _, rel := range []string{"existing", "new/nested", "."} {
		if err := d.ImportVolumeArchive(ctx, vol, rel, archive, "1000:1000"); err != nil {
			t.Fatalf("import into %s: %v", rel, err)
		}
	}
	if err := d.ImportVolumeArchive(ctx, vol, "plain", archive, ""); err != nil {
		t.Fatal(err)
	}
	out, err := docker(ctx, "run", "--rm", "-v", vol+":/volume", "alpine:3.20", "sh", "-c",
		`cd /volume && for p in . existing existing/pkg existing/pkg/index.js existing/pkg/lib existing/pkg/lib/deep.js new new/nested new/nested/pkg pkg plain plain/pkg plain/pkg/index.js; do printf '%s %s\n' "$p" "$(stat -c '%a %u:%g' "$p")"; done`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		p, attrs, _ := strings.Cut(line, " ")
		got[p] = attrs
	}
	for p, want := range map[string]string{
		".":                        "750 0:0", // the volume root keeps its own mode
		"existing":                 "755 7:7", // the target keeps its own mode and owner
		"existing/pkg":             "755 1000:1000",
		"existing/pkg/index.js":    "644 1000:1000",
		"existing/pkg/lib":         "755 1000:1000", // implicit folder: readable, owned
		"existing/pkg/lib/deep.js": "600 1000:1000", // archived mode kept
		"new":                      "755 1000:1000", // created parents
		"new/nested":               "755 1000:1000",
		"new/nested/pkg":           "755 1000:1000",
		"pkg":                      "755 1000:1000",
		"plain":                    "755 0:0",    // no owner: created parent stays root but readable
		"plain/pkg":                "755 501:20", // no owner: archive owner kept (unchanged behaviour)
		"plain/pkg/index.js":       "644 501:20",
	} {
		if got[p] != want {
			t.Fatalf("%s = %q, want %q\n%s", p, got[p], want, out)
		}
	}
}

func TestDockerStandaloneVolumeSharedByWorkloads(t *testing.T) {
	a, db, ctx := sandboxApp(t)
	owner := ownerIdentity{}
	v, _, err := a.createStandaloneVolume(ctx, nil, db, owner, "test-cache-"+strings.ToLower(newExecutionID()[4:10]), map[string]string{"purpose": "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = a.deleteStandaloneVolume(context.Background(), nil, db, owner, v.ID)
		_, _ = docker(context.Background(), "volume", "rm", "-f", v.DockerVolumeName)
	})
	if v.LastUsedAt != "" {
		t.Fatalf("new volume already used: %q", v.LastUsedAt)
	}
	writer, err := a.createWorkload(ctx, nil, db, RunSpec{Name: "vol-writer", Image: sandboxTestImage, Command: []string{"sleep", "infinity"}, UseLocal: true, RestartPolicy: "no",
		Volumes: []VolumeSpec{{Name: "cache", MountPath: "/cache", VolumeID: v.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := a.createWorkload(ctx, nil, db, RunSpec{Name: "vol-reader", Image: sandboxTestImage, Command: []string{"sleep", "infinity"}, UseLocal: true, RestartPolicy: "no",
		Volumes: []VolumeSpec{{Name: "cache", MountPath: "/cache", VolumeID: v.ID, ReadOnly: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if out, ok := dexec(ctx, writer, "", "sh", "-c", "echo cached > /cache/item"); !ok {
		t.Fatal(out)
	}
	if out, ok := dexec(ctx, reader, "", "cat", "/cache/item"); !ok || out != "cached" {
		t.Fatalf("reader sees %q", out)
	}
	if out, ok := dexec(ctx, reader, "", "sh", "-c", "echo x > /cache/other"); ok || !strings.Contains(out, "Read-only") {
		t.Fatalf("read-only mount accepted a write: %q", out)
	}
	got, err := a.describeStandaloneVolume(ctx, nil, db, v.ID, true)
	if err != nil || len(got.MountedBy) != 2 || got.LastUsedAt == "" || got.SizeBytes <= 0 {
		t.Fatalf("describe: %v %+v", err, got)
	}
	if err := a.deleteStandaloneVolume(ctx, nil, db, owner, v.ID); err == nil || !strings.Contains(err.Error(), "mounted by") {
		t.Fatalf("delete while mounted: %v", err)
	}
	for _, w := range []*Workload{writer, reader} {
		if err := a.destroyWorkload(ctx, nil, db, w.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	// The volume and its data outlive both workloads, even with delete_volumes.
	if out, err := docker(ctx, "run", "--rm", "-v", v.DockerVolumeName+":/cache", "alpine:3.20", "cat", "/cache/item"); err != nil || strings.TrimSpace(out) != "cached" {
		t.Fatalf("data after destroy: %q %v", out, err)
	}
	// A container Docker still knows about (outside the app) also blocks delete.
	blocker := "containers-test-blocker-" + strings.ToLower(newExecutionID()[4:12])
	if _, err := docker(ctx, "create", "--name", blocker, "-v", v.DockerVolumeName+":/x", "alpine:3.20", "true"); err != nil {
		t.Fatal(err)
	}
	if err := a.deleteStandaloneVolume(ctx, nil, db, owner, v.ID); err == nil || !strings.Contains(err.Error(), blocker) {
		t.Fatalf("delete with a stopped Docker container attached: %v", err)
	}
	_, _ = docker(ctx, "rm", "-f", blocker)
	if err := a.deleteStandaloneVolume(ctx, nil, db, owner, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := docker(ctx, "volume", "inspect", v.DockerVolumeName); !dockerMissing(err) {
		t.Fatalf("docker volume still present: %v", err)
	}
}

// Archives often list files without their folders. Importing them must not
// change the owner or mode of folders that already exist along the path.
func TestDockerImportLeavesExistingSubfoldersAlone(t *testing.T) {
	_, _, ctx := sandboxApp(t)
	vol := "containers-test-import-" + strings.ToLower(newExecutionID()[4:12])
	if _, err := docker(ctx, "volume", "create", vol); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = docker(context.Background(), "volume", "rm", "-f", vol) })
	if _, err := docker(ctx, "run", "--rm", "-v", vol+":/volume", "alpine:3.20", "sh", "-c",
		"mkdir -p /volume/src/lib /volume/src/old && chown -R 1000:1000 /volume/src && chmod 750 /volume/src /volume/src/lib && echo stale > /volume/src/lib/b.go && echo keep > /volume/src/old/k"); err != nil {
		t.Fatal(err)
	}
	fileOnly := testTarGzip(t, []*tar.Header{
		{Name: "src/lib/b.go", Typeflag: tar.TypeReg, Mode: 0o644, Size: 6, Uid: 501, Gid: 20},
		{Name: "src/fresh/c.go", Typeflag: tar.TypeReg, Mode: 0o644, Size: 6, Uid: 501, Gid: 20},
	}, [][]byte{[]byte("new b\n"), []byte("new c\n")})
	withDirs := testTarGzip(t, []*tar.Header{
		{Name: "src/", Typeflag: tar.TypeDir, Mode: 0o777, Uid: 0, Gid: 0},
		{Name: "src/made/", Typeflag: tar.TypeDir, Mode: 0o700, Uid: 501, Gid: 20},
		{Name: "src/made/d.go", Typeflag: tar.TypeReg, Mode: 0o600, Size: 2, Uid: 501, Gid: 20},
	}, [][]byte{nil, nil, []byte("d\n")})
	d := LocalDocker{}
	if err := d.ImportVolumeArchive(ctx, vol, ".", fileOnly, ""); err != nil {
		t.Fatal(err)
	}
	if err := d.ImportVolumeArchive(ctx, vol, ".", fileOnly, "1000:1000"); err != nil {
		t.Fatal(err)
	}
	if err := d.ImportVolumeArchive(ctx, vol, ".", withDirs, ""); err != nil {
		t.Fatal(err)
	}
	out, err := docker(ctx, "run", "--rm", "-v", vol+":/volume", "alpine:3.20", "sh", "-c",
		`cd /volume && for p in src src/lib src/lib/b.go src/fresh src/fresh/c.go src/old/k src/made src/made/d.go; do printf '%s %s\n' "$p" "$(stat -c '%a %u:%g' "$p")"; done; cat src/lib/b.go`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines[:len(lines)-1] {
		p, attrs, _ := strings.Cut(line, " ")
		got[p] = attrs
	}
	for p, want := range map[string]string{
		"src":            "750 1000:1000", // listed as 0777 root in the last archive: still untouched
		"src/lib":        "750 1000:1000",
		"src/lib/b.go":   "644 1000:1000", // replaced by the owner import
		"src/fresh":      "755 0:0",       // created by the first import (no owner), not rewritten later
		"src/fresh/c.go": "644 1000:1000",
		"src/old/k":      "644 0:0",    // unrelated files remain
		"src/made":       "700 501:20", // new folder takes its archived mode and owner
		"src/made/d.go":  "600 501:20",
	} {
		if got[p] != want {
			t.Fatalf("%s = %q, want %q\n%s", p, got[p], want, out)
		}
	}
	if lines[len(lines)-1] != "new b" {
		t.Fatalf("file not replaced: %q", lines[len(lines)-1])
	}
}
