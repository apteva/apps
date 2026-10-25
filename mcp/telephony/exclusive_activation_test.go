package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestTelephonyExclusiveActivationManifest(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	embedded := (&App{}).Manifest()
	for _, m := range []*sdk.Manifest{disk, &embedded} {
		if m.Runtime.ActivationStrategy != sdk.ActivationExclusive {
			t.Fatal("configurable SIP ports need exclusive activation")
		}
		if m.MinAptevaVersion != "0.94.1" {
			t.Fatalf("activation and managed dashboard routing require the supporting server: %q", m.MinAptevaVersion)
		}
		if m.Runtime.StartupTimeoutSeconds != 60 || m.Runtime.DatabaseUpgrade != "backward_compatible" {
			t.Fatalf("missing bounded readiness/rollback policy: %+v", m.Runtime)
		}
		if err = sdk.ValidateManifest(m); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var restored sdk.Manifest
		if err = json.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		if restored.Runtime.ActivationStrategy != sdk.ActivationExclusive || restored.MinAptevaVersion != m.MinAptevaVersion {
			t.Fatal("persisted manifest lost activation contract")
		}
	}
	if disk.Runtime.ActivationStrategy != embedded.Runtime.ActivationStrategy || disk.Version != embedded.Version || disk.Runtime.Source.Ref != embedded.Runtime.Source.Ref {
		t.Fatal("source and runtime manifests differ")
	}
}

func TestSIPExclusiveActivationOutboundListenerReadiness(t *testing.T) {
	a, ctx, _, _ := outboundSIPFixture(t, "telnyx")
	outboundSetupGatewayFixture(t)
	t.Setenv("TELEPHONY_SIP_ENABLED", "false")
	// HTTP-only defaults must not bind/configure SIP merely because the app's
	// replacement strategy is exclusive.
	if err := a.startSIPGateway(ctx); err != nil || a.directSIPGateway() != nil {
		t.Fatalf("default starts SIP: %v", err)
	}
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	address := blocker.Addr().String()
	t.Setenv("TELEPHONY_SIP_LISTEN", address)
	_, err = a.db().db.Exec(`INSERT INTO outbound_transports(project_id,connection_id,phone_number,provider,transport) VALUES('project-a',10,'','telnyx','sip_direct')`)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.startSIPGateway(ctx); err == nil {
		a.stopSIPGateway()
		t.Fatal("outbound-only mount reported ready despite occupied SIP listener")
	}
	if a.directSIPGateway() != nil {
		t.Fatal("failed listener was retained as ready")
	}
	// The supervisor stops/reaps the previous process before calling OnMount.
	// Model release of that exclusive port, then confirm replacement readiness.
	if err = blocker.Close(); err != nil {
		t.Fatal(err)
	}
	if err = a.startSIPGateway(ctx); err != nil {
		t.Fatal(err)
	}
	if a.directSIPGateway() == nil {
		t.Fatal("outbound-only SIP was not bound before mount completed")
	}
	a.stopSIPGateway()
	// Shutdown must release the configured port so another process can own it.
	probe, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("shutdown retained SIP port %s: %v", address, err)
	}
	if err = probe.Close(); err != nil {
		t.Fatal(fmt.Errorf("close probe: %w", err))
	}
}
