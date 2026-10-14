package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHostingerRecordsStrictAndCanonical(t *testing.T) {
	raw := json.RawMessage(`{"records":[{"id":"1","name":"@","type":"A","content":"192.0.2.1","ttl":300},{"name":"www","type":"CNAME","content":"example.com","ttl":600}]}`)
	got, err := hostingerRecords(raw, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Name != "example.com" || got[1].Name != "www" {
		t.Fatalf("unexpected names: %+v", got)
	}
	if _, err = hostingerRecords(json.RawMessage(`{"unexpected":true}`), "example.com"); err == nil || !strings.Contains(err.Error(), "records array") {
		t.Fatalf("expected strict parse error, got %v", err)
	}
}
