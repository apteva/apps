package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// These tests exercise the transaction contract, not a live airline adapter.
// The .test origin and synthetic data-booking attributes are deliberately
// different from Iberia. No card, reservation or provider network is involved.
func TestFlightBookingTransactionFixtures(t *testing.T) {
	cases := []struct {
		name, field, value, outcome string
		prepare, uncertain          bool
		wantFail                    bool
	}{
		{name: "review_and_commit"},
		{name: "passenger_extras_and_secret_payment", prepare: true},
		{name: "wrong_airport", field: "destination", value: "CDG", wantFail: true},
		{name: "wrong_date", field: "departure_date", value: "2027-11-18", wantFail: true},
		{name: "wrong_flight", field: "flight_number", value: "XX999", wantFail: true},
		{name: "wrong_traveler", field: "passenger_last_name", value: "Other", wantFail: true},
		{name: "wrong_contact", field: "contact_email", value: "other@example.invalid", wantFail: true},
		{name: "wrong_baggage", field: "checked_bags", value: "2", wantFail: true},
		{name: "wrong_seat", field: "seat", value: "12C", wantFail: true},
		{name: "wrong_currency", field: "currency", value: "USD", wantFail: true},
		{name: "wrong_card", field: "payment_last4", value: "4444", wantFail: true},
		{name: "fare_changed", field: "total", value: "101.00", wantFail: true},
		{name: "bad_itemized_sum", field: "payment_fee", value: "5.00", wantFail: true},
		{name: "wrong_button_amount", field: "pay_label", value: "Pay EUR 101.00", wantFail: true},
		{name: "wrong_button_currency", field: "pay_label", value: "Pay USD 100.00", wantFail: true},
		{name: "over_budget", field: "maximum_total", value: "99.00", wantFail: true},
		{name: "declined_payment", outcome: "declined", wantFail: true},
		{name: "three_ds_required", outcome: "requires_action", wantFail: true},
		{name: "unknown_after_submit", outcome: "unknown", wantFail: true},
		{name: "confirmation_amount_changed", outcome: "wrong_receipt_total", wantFail: true},
		{name: "uncertain_click", uncertain: true, wantFail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile("examples/flights/booking-transaction.fixture.json")
			if err != nil {
				t.Fatal(err)
			}
			var definition map[string]any
			if err := json.Unmarshal(raw, &definition); err != nil {
				t.Fatal(err)
			}
			input := map[string]any{
				"provider": "fixture_airline", "origin": "MAD", "destination": "LHR",
				"departure_date": "2027-11-17", "flight_number": "XX717",
				"passenger_first_name": "Fixture", "passenger_last_name": "Traveler",
				"contact_email": "traveler@example.invalid", "checked_bags": "1", "seat": "12A",
				"currency": "EUR", "payment_last4": "1111", "reviewed_total": "100.00",
				"maximum_total": "110.00", "reviewed_pay_label": "Pay EUR 100.00",
				"checkout_url": "https://checkout.fixture.test/checkout", "booking_context_id": "booking-qa",
				"payment_number_ref": "pan", "payment_expiry_ref": "expiry", "payment_cvv_ref": "cvv",
			}
			state := map[string]string{}
			for k, v := range input {
				state[k] = v.(string)
			}
			for k, v := range map[string]string{"fare": "60.00", "taxes": "15.00", "baggage_fee": "10.00", "seat_fee": "15.00", "payment_fee": "0.00", "total": "100.00", "pay_label": "Pay EUR 100.00"} {
				state[k] = v
			}
			if tc.field == "maximum_total" {
				input[tc.field] = tc.value
			} else if tc.field != "" {
				state[tc.field] = tc.value
			}
			if tc.prepare {
				for _, key := range []string{"passenger_first_name", "passenger_last_name", "contact_email", "checked_bags", "seat"} {
					state[key] = ""
				}
			}
			page := input["checkout_url"].(string)
			submitted, clicks := false, 0
			platform := newFakePlatform()
			platform.openBackendOverride = "browserbase"
			if tc.uncertain {
				platform.failAction = "click"
			}
			platform.readViewsResponse = func(tool string, args map[string]any) map[string]any {
				if tool == "computer.browser_open" {
					page = stringFromAny(args["url"])
					return map[string]any{"session_id": "fixture-session", "backend": "browserbase", "app_context_id": "booking-qa", "current_url": page, "proxy": map[string]any{"mode": "auto"}}
				}
				if tool == "computer.computer_use" {
					switch args["action"] {
					case "navigate":
						page, submitted = stringFromAny(args["url"]), false
					case "set_text":
						key := map[string]string{"#name_0": "passenger_first_name", "#first_surname_0": "passenger_last_name", "#contact_email": "contact_email"}[stringFromAny(args["selector"])]
						if key != "" {
							state[key] = stringFromAny(args["text"])
						}
					case "select_option":
						key := map[string]string{"#checked-bags": "checked_bags", "#seat-choice": "seat"}[stringFromAny(args["selector"])]
						state[key] = stringFromAny(args["value"])
					case "screenshot":
						if args["include_som"] == true {
							return map[string]any{"current_url": page, "som_revision": "fresh", "som": []map[string]any{{"id": "pay", "label": 1, "role": "button", "text": state["pay_label"], "accessible_name": state["pay_label"], "dangerous": true, "effect": "financial_action"}}}
						}
					case "click":
						clicks++
						submitted = true
					case "wait_for":
						return map[string]any{"current_url": page, "matched": tc.outcome != "unknown", "timed_out": tc.outcome == "unknown"}
					}
					return map[string]any{"current_url": page, "success": true}
				}
				if tool != "computer.browser_extract" {
					return nil
				}
				var body strings.Builder
				body.WriteString("<html><body>")
				for k, value := range state {
					fmt.Fprintf(&body, `<span data-booking="%s">%s</span>`, k, html.EscapeString(value))
				}
				fmt.Fprintf(&body, `<button id="pay-reviewed-booking">%s</button>`, html.EscapeString(state["pay_label"]))
				if submitted && tc.outcome != "unknown" {
					receipt := map[string]string{"payment_status": "paid", "booking_status": "confirmed", "booking_reference": "AB12CD", "ticket_number": "0751234567890"}
					for k, value := range state {
						receipt[k] = value
					}
					if tc.outcome == "declined" || tc.outcome == "requires_action" {
						receipt["payment_status"] = tc.outcome
					}
					if tc.outcome == "wrong_receipt_total" {
						receipt["total"] = "101.00"
					}
					for k, value := range receipt {
						fmt.Fprintf(&body, `<span data-confirmation="%s">%s</span>`, k, html.EscapeString(value))
					}
				}
				body.WriteString("</body></html>")
				return map[string]any{"current_url": page, "html": body.String(), "rendered": true, "extraction_backend": "browser_dom"}
			}
			secretValues := map[string]string{"pan": "4111111111111111", "expiry": "12/99", "cvv": "123"}
			secrets := map[string]map[string]actorBrowserSecret{"flight-fixture": {}}
			for ref, value := range secretValues {
				secrets["flight-fixture"][ref] = actorBrowserSecret{Value: value, ContextID: "booking-qa", Host: "checkout.fixture.test", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
			}
			secretJSON, _ := json.Marshal(secrets)
			ctx, application := newTestCtx(t, platform, tk.WithConfig(map[string]string{"browser_secrets": string(secretJSON), "allow_private_networks": "true"}))
			ctx = ctx.WithProject("flight-fixture")
			record := saveFixtureActor(t, ctx, application, definition)
			run := func(operation string) map[string]any {
				queued, err := application.toolActorRun(ctx, map[string]any{"actor_id": record.ID, "operation": operation, "input": input})
				if err != nil {
					t.Fatal(err)
				}
				job, err := claimActorRun(ctx)
				if err != nil || job == nil {
					t.Fatalf("claim %v: %v", queued, err)
				}
				_ = application.executeActorRun(context.Background(), ctx, job)
				result, err := getActorRun(ctx, job.ID)
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			operation := "submit_booking"
			if tc.prepare {
				operation = "prepare_and_submit_booking"
			}
			result := run(operation)
			if (result["status"] == "failed") != tc.wantFail {
				t.Fatalf("status=%v error=%v", result["status"], result["error"])
			}
			out := mapFromAny(result["output"])
			attempted := tc.outcome != "" || tc.uncertain || !tc.wantFail
			if mapFromAny(out["execution_progress"])["commit_attempted"] != attempted {
				t.Fatal("incorrect commit progress", out)
			}
			if !attempted && clicks != 0 {
				t.Fatal("guard failure reached payment")
			}
			if attempted {
				// Another request label and operation alias must not bypass the
				// durable guard, including after an ambiguous provider outcome.
				before := clicks
				input["request_label"] = "a-different-request-label"
				if _, err := application.toolActorSave(ctx, map[string]any{"id": record.ID, "name": record.Name, "expected_revision": record.Revision, "definition": definition}); err != nil {
					t.Fatal(err)
				}
				again := run("submit_booking_alias")
				if again["status"] != "failed" || !strings.Contains(stringFromAny(again["error"]), "once_key already reserved") || clicks != before {
					t.Fatal("duplicate booking was not blocked", again["error"])
				}
			}
			if tc.prepare {
				encoded, _ := json.Marshal(result)
				if strings.Contains(string(encoded), secretValues["pan"]) || strings.Contains(string(encoded), `"text":"123"`) {
					t.Fatal("payment secret was persisted")
				}
				secretFills := 0
				for _, call := range platform.callsSnapshot() {
					if call.args["action"] == "set_text" && strings.HasPrefix(stringFromAny(call.args["selector"]), "#card-") {
						secretFills++
					}
				}
				if secretFills != 3 {
					t.Fatal("secret-backed payment fields were not all filled")
				}
			}
		})
	}
}

func TestIberiaPassengerDraftFixture(t *testing.T) {
	raw, err := os.ReadFile("examples/flights/iberia-checkout.local.json")
	if err != nil {
		t.Fatal(err)
	}
	var definition map[string]any
	if err = json.Unmarshal(raw, &definition); err != nil {
		t.Fatal(err)
	}
	operation := definition["operations"].(map[string]any)["fill_iberia_passenger_draft"].(map[string]any)
	steps := operation["steps"].([]any)
	firstInput := -1
	for i, rawStep := range steps {
		if rawStep.(map[string]any)["action"] == "set_text" {
			firstInput = i
			break
		}
	}
	if firstInput < 0 {
		t.Fatal("missing passenger input steps")
	}
	// Test the real provider field bindings without following a live quote.
	operation["steps"] = append([]any{map[string]any{"action": "goto", "url": "https://www.iberia.com/flights/?fixture=passengers"}}, steps[firstInput:]...)
	operation["required_explicit_inputs"] = []string{"booking_context_id", "passenger_first_name", "passenger_last_name", "contact_email", "contact_phone", "contact_country_code"}
	definition["operations"] = map[string]any{"fill_iberia_passenger_draft": operation}
	values := map[string]string{"#name_0": "", "#first_surname_0": "", "#contact_email": "", "#contact_phone_number": ""}
	checks := map[string]bool{}
	platform := newFakePlatform()
	platform.openBackendOverride = "browserbase"
	platform.readViewsResponse = func(tool string, args map[string]any) map[string]any {
		if tool == "computer.computer_use" {
			if args["action"] == "set_text" {
				values[stringFromAny(args["selector"])] = stringFromAny(args["text"])
			}
			if args["action"] == "set_checked" {
				checks[stringFromAny(args["selector"])], _ = args["checked"].(bool)
			}
			if args["action"] == "screenshot" && args["include_som"] == true {
				return map[string]any{"current_url": "https://www.iberia.com/flights/?fixture=passengers", "som_revision": "fixture", "som": []any{}}
			}
		}
		if tool != "computer.browser_extract" {
			return nil
		}
		body := `<body><button class="steps-nav-btn-current"><span class="steps-nav-btn-text">Passenger information</span></button><div class="ib-header-common__container-top"><span class="ib-header-common__city hidden-xl">MAD</span><span class="ib-header-common__city hidden-xl">LHR</span></div><div class="ib-header-common__fly-day">OUTBOUND: 17/11/2026</div><div class="ib-header-common__fly-passenger">x 1</div><span class="ib-text--price-big">86.57€</span><form name="passengerForm">`
		for _, id := range []string{"name_0", "first_surname_0"} {
			body += fmt.Sprintf(`<input id="%s" value="%s">`, id, html.EscapeString(values["#"+id]))
		}
		body += `</form>`
		for _, id := range []string{"contact_email", "contact_phone_number"} {
			body += fmt.Sprintf(`<input id="%s" value="%s">`, id, html.EscapeString(values["#"+id]))
		}
		body += `<div id="contact_prefix"><input value="+34"></div></body>`
		return map[string]any{"current_url": "https://www.iberia.com/flights/?fixture=passengers", "html": body, "rendered": true, "extraction_backend": "browser_dom"}
	}
	ctx, application := newTestCtx(t, platform)
	record := saveFixtureActor(t, ctx, application, definition)
	_, err = application.toolActorRun(ctx, map[string]any{"actor_id": record.ID, "operation": "fill_iberia_passenger_draft", "input": map[string]any{"booking_context_id": "booking-qa", "passenger_first_name": "Fixture", "passenger_last_name": "Traveler", "contact_email": "fixture@example.invalid", "contact_phone": "000000000", "contact_country_code": "+34"}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := claimActorRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = application.executeActorRun(context.Background(), ctx, job)
	result, _ := getActorRun(ctx, job.ID)
	if result["status"] != "completed" {
		t.Fatal(result["error"])
	}
	if checks["#contact_check"] != true || checks["#save_passengers_data"] != false {
		t.Fatal("draft did not enforce consent settings")
	}
	for _, call := range platform.callsSnapshot() {
		if call.args["action"] == "click" {
			t.Fatal("passenger draft unexpectedly submitted a form")
		}
	}
}
