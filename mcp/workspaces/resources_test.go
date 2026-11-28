package main

import "testing"

func TestParseCgroupLimits(t *testing.T) {
	for text, want := range map[string]float64{"100000 100000": 1, "150000 100000": 1.5, "max 100000": 0, "-1 100000": 0, "": 0} {
		got, ok := parseCgroupCPUMax(text)
		if got != want || ok != (want > 0) {
			t.Fatalf("cpu %q = %v %v", text, got, ok)
		}
	}
	for text, want := range map[string]int{"1073741824": 1024, "max": 0, "9223372036854771712": 0, "": 0} {
		got, ok := parseCgroupMemoryMax(text)
		if got != want || ok != (want > 0) {
			t.Fatalf("memory %q = %v %v", text, got, ok)
		}
	}
	if got := parseMeminfoTotalMB("MemTotal:        2048000 kB\nMemFree: 1 kB\n"); got != 2000 {
		t.Fatalf("meminfo = %d", got)
	}
}

func stubSharedResources(t *testing.T, res SharedResources) {
	t.Helper()
	prev := probeSharedResources
	probeSharedResources = func() SharedResources { return res }
	resetSharedResources()
	t.Cleanup(func() { probeSharedResources = prev; resetSharedResources() })
}

func TestLocalWorkspacesReportNoLimitsButSharedResources(t *testing.T) {
	stubSharedResources(t, SharedResources{CPU: 1, MemoryMB: 1024, Source: "container"})
	a, ctx, _ := localWorkspaceFixture(t, true)
	w, err := a.createWorkspace(agentContext("p", 5, "t1", "res-1"), ctx, map[string]any{"name": "local-res", "driver": "local"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if w.CPU != 0 || w.MemoryMB != 0 || w.Capabilities.ResourceLimits {
		t.Fatalf("local limits = %v CPU %d MB enforced=%v", w.CPU, w.MemoryMB, w.Capabilities.ResourceLimits)
	}
	if w.SharedResources == nil || w.SharedResources.CPU != 1 || w.SharedResources.MemoryMB != 1024 || w.SharedResources.Source != "container" {
		t.Fatalf("shared = %+v", w.SharedResources)
	}
	// A row created before 0.12.4 stored the Docker defaults.
	if _, err := ctx.AppDB().Exec(`UPDATE workspaces SET cpu=2, memory_mb=4096 WHERE id=?`, w.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := requireWorkspace(ctx.AppDB(), "p", w.ID)
	if got.CPU != 0 || got.MemoryMB != 0 || got.SharedResources == nil {
		t.Fatalf("old local row = %v CPU %d MB shared=%v", got.CPU, got.MemoryMB, got.SharedResources)
	}
	docker, err := a.createWorkspace(agentContext("p", 5, "t1", "res-2"), ctx, map[string]any{"name": "docker-res", "driver": "containers"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if docker.CPU != 2 || docker.MemoryMB != 4096 || !docker.Capabilities.ResourceLimits || docker.SharedResources != nil {
		t.Fatalf("docker = %v CPU %d MB enforced=%v shared=%v", docker.CPU, docker.MemoryMB, docker.Capabilities.ResourceLimits, docker.SharedResources)
	}
}

func TestReadSharedResourcesIsSane(t *testing.T) {
	res := readSharedResources()
	if res.CPU <= 0 || res.MemoryMB <= 0 || (res.Source != "machine" && res.Source != "container") {
		t.Fatalf("shared resources = %+v", res)
	}
}
