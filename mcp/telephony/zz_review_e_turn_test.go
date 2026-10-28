package main

import (
	"net/url"
	"testing"
)

// Sessions without a user identity (operator, API key, leased runtime) used to
// bypass the override gate, so ?ice_policy=all defeated a relay-required project.
func TestReviewETURNOverridesNeverWeakenProjectPolicy(t *testing.T) {
	a, ctx, _ := turnTestFixture(t)
	phoneTestPolicy(t, a)
	saveTURNTestPolicy(t, a, ctx, nil, phoneTURNPolicy{Source: "app", ICEPolicy: "relay", Transport: "tls", AllowUserOverrides: true})
	alice := phoneTestIdentity("alice")
	for name, identity := range map[string]phoneIdentity{"no principal": {}, "user": alice} {
		if p, err := a.resolvedTURNPolicy("project-a", identity, url.Values{"ice_policy": {"all"}}); err == nil {
			t.Fatalf("%s: relay weakened to %q", name, p.ICEPolicy)
		}
		if _, err := a.resolvedTURNPolicy("project-a", identity, url.Values{"turn_transport": {"udp"}}); err == nil {
			t.Fatalf("%s: pinned transport changed", name)
		}
		if p, err := a.resolvedTURNPolicy("project-a", identity, url.Values{"ice_policy": {"relay"}, "turn_transport": {"tls"}, "turn_tls_port": {"443"}}); err != nil || p.ICEPolicy != "relay" || p.TLSPort != 443 {
			t.Fatalf("%s: non-weakening override rejected: %+v %v", name, p, err)
		}
	}
	// A permissive default may still be narrowed by any session.
	saveTURNTestPolicy(t, a, ctx, nil, phoneTURNPolicy{Source: "app"})
	if p, err := a.resolvedTURNPolicy("project-a", phoneIdentity{}, url.Values{"ice_policy": {"relay"}}); err != nil || p.ICEPolicy != "relay" {
		t.Fatalf("narrowing override rejected: %+v %v", p, err)
	}
}
