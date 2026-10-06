package main

import (
	"errors"
	sdk "github.com/apteva/app-sdk"
	"testing"
)

func TestExecutionPermissionIsRepositoryScopedAndRequiresConfirmation(t *testing.T) {
	a, ctx, r := reliabilityApp(t)
	m := (&App{}).Manifest()
	ctx = sdk.NewAppCtxForTest(&m, ctx.AppDB(), nil, nil, nil)
	if _, err := a.configureExecution(ctx, r, true, false, "agent:1"); err == nil {
		t.Fatal("enabled without confirmation")
	}
	if _, err := a.configureExecution(ctx, r, true, true, "agent:1"); err != nil {
		t.Fatal(err)
	}
	p, err := a.executionPermission(ctx, r)
	if err != nil || !p.Enabled || p.Source != "repository" {
		t.Fatalf("permission=%+v err=%v", p, err)
	}
	if err := requireLocalExecution(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := a.configureExecution(ctx, r, false, false, "agent:1"); err != nil {
		t.Fatal(err)
	}
	if err := requireLocalExecution(ctx, r); !errors.Is(err, errLocalExecutionPermission) {
		t.Fatalf("revoke err=%v", err)
	}
}
