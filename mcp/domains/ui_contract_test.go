package main

import (
	"os"
	"strings"
	"testing"
)

func TestDomainsPanelSafetyContracts(t *testing.T) {
	source, err := os.ReadFile("ui/DomainsPanel.tsx")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		`domain_registration_prepare`,
		`confirmation_token: intent.confirmation_token`,
		`record_id: record.id`,
		`editableRecordValue(record)`,
		`role="dialog"`,
		`flex flex-col md:flex-row`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("DomainsPanel.tsx is missing safety contract %q", required)
		}
	}
}
