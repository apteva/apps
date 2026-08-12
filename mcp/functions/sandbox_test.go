package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if maybeRunSandboxHelper() {
		return
	}
	os.Exit(m.Run())
}

func TestBuildEnvironmentScrubsSidecarCredentials(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "must-not-leak")
	t.Setenv("APTEVA_GATEWAY_URL", "https://internal.invalid")
	dir := t.TempDir()
	env := buildCmdEnv(dir, dir)
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"APTEVA_APP_TOKEN", "APTEVA_GATEWAY_URL", "must-not-leak"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("build environment leaked %s: %s", forbidden, joined)
		}
	}
}

func TestWorkerEnvironmentProtectsReservedKeys(t *testing.T) {
	fn := &Function{ID: 1, Name: "safe", Runtime: "node", MaxMemoryMB: 64, Env: map[string]string{
		"VISIBLE": "yes", "HOME": "/root", "NODE_OPTIONS": "--require=/evil",
		"LD_PRELOAD": "/evil.so", "APTEVA_APP_TOKEN": "stolen",
	}}
	joined := strings.Join(workerEnv(fn, "/artifact/entry.mjs", "/private/tmp/fn"), "\n")
	for _, forbidden := range []string{"/root", "/evil", "stolen"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("worker environment accepted reserved value %q: %s", forbidden, joined)
		}
	}
	if !strings.Contains(joined, "VISIBLE=yes") || !strings.Contains(joined, "NODE_OPTIONS=--max-old-space-size=64") {
		t.Fatalf("worker environment missing expected values: %s", joined)
	}
}

func TestGlobalQueueRejectsInsteadOfGrowingUnbounded(t *testing.T) {
	p := &pool{
		byFn: map[int64]*fnPool{}, globalQueue: make(chan struct{}, 1),
		globalSem: make(chan struct{}, 1), buildSem: make(chan struct{}, 1),
	}
	p.globalQueue <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := p.invoke(nil, ctx, &Function{ID: 7}, &FunctionVersion{}, runtimeSpec{}, "", nil, time.Second, nil)
	if !errors.Is(err, errFunctionBusy) {
		t.Fatalf("invoke error = %v, want errFunctionBusy", err)
	}
}
