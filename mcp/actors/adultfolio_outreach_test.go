package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type lostSendTransport struct{ *fakePlatform }

func (p *lostSendTransport) CallAppResultContext(ctx context.Context, app, tool string, in map[string]any, out any) error {
	err := p.fakePlatform.CallAppResultContext(ctx, app, tool, in, out)
	if err == nil && app == "computer" && tool == "computer_use" && in["action"] == "click" {
		return fmt.Errorf("transport lost after dispatch")
	}
	return err
}

var _ sdk.PlatformClient = (*lostSendTransport)(nil)

func TestAdultFolioInitialSendAndAmbiguousReconciliation(t *testing.T) {
	for _, mode := range []string{"success", "server_line_indentation", "fast_incoming", "age18", "age39", "age17", "age40", "missing_age", "duplicate_age", "wrong_profile_id", "wrong_profile_url", "wrong_country", "wrong_role", "previous_history", "nonempty", "loading", "missing_composer", "wrong_account", "wrong_recipient", "wrong_composer", "race_message", "wrong_saved_body", "wrong_sender", "missing_timestamp", "lost_accepted_response", "lost_unaccepted_response", "transport_accepted", "transport_unaccepted", "prepare"} {
		t.Run(mode, func(t *testing.T) {
			plat := newFakePlatform()
			plat.waitOutput = map[string]any{"matched": true, "timed_out": false}
			current, composer := "", ""
			dispatched, saved := 0, false
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_open":
					current = stringFromAny(in["url"])
				case "computer.computer_use":
					switch in["action"] {
					case "wait":
						return map[string]any{"current_url": current}
					case "wait_for":
						raw, _ := json.Marshal(in["conditions"])
						var conditions []map[string]any
						json.Unmarshal(raw, &conditions)
						for _, condition := range conditions {
							if condition["type"] == "selector_present" && condition["selector"] == "#MessageResult" {
								t.Fatal("empty zero-height history must not be a visibility readiness predicate")
							}
						}
						return map[string]any{"matched": mode != "missing_composer", "timed_out": mode == "missing_composer"}
					case "navigate":
						current = stringFromAny(in["url"])
						return map[string]any{"current_url": current}
					case "set_text":
						composer = "<p>Hello</p><p>World</p>"
						if mode == "wrong_composer" {
							composer = "Wrong"
						}
						return map[string]any{"current_url": current}
					case "screenshot":
						return map[string]any{"current_url": current, "som_revision": 1, "som": []any{map[string]any{"id": "send", "tag": "button", "accessible_name": "Send!", "effect": "message_send"}}}
					case "click":
						if in["target_id"] != "send" || in["expected_text"] != "Send!" || in["expected_effect"] != "message_send" || in["confirm_consequence"] != "message_send" || in["coordinate"] != nil {
							t.Fatal("unguarded send")
						}
						dispatched++
						saved = mode != "lost_unaccepted_response" && mode != "transport_unaccepted"
						if strings.HasPrefix(mode, "lost_") {
							return map[string]any{"success": false, "error": "response lost after dispatch"}
						}
						return map[string]any{"current_url": current}
					}
				case "computer.browser_extract":
					if in["readability"] != false {
						t.Fatal("readability omitted controls")
					}
					owner, recipient, mid, age, country, role, history := "owner", "model", "42", "36", "ES", "Female Model", "No message history"
					if mode == "wrong_account" {
						owner = "other"
					}
					if mode == "wrong_recipient" {
						recipient = "other"
					}
					if mode == "wrong_profile_id" {
						mid = "43"
					}
					if mode == "age18" {
						age = "18"
					}
					if mode == "age39" {
						age = "39"
					}
					if mode == "age17" {
						age = "17"
					}
					if mode == "age40" {
						age = "40"
					}
					if mode == "wrong_country" {
						country = "GB"
					}
					if mode == "wrong_role" {
						role = "Male Model"
					}
					if mode == "previous_history" {
						history = "View message history"
					}
					h := `<body><div class="navbar"><a href="/` + owner + `">Your Profile</a></div>`
					url := current
					if !strings.Contains(current, "message.php") {
						h += `<h1 class="MainTitle">Model</h1><p>` + role + `<a href="/search?profile=model&amp;country=` + country + `">Madrid, Spain</a></p><button onclick="document.location='/message.php?user=` + mid + `&amp;casting='">Message</button><a class="sub-menu-link" href="/model/info">Info</a>`
						if strings.HasSuffix(current, "/info") {
							if mode != "missing_age" {
								h += `<div class="col-lg-6">Age</div><div class="col-lg-6">` + age + `</div>`
							}
							if mode == "duplicate_age" {
								h += `<div class="col-lg-6">Age</div><div class="col-lg-6">36</div>`
							}
							h += `<div class="col-lg-6">Message History</div><div class="col-lg-6"><a href="/message.php?user=42">` + history + `</a></div>`
						}
						if mode == "wrong_profile_url" {
							url = "https://www.adultfolio.com/other"
						}
					} else {
						h += `<div class="MainTitle"><a class="a4" href="/` + recipient + `">Model</a></div><div class="note-editable panel-body" contenteditable="true">` + composer + `</div><div id="MessageResult" class="conversation-42">`
						if mode == "nonempty" || (mode == "race_message" && composer != "") {
							h += `<div id="10" class="messageContainer message-modern-wrap">Previous</div>`
						}
						if mode == "loading" {
							h += `Still loading…`
						}
						if saved {
							sender, body, ts := "owner", "<p>Hello</p><p>World</p>", "2026-10-11T12:00:00Z"
							if mode == "server_line_indentation" {
								body = "\n\t<p> Hello <br> World </p>\n\t"
							}
							if mode == "wrong_saved_body" {
								body = "Other"
							}
							if mode == "wrong_sender" {
								sender = "model"
							}
							if mode == "missing_timestamp" {
								ts = ""
							}
							h += fmt.Sprintf(`<div id="11" class="messageContainer message-modern-wrap"><a class="thumbnailPic" href="/%s"></a><div class="message-modern-meta"><span class="timeago" title="%s"></span></div><div class="message-modern-body">%s</div></div>`, sender, ts, body)
							if mode == "fast_incoming" {
								h += `<div id="12" class="messageContainer message-modern-wrap"><a class="thumbnailPic" href="/model"></a><div class="message-modern-body">Reply</div></div>`
							}
						}
						h += `</div>`
					}
					return map[string]any{"html": h + `</body>`, "current_url": url, "rendered": true}
				}
				return nil
			}
			var opts []tk.Option
			if strings.HasPrefix(mode, "transport_") {
				opts = append(opts, tk.WithPlatform(&lostSendTransport{plat}))
			}
			ctx, app := newTestCtx(t, plat, opts...)
			raw, err := os.ReadFile("examples/adultfolio-control.json")
			if err != nil {
				t.Fatal(err)
			}
			var def map[string]any
			if err = json.Unmarshal(raw, &def); err != nil {
				t.Fatal(err)
			}
			def["browser"].(map[string]any)["context_id"] = ""
			rec := saveFixtureActor(t, ctx, app, def)
			input := map[string]any{"account_url": "https://www.adultfolio.com/owner", "account_profile_path": "/owner", "model_id": "42", "model_url": "https://www.adultfolio.com/model", "country_code": "ES", "message": "Hello\nWorld", "request_id": "initial-test", "max_duration_seconds": 60}
			execute := func(op string) map[string]any {
				q, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": op, "input": input})
				if err != nil {
					t.Fatal(err)
				}
				run, err := claimActorRun(ctx)
				if err != nil {
					t.Fatal(err)
				}
				app.executeActorRun(context.Background(), ctx, run)
				r, err := getActorRun(ctx, q.(map[string]any)["run_id"].(int64))
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			op := "initial_send"
			if mode == "prepare" {
				op = "prepare_initial_send"
			}
			r := execute(op)
			good := mode == "success" || mode == "server_line_indentation" || mode == "fast_incoming" || mode == "age18" || mode == "age39" || mode == "prepare"
			if (r["status"] == "completed") != good {
				t.Fatalf("status %v: %v", r["status"], r["error"])
			}
			postFailure := mode == "wrong_saved_body" || mode == "wrong_sender" || mode == "missing_timestamp" || (strings.HasPrefix(mode, "lost_") || strings.HasPrefix(mode, "transport_"))
			want := 0
			if (good && mode != "prepare") || postFailure {
				want = 1
			}
			if dispatched != want {
				t.Fatalf("sent %d want %d: %v", dispatched, want, r["error"])
			}
			if strings.HasPrefix(mode, "lost_") || strings.HasPrefix(mode, "transport_") {
				v := execute("verify_initial_send")
				if (v["status"] == "completed") != (mode == "lost_accepted_response" || mode == "transport_accepted") {
					t.Fatalf("reconciliation %v: %v", v["status"], v["error"])
				}
				retry := execute("initial_send")
				if retry["status"] != "failed" || dispatched != 1 {
					t.Fatal("ambiguous initial send repeated")
				}
				if saved {
					reply := execute("reply")
					if reply["status"] != "failed" || dispatched != 1 || !strings.Contains(reply["error"].(string), "once_key already reserved") {
						t.Fatal("cross-operation retry allowed", reply["error"])
					}
				}
			}
		})
	}
}
