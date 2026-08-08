package main

import (
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestRegisterExternalHostGeneratesDedicatedKey(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-1"))
	result, err := (&App{}).toolRegister(ctx, map[string]any{
		"name": "mac-studio", "ssh_host": "mac.example.test", "ssh_user": "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	instance := out["instance"].(*Instance)
	if instance.Provider != "external" || instance.Status != "provisioning" || instance.SSHPort != 22 {
		t.Fatalf("unexpected external instance: %+v", instance)
	}
	if instance.SSHPrivateKey != "" {
		t.Fatal("private key leaked in registration response")
	}
	authorization := out["authorization"].(map[string]any)
	publicKey, _ := authorization["public_key"].(string)
	if !strings.HasPrefix(publicKey, "ssh-ed25519 ") {
		t.Fatalf("public key=%q", publicKey)
	}
	stored, err := dbGetInstance(ctx.AppDB(), instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SSHPrivateKey == "" || stored.SSHPublicKey != publicKey {
		t.Fatal("generated keypair was not retained server-side")
	}
	capabilities := instanceCapabilities(stored)
	if !capabilities.Run || !capabilities.Tunnel || !capabilities.Destroy || capabilities.Upgrade {
		t.Fatalf("external capabilities=%+v", capabilities)
	}
}

func TestRegisterExternalHostRejectsShellLikeHost(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	_, err := (&App{}).toolRegister(ctx, map[string]any{
		"name": "bad", "ssh_host": "host; reboot", "ssh_user": "runner",
	})
	if err == nil {
		t.Fatal("expected invalid host error")
	}
}
