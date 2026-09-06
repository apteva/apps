package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestHostedStopRetriesOnlyReadOnlyInspection(t *testing.T) {
	p := &hostedStopPlatform{replies: []hostedStopReply{
		{exitCode: -1, errText: "lost stop response"},
		{exitCode: -1, errText: "lost verification response"},
		{output: "FLEET_STOP_STATE stopped", exitCode: 0},
	}}
	_, ctx := newTestApp(t, tk.WithPlatform(p))
	if err := stopHostedTenant(ctx, 3, "acme", 7100, time.Second); err != nil {
		t.Fatal(err)
	}
	if p.calls != 3 {
		t.Fatalf("calls=%d", p.calls)
	}
	for i, cmd := range p.commands {
		want := "ACTION='inspect'"
		if i == 0 {
			want = "ACTION='stop'"
		}
		if !strings.Contains(cmd, want) {
			t.Fatalf("unexpected command %d, want %s", i, want)
		}
	}
}

func TestHostedStopDoesNotTrustUnacknowledgedStoppedMarker(t *testing.T) {
	p := &hostedStopPlatform{replies: []hostedStopReply{
		{exitCode: -1, errText: "lost stop response"},
		{output: "FLEET_STOP_STATE stopped", exitCode: -1, errText: "lost verification response"},
	}}
	_, ctx := newTestApp(t, tk.WithPlatform(p))
	if err := stopHostedTenant(ctx, 3, "acme", 7100, time.Second); !errors.Is(err, errHostedStopIndeterminate) {
		t.Fatalf("unsafe stop accepted: %v", err)
	}
	if p.calls != 2 {
		t.Fatal("ambiguous state should remain fenced")
	}
}
