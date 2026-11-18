package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExecutionUserAndUmaskValidation(t *testing.T) {
	plain := &Workload{}
	hardened := &Workload{Hardened: true}
	ok := func(w *Workload, user, umask string) executionInput {
		t.Helper()
		in, err := normalizeExecutionInput(executionInput{Argv: []string{"true"}, User: user, Umask: umask}, w, 60)
		if err != nil {
			t.Fatalf("user=%q umask=%q: %v", user, umask, err)
		}
		return in
	}
	bad := func(w *Workload, user, umask, want string) {
		t.Helper()
		_, err := normalizeExecutionInput(executionInput{Argv: []string{"true"}, User: user, Umask: umask}, w, 60)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("user=%q umask=%q: err=%v want %q", user, umask, err, want)
		}
	}
	if in := ok(plain, "", ""); in.User != "" || in.Umask != "" {
		t.Fatalf("defaults changed: %+v", in)
	}
	ok(plain, "node", "")
	ok(plain, "1000:1000", "")
	ok(plain, "root", "") // only hardened workloads forbid root
	bad(plain, "", "22", "umask")
	for in, want := range map[string]string{"022": "0022", "0027": "0027", "777": "0777"} {
		if got := ok(plain, "", in).Umask; got != want {
			t.Fatalf("umask %q normalized to %q, want %q", in, got, want)
		}
	}
	bad(plain, "a b", "", "user must be")
	bad(plain, "x;rm", "", "user must be")
	bad(hardened, "root", "", "cannot run as root")
	bad(hardened, "0:0", "", "cannot run as root")
	ok(hardened, "2000:2000", "")
	bad(plain, "", "0888", "umask")
	bad(plain, "", "u=rwx", "umask")
	bad(plain, "", "-022", "umask")
}

func TestPersistentShellCommandAppliesUmask(t *testing.T) {
	cmd := persistentShellCommand(executionRuntimeSpec{Argv: []string{"touch", "f"}, Umask: "0022"}, "rt")
	if !strings.Contains(cmd, "umask 0022\n") || strings.Index(cmd, "umask") > strings.Index(cmd, "'touch'") {
		t.Fatalf("session command: %q", cmd)
	}
	if strings.Contains(persistentShellCommand(executionRuntimeSpec{Argv: []string{"true"}}, "rt"), "umask") {
		t.Fatal("umask written without being requested")
	}
}

func TestExecAsAddsUserOnlyWhenSet(t *testing.T) {
	if got := strings.Join(execAs("", "c", "id"), " "); got != "exec c id" {
		t.Fatal(got)
	}
	if got := strings.Join(execAs("1000:1000", "c", "id"), " "); got != "exec --user 1000:1000 c id" {
		t.Fatal(got)
	}
}

func TestImportOwnerValidation(t *testing.T) {
	a := &App{}
	w := &Workload{Volumes: []VolumeSpec{{Name: "data", MountPath: "/data", DockerVolumeName: "v"}}}
	for _, owner := range []string{"node", "1000", "1000:node", "-1:0", "1000:1000:1"} {
		if _, err := a.importVolumeArchive(nil, w, "data", "", "", owner); err == nil || !strings.Contains(err.Error(), "owner must be numeric") {
			t.Fatalf("owner %q: %v", owner, err)
		}
	}
}

func TestStandaloneVolumeSpecValidation(t *testing.T) {
	for _, v := range []VolumeSpec{
		{Name: "c", MountPath: "/c", VolumeID: "vol_1", SharedFrom: "wrk_1", ReadOnly: true},
		{Name: "c", MountPath: "/c", VolumeID: "vol_1", RetainedFrom: "wrk_1"},
	} {
		if _, err := normalizeRunSpec(RunSpec{Name: "x", Image: "alpine", Volumes: []VolumeSpec{v}}); err == nil || !strings.Contains(err.Error(), "volume_id cannot be combined") {
			t.Fatalf("%+v: %v", v, err)
		}
	}
	if _, err := normalizeLabels(map[string]string{"bad key": "x"}); err == nil {
		t.Fatal("label key with space accepted")
	}
	if _, err := normalizeLabels(map[string]string{"k": "a\nb"}); err == nil {
		t.Fatal("multi-line label value accepted")
	}
}

func TestStandaloneVolumeOwnershipAndMountGuard(t *testing.T) {
	db := testDB(t)
	a := &App{backend: fakeVolumeBackend{}}
	ctx := context.Background()
	appA := ownerIdentity{InstallID: 7, AppName: "workspaces", ProjectID: "p1"}
	appB := ownerIdentity{InstallID: 8, AppName: "other", ProjectID: "p1"}

	v, existing, err := a.createStandaloneVolume(ctx, nil, db, appA, "npm-cache", map[string]string{"kind": "cache"})
	if err != nil || existing {
		t.Fatalf("create: %v existing=%v", err, existing)
	}
	again, existing, err := a.createStandaloneVolume(ctx, nil, db, appA, "npm-cache", nil)
	if err != nil || !existing || again.ID != v.ID {
		t.Fatalf("create is not idempotent: %v %v %+v", err, existing, again)
	}
	if other, _, err := a.createStandaloneVolume(ctx, nil, db, appB, "npm-cache", nil); err != nil || other.ID == v.ID {
		t.Fatalf("same name for another app should be a separate volume: %v", err)
	}

	list, err := a.listStandaloneVolumes(ctx, nil, db, appA, map[string]string{"kind": "cache"}, "", false)
	if err != nil || len(list) != 1 || list[0].ID != v.ID {
		t.Fatalf("label filter: %v %+v", err, list)
	}
	if list, _ := a.listStandaloneVolumes(ctx, nil, db, appA, map[string]string{"kind": "other"}, "", false); len(list) != 0 {
		t.Fatalf("label filter matched wrong volume: %+v", list)
	}

	if _, err := standaloneVolumeForRun(db, v.ID, appB, 0); !errors.Is(err, errVolumeNotFound) {
		t.Fatalf("other app resolved the volume: %v", err)
	}
	if _, err := standaloneVolumeForRun(db, v.ID, ownerIdentity{InstallID: 7, AppName: "workspaces", ProjectID: "p2"}, 0); !errors.Is(err, errVolumeNotFound) {
		t.Fatalf("other project resolved the volume: %v", err)
	}
	if _, err := standaloneVolumeForRun(db, v.ID, appA, 3); err == nil || !strings.Contains(err.Error(), "local workloads only") {
		t.Fatalf("remote mount accepted: %v", err)
	}

	w, _, err := a.prepareOwnedWorkload(nil, db, RunSpec{Name: "user1", Image: "alpine", UseLocal: true, Volumes: []VolumeSpec{{Name: "cache", MountPath: "/cache", VolumeID: v.ID}}}, appA)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.deleteStandaloneVolume(ctx, nil, db, appA, v.ID); !errors.Is(err, errConflict) || !strings.Contains(err.Error(), w.ID) {
		t.Fatalf("delete while mounted: %v", err)
	}
	if err := a.deleteStandaloneVolume(ctx, nil, db, appB, v.ID); !errors.Is(err, errVolumeNotFound) {
		t.Fatalf("other app deleted the volume: %v", err)
	}
	if _, err := db.Exec(`UPDATE containers_workloads SET status='destroyed' WHERE id=?`, w.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.deleteStandaloneVolume(ctx, nil, db, appA, v.ID); err != nil {
		t.Fatalf("delete after unmount: %v", err)
	}
}

// fakeVolumeBackend is a DockerBackend that only tracks volumes.
type fakeVolumeBackend struct{ DockerBackend }

func (fakeVolumeBackend) EnsureVolume(context.Context, string) (bool, error) { return true, nil }
func (fakeVolumeBackend) RemoveVolume(context.Context, string) error         { return nil }
func (fakeVolumeBackend) VolumeUsage(context.Context, string) (int64, error) { return 42, nil }
