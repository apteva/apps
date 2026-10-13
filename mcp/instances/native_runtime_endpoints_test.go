package main

import "testing"

func TestNativePublicEndpointValidation(t *testing.T) {
	for _, raw := range []string{"http://voice.test", "https://user:password@voice.test", "https://voice.test?token=secret", "https://voice.test/#fragment", "https://voice.test/a/%2e%2e/b", "https://voice.test/a\\b", "https:///missing-host"} {
		if nativePublicEndpoints(nativeOptions{Endpoints: map[string]string{"browser": raw}}) == nil {
			t.Errorf("accepted unsafe endpoint %q", raw)
		}
	}
	if err := nativePublicEndpoints(nativeOptions{BrowserEndpoint: "https://voice.test/scoped", Endpoints: map[string]string{"browser": "https://voice.test", "media": "wss://voice.test"}}); err != nil {
		t.Fatal(err)
	}
}

func TestNativePublicEndpointsPersistWithoutChangingControl(t *testing.T) {
	b, _, req := newNativeTest(t)
	req.Options["browser_endpoint"] = "https://voice.test/scoped"
	req.Options["endpoints"] = map[string]string{"browser": "https://voice.test", "media": "https://voice.test"}
	out := prepareNativeTest(t, b, req)
	if out.BrowserEndpoint != "https://voice.test/scoped" || out.Endpoints["browser"] != "https://voice.test" || out.Endpoint == "" || out.Endpoint == out.BrowserEndpoint {
		t.Fatalf("bad endpoint handoff: %+v", out)
	}
	record, err := b.record(req.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Options.Endpoints["media"] != "https://voice.test" || record.Execution.BrowserEndpoint != out.BrowserEndpoint {
		t.Fatal("endpoints not persisted")
	}
	req.Options["browser_endpoint"] = "https://other.test/scoped"
	_, w := nativeCall(t, b, "prepare", req)
	if w.Code != 409 {
		t.Fatalf("retry changed endpoints: %d %s", w.Code, w.Body.String())
	}
}
