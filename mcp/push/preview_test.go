package main

import (
	"encoding/json"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPreviewReachesAPNsWithoutChangingRouting(t *testing.T) {
	p := &credentialPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
	badge := 2
	d := &delivery{Type: "notification", ItemID: "401", InstanceRef: "instance-a", ProjectID: "project-a", Badge: &badge, Preview: &pushPreview{Title: "Info in Kimi", Body: "Second test alert requested by the user. No action is required."}}
	if _, err := (apnsProvider{}).send(ctx, "token", &device{BundleID: "com.apteva.ios", Environment: "production"}, d); err != nil {
		t.Fatal(err)
	}
	aps := p.executeInput["aps"].(map[string]any)
	alert := aps["alert"].(map[string]string)
	if alert["title"] != d.Preview.Title || alert["body"] != d.Preview.Body || aps["badge"] != 2 {
		t.Fatal(aps)
	}
	data := p.executeInput["data"].(map[string]any)
	if data["item_id"] != "401" || data["instance_ref"] != "instance-a" || data["project_id"] != "project-a" {
		t.Fatal(data)
	}
	d.Preview = nil
	if _, err := (apnsProvider{}).send(ctx, "token", &device{BundleID: "com.apteva.ios", Environment: "production"}, d); err != nil {
		t.Fatal(err)
	}
	alert = p.executeInput["aps"].(map[string]any)["alert"].(map[string]string)
	if alert["title"] != "New app notification" {
		t.Fatal(alert)
	}
}

func TestPreviewDeliveryDoesNotPersistOrExposeText(t *testing.T) {
	h := newHarness(t)
	r := h.request(t, "POST", "/v1/devices/register", "", map[string]any{"provider_token": strings.Repeat("a", 64), "platform": "ios", "bundle_id": "com.apteva.ios", "environment": "production", "instance_ref": "instance-a"})
	registration := decodeResponse[struct {
		Device device `json:"device"`
		Grant  string `json:"grant"`
	}](t, r)
	input := map[string]any{"device_id": registration.Device.ID, "type": "notification", "item_id": "401", "idempotency_key": "preview-401", "preview": map[string]string{"title": "Private title", "body": "Private body"}}
	r = h.request(t, "POST", "/v1/deliveries", registration.Grant, input)
	if r.Code != http.StatusCreated {
		t.Fatal(r.Code, r.Body)
	}
	if len(h.caller.calls) != 1 || h.caller.calls[0].delivery.Preview == nil {
		t.Fatal(h.caller.calls)
	}
	if strings.Contains(r.Body.String(), "Private") || strings.Contains(r.Body.String(), `"preview":`) {
		t.Fatal("response leaked content", r.Body)
	}
	id := decodeResponse[delivery](t, r).ID
	stored, err := h.app.store.deliveryByID(id)
	if err != nil || stored.Preview != nil {
		t.Fatal("persisted preview", stored, err)
	}
	r = h.request(t, "POST", "/v1/deliveries", registration.Grant, input)
	if r.Code != 200 || len(h.caller.calls) != 1 {
		t.Fatal("duplicate dispatched again")
	}
	input["idempotency_key"] = "bad"
	input["preview"] = map[string]string{"title": "  ", "body": "private"}
	r = h.request(t, "POST", "/v1/deliveries", registration.Grant, input)
	if r.Code != 400 {
		t.Fatal(r.Code, r.Body)
	}
}

func TestPreviewIsBoundedSanitizedAndOptional(t *testing.T) {
	if p, e := normalizePreview(nil); p != nil || e != nil {
		t.Fatal(p, e)
	}
	for _, text := range []string{strings.Repeat("😀", 1000), strings.Repeat("<>&", 1000), strings.Repeat("\n\x00a", 1000)} {
		p, err := normalizePreview(&pushPreview{Title: "Info", Body: text})
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(p)
		if len(encoded) > 2400 || len(p.Body) > 1024 || !utf8.ValidString(p.Body) || strings.ContainsAny(p.Body, "\n\x00") {
			t.Fatal("unsafe preview", len(encoded))
		}
	}
}
