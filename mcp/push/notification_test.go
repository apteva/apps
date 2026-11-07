package main

import (
	"database/sql"
	"net/http"
	"os"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestGenericNotificationDeliveryAndIdempotency(t *testing.T) {
	h := newHarness(t)
	r := h.request(t, http.MethodPost, "/v1/devices/register", "", map[string]any{
		"provider_token": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"platform":       "ios", "bundle_id": "com.apteva.ios", "environment": "production",
		"instance_ref": "local-instance",
	})
	if r.Code != http.StatusCreated {
		t.Fatalf("registration: %d %s", r.Code, r.Body)
	}
	registration := decodeResponse[struct {
		Device device `json:"device"`
		Grant  string `json:"grant"`
	}](t, r)
	input := map[string]any{"device_id": registration.Device.ID, "type": "notification",
		"item_id": "17", "project_id": "project-a", "badge": 5, "idempotency_key": "notification-17"}
	for i := 0; i < 2; i++ {
		r = h.request(t, http.MethodPost, "/v1/deliveries", registration.Grant, input)
		want := http.StatusCreated
		if i > 0 {
			want = http.StatusOK
		}
		if r.Code != want {
			t.Fatalf("delivery %d: %d %s", i, r.Code, r.Body)
		}
		d := decodeResponse[delivery](t, r)
		if d.Status != "sent" || d.ItemID != "17" || d.ProjectID != "project-a" || d.Badge == nil || *d.Badge != 5 {
			t.Fatalf("delivery: %+v", d)
		}
	}
	if len(h.caller.calls) != 1 || h.caller.calls[0].delivery.InstanceRef != "local-instance" {
		t.Fatalf("provider calls: %+v", h.caller.calls)
	}
	title, body := notificationCopy("notification")
	if title != "New app notification" || body != "Open Apteva to view it." {
		t.Fatalf("unsafe copy %q %q", title, body)
	}
}

func TestGenericNotificationAPNsRouting(t *testing.T) {
	p := &credentialPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(p))
	badge := 5
	_, err := (apnsProvider{}).send(ctx, "token", &device{BundleID: "com.apteva.ios", Environment: "production"},
		&delivery{Type: "notification", InstanceRef: "instance-a", ProjectID: "project-a", ItemID: "17", Badge: &badge})
	if err != nil {
		t.Fatal(err)
	}
	data := p.executeInput["data"].(map[string]any)
	for key, want := range map[string]string{"type": "notification", "instance_ref": "instance-a", "project_id": "project-a", "item_id": "17"} {
		if data[key] != want {
			t.Fatalf("%s=%v", key, data[key])
		}
	}
	aps := p.executeInput["aps"].(map[string]any)
	if aps["badge"] != 5 {
		t.Fatalf("badge=%v", aps["badge"])
	}
}

func TestNotificationMigrationPreservesLegacyData(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, name := range []string{"001_init.sql", "002_device_routing.sql", "003_delivery_project.sql", "004_relay_domain.sql"} {
		body, err := os.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO devices(id, token_ciphertext, token_hash, platform, last_seen_at, created_at, bundle_id, environment)
		VALUES('d','encrypted-token','token-hash','ios','now','then','com.apteva.ios','production');
		INSERT INTO grants(id,device_id,instance_ref,secret_hash,expires_at,created_at)
		VALUES('g','d','instance-a','grant-hash','2099-01-01T00:00:00Z','then');
		INSERT INTO deliveries(id,grant_id,device_id,idempotency_key,type,item_id,badge,status,provider_id,error,created_at,sent_at,project_id)
		VALUES('old','g','d','legacy-key','alert','42',5,'sent','apns-id','','then','later','project-a');`)
	if err != nil {
		t.Fatal(err)
	}
	var before string
	query := `SELECT json_object('id',id,'grant',grant_id,'device',device_id,'key',idempotency_key,'type',type,'item',item_id,'badge',badge,'status',status,'provider',provider_id,'error',error,'created',created_at,'sent',sent_at,'project',project_id) FROM deliveries WHERE id='old'`
	if err := db.QueryRow(query).Scan(&before); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("migrations/005_generic_notifications.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var after, cipher, secret string
	if err := db.QueryRow(query).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("delivery changed: %s -> %s", before, after)
	}
	if err := db.QueryRow(`SELECT token_ciphertext,secret_hash FROM devices JOIN grants ON device_id=devices.id`).Scan(&cipher, &secret); err != nil {
		t.Fatal(err)
	}
	if cipher != "encrypted-token" || secret != "grant-hash" {
		t.Fatal("device/grant changed")
	}
	if _, err := db.Exec(`INSERT INTO deliveries(id,grant_id,device_id,idempotency_key,type,created_at) VALUES('new','g','d','new-key','notification','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deliveries(id,grant_id,device_id,idempotency_key,type,created_at) VALUES('duplicate','g','d','legacy-key','notification','now')`); err == nil {
		t.Fatal("lost idempotency constraint")
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
	rows.Close()
	if _, err := db.Exec(`DELETE FROM devices WHERE id='d'`); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM grants)+(SELECT count(*) FROM deliveries)`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("cascade broken: %d", remaining)
	}
}
