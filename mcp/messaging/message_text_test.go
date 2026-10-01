package main

import (
	"strings"
	"testing"
)

const testHTMLOnlyEmail = `<!DOCTYPE html><html><head><title>Hidden title</title><style>hidden-css</style></head><body><p>Hello &amp; team<br>Full email content.</p><script>hidden-script</script></body></html>`

func TestParseRawEmlDerivesTextForHTMLOnlyEmail(t *testing.T) {
	raw := "From: sender@example.test\r\nTo: inbox@example.test\r\nSubject: Example\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" + testHTMLOnlyEmail
	parsed, err := parseRawEml([]byte(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(parsed.BodyText, "Hello & team") || !strings.Contains(parsed.BodyText, "Full email content.") || strings.Contains(parsed.BodyText, "hidden-") || strings.Contains(parsed.BodyText, "Hidden title") {
		t.Fatalf("HTML-only email did not yield safe readable text: %q", parsed.BodyText)
	}
	if parsed.BodyHTML != testHTMLOnlyEmail {
		t.Fatal("normalization altered the original HTML")
	}
	if text := inboundEmailText("Original provider text", testHTMLOnlyEmail); text != "Original provider text" {
		t.Fatal("provider text was replaced")
	}
}

func TestDispatchInboundRepairsLegacyHTMLOnlyTextAndRoutesBothBodies(t *testing.T) {
	for _, provided := range []string{"", " \r\n\t", "Original provider text"} {
		t.Run(provided, func(t *testing.T) {
			platform := &stubPlatform{}
			ctx := newTestCtx(t, platform)
			if _, err := dbInboundRouteUpsert(ctx.AppDB(), "test-proj", channelEmail, "*", "crm", "/inbound", 0); err != nil {
				t.Fatal(err)
			}
			res, err := ctx.AppDB().Exec(`INSERT INTO messages(project_id,channel,direction,from_addr,to_addrs,subject,body_text,body_html,status,route_status)
				VALUES('test-proj','email','in','sender@example.test','["inbox@example.test"]','Example',?,?,'received','pending')`, provided, testHTMLOnlyEmail)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := res.LastInsertId()
			message, err := dbMessageGet(ctx.AppDB(), "test-proj", id)
			if err != nil {
				t.Fatal(err)
			}
			if err := dispatchInbound(ctx, "test-proj", message); err != nil {
				t.Fatal(err)
			}
			stored, err := dbMessageGet(ctx.AppDB(), "test-proj", id)
			if err != nil {
				t.Fatal(err)
			}
			if stored.BodyText == "" || stored.BodyHTML != testHTMLOnlyEmail || stored.RouteStatus != "ok" {
				t.Fatalf("normalized original was not retained: %#v", stored)
			}
			if provided == "Original provider text" && stored.BodyText != provided {
				t.Fatal("existing text was overwritten")
			}
			if len(platform.callAppCalls) != 1 {
				t.Fatalf("dispatch calls=%d, want one", len(platform.callAppCalls))
			}
			call := platform.callAppCalls[0]
			if call.Tool != "messaging_inbound_receive" || call.Input["body_text"] != stored.BodyText || call.Input["body_html"] != testHTMLOnlyEmail {
				t.Fatalf("dispatch omitted original or derived body: %#v", call)
			}
		})
	}
}
