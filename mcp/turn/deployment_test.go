package main

import (
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/pion/turn/v5"
)

func TestMissingControlCredentialAbortsStartup(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "")
	if err := (&App{}).OnMount(&sdk.AppCtx{}); err == nil {
		t.Fatal("app started with the SDK development authentication bypass")
	}
}

func assertPoolBound(t *testing.T, a *App) {
	t.Helper()
	for port := relayPortMin; port <= relayPortMax; port++ {
		c, err := net.ListenPacket("udp4", fmt.Sprintf("%s:%d", a.cfg.bindIP, port))
		if err == nil {
			c.Close()
			t.Fatalf("declared port %d is not held by the deployment", port)
		}
	}
}

func TestDeploymentKeepsEveryDeclaredPortBound(t *testing.T) {
	a := startTestRelay(t)
	assertPoolBound(t, a)
	g := &relayGenerator{app: a}
	c, addr, err := g.AllocatePacketConn(turn.AllocateListenerConfig{Network: "udp4", UserID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	port := addr.(*net.UDPAddr).Port
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	assertPoolBound(t, a)
	// A released lease can be reused without dropping the native backend's
	// socket ownership/readiness invariant.
	c, _, err = g.AllocatePacketConn(turn.AllocateListenerConfig{Network: "udp4", UserID: "next-session", RequestedPort: port})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetReadDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.ReadFrom(make([]byte, 100)); err == nil {
		t.Fatal("lease ignored its read deadline")
	}
	_ = c.Close()
	a.mu.Lock()
	a.state = "standby"
	a.mu.Unlock()
	assertPoolBound(t, a)
	w := httptest.NewRecorder()
	a.handleReady(w, httptest.NewRequest("GET", "/ready", nil))
	if w.Code != 200 {
		t.Fatal("passive execution failed readiness")
	}
	if err := a.OnUnmount(nil); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	a.handleReady(w, httptest.NewRequest("GET", "/ready", nil))
	if w.Code != 503 {
		t.Fatal("stopped execution remained ready")
	}
	for port := relayPortMin; port <= relayPortMax; port++ {
		c, err := net.ListenPacket("udp4", fmt.Sprintf("%s:%d", a.cfg.bindIP, port))
		if err != nil {
			t.Fatalf("stop retained port %d: %v", port, err)
		}
		_ = c.Close()
	}
}

func TestPoolConflictFailsReadinessAndReleasesPartialStartup(t *testing.T) {
	a := testApp(t)
	a.cfg.bindIP = "127.0.0.1"
	conflict, err := net.ListenPacket("udp4", fmt.Sprintf("127.0.0.1:%d", relayPortMin+5))
	if err != nil {
		t.Fatal(err)
	}
	defer conflict.Close()
	if err := a.listenNetwork("127.0.0.1:0"); err == nil {
		t.Fatal("app started without its entire declared pool")
	}
	for port := relayPortMin; port < relayPortMin+5; port++ {
		c, err := net.ListenPacket("udp4", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("failed startup leaked port %d", port)
		}
		c.Close()
	}
	if a.status()["ready"].(bool) {
		t.Fatal("partially started relay is ready")
	}
}

func TestStandbyDoesNotAnswerSTUN(t *testing.T) {
	a := startTestRelay(t)
	credential := issueTestCredential(t, a)
	a.mu.Lock()
	a.state = "standby"
	a.mu.Unlock()
	client := newTURNClient(t, a, "udp", credential)
	if _, err := client.SendBindingRequest(); err == nil {
		t.Fatal("standby answered a public STUN request")
	}
}

func TestOwnershipMismatchAndTemporaryOutage(t *testing.T) {
	for _, status := range []int{401, 403, 409} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			a := testApp(t)
			a.managed = true
			a.state = "standby"
			a.deployment = "d"
			a.generation = 1
			now := time.Now()
			own := ownership{Active: true, DeploymentID: "d", Generation: 1, ExpiresAt: now.Unix() + 15}
			if a.applyOwnership(ownership{}, status, errors.New("pending"), now) {
				t.Fatal("first passive startup fenced before ever owning a lease")
			}
			a.armed = true
			a.applyOwnership(own, 200, nil, now)
			if a.applyOwnership(ownership{}, 503, errors.New("temporary outage"), now) {
				t.Fatal("valid lease discarded during temporary failure")
			}
			if !a.applyOwnership(ownership{}, status, errors.New("revoked"), now) {
				t.Fatal("revoked owner not fenced")
			}
			if !a.applyOwnership(own, 200, nil, now) {
				t.Fatal("fenced execution regained ownership")
			}
		})
	}
	a := testApp(t)
	a.managed = true
	a.deployment = "d"
	a.generation = 1
	a.leaseExpiry = time.Now().Add(time.Minute)
	if !a.applyOwnership(ownership{Active: true, DeploymentID: "other", Generation: 1, ExpiresAt: time.Now().Unix() + 15}, 200, nil, time.Now()) {
		t.Fatal("ownership mismatch did not fence")
	}
}
