package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"
)

func TestEditableUnmessagedCountrySearch(t *testing.T) {
	for _, mode := range []string{"success", "default_gender", "other_country", "blank_location", "wrong_account", "radius_mode", "wrong_country", "wrong_gender", "wrong_messages", "wrong_profile", "restricted_region", "restricted_town", "encoded_input", "age_filter", "wrong_min_age", "wrong_max_age", "wrong_age_input", "wrong_age_order"} {
		t.Run(mode, func(t *testing.T) {
			aged := mode == "age_filter" || mode == "wrong_min_age" || mode == "wrong_max_age" || mode == "wrong_age_input" || mode == "wrong_age_order"
			plat := newFakePlatform()
			current := ""
			country, gender := "ES", "F"

			if mode == "other_country" {
				country = "GB"
			}
			if mode == "encoded_input" {
				country = "ES&do_postcode_search=1"
			}
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_open":
					current = stringFromAny(in["url"])
				case "computer.computer_use":
					switch in["action"] {
					case "navigate":
						current = stringFromAny(in["url"])
						return map[string]any{"current_url": current}
					case "screenshot":
						return map[string]any{"current_url": current, "som_revision": 1, "scroll_regions": []any{map[string]any{"id": "doc", "name": "Document", "role": "document", "scroll_top": 0, "max_scroll_y": 1000}}}
					case "scroll":
						return map[string]any{"scroll": map[string]any{"actual_target_id": "doc"}}
					case "click", "type", "set_text", "select_option":
						t.Fatal("country search must be read-only")
					}
				case "computer.browser_extract":
					if in["readability"] != false {
						t.Fatal("filters omitted by readability")
					}
					account, locmode, actualCountry, actualGender, messages, profile, region, town := "owner", "0", country, gender, "exclude", "model", "", ""
					switch mode {
					case "wrong_account":
						account = "other"
					case "radius_mode":
						locmode = "1"
					case "wrong_country":
						actualCountry = "GB"
					case "wrong_gender":
						actualGender = "M"
					case "wrong_messages":
						messages = "include"
					case "wrong_profile":
						profile = "photographer"
					case "restricted_region":
						region = "Madrid"
					case "restricted_town":
						town = "Madrid"
					}
					location := "Madrid, Spain"
					if mode == "other_country" {
						location = "London, United Kingdom"
					}
					if mode == "blank_location" {
						location = "\u00a0"
					}
					h := `<body><div class="navbar"><a href="/` + account + `">Your Profile</a></div><input id="do_postcode_search" value="` + locmode + `"><input name="postcode" value="London"><input name="acPostcodeAllID" value="37999">`
					for _, f := range [][2]string{{"country", actualCountry}, {"gender", actualGender}, {"messages", messages}, {"profile", profile}, {"location", region}, {"town", town}} {
						h += fmt.Sprintf(`<select name="%s"><option selected value="%s">%s</option></select>`, f[0], f[1], f[1])
					}
					if aged {
						lower, upper := "18", "39"
						if mode == "wrong_min_age" || mode == "wrong_age_input" {
							lower = "17"
						}
						if mode == "wrong_max_age" {
							upper = "40"
						}
						if mode == "wrong_age_order" {
							lower = "39"
							upper = "18"
						}
						h += fmt.Sprintf(`<select name="lage"><option selected value="%s">%s</option></select><select name="hage"><option selected value="%s">%s</option></select>`, lower, lower, upper, upper)
					}
					h += `<div id="member_search_results"><div id="42_member_card"><a class="thumbnailPic" href="/sample-model"></a><a class="a4" href="/sample-model">Sample Model</a><a href="/sample-model/references">3</a><a href="/message.php?user=42">Message</a><div class="text-left smallSidePadding">Female Model<br>3 Positive References<br>` + location + `<br>1264 km away</div></div></div></body>`
					return map[string]any{"html": h, "current_url": current, "rendered": true}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			raw, err := os.ReadFile("examples/adultfolio-control.json")
			if err != nil {
				t.Fatal(err)
			}
			var def map[string]any
			if err := json.Unmarshal(raw, &def); err != nil {
				t.Fatal(err)
			}
			def["browser"].(map[string]any)["context_id"] = ""
			rec := saveFixtureActor(t, ctx, app, def)
			input := map[string]any{"country_code": country, "gender_code": gender, "account_url": "https://www.adultfolio.com/owner", "read_pages": 1, "max_duration_seconds": 60}
			if mode == "default_gender" {
				delete(input, "gender_code")
			}
			operation := "search_unmessaged_models_by_country"
			if aged {
				operation = "search_unmessaged_models_by_country_and_age"
			}
			if mode == "wrong_age_input" {
				input["min_age"] = "17"
			}
			if mode == "wrong_age_order" {
				input["min_age"] = "39"
				input["max_age"] = "18"
			}
			queued, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": operation, "input": input})
			if err != nil {
				t.Fatal(err)
			}
			run, _ := claimActorRun(ctx)
			app.executeActorRun(context.Background(), ctx, run)
			id := queued.(map[string]any)["run_id"].(int64)
			r, _ := getActorRun(ctx, id)
			good := mode == "success" || mode == "default_gender" || mode == "other_country" || mode == "blank_location" || mode == "age_filter"
			if (r["status"] == "completed") != good {
				t.Fatalf("%s: %v", mode, r["error"])
			}
			u, _ := url.Parse(current)
			q := u.Query()
			wantKeys := 9
			if aged {
				wantKeys = 11
				if q.Get("lage") == "" || q.Get("hage") == "" {
					t.Fatal("missing age query")
				}
			}
			if q.Get("country") != country || q.Get("gender") != gender || q.Get("do_postcode_search") != "0" || q.Get("messages") != "exclude" || q.Get("profile") != "model" || q.Get("location") != "" || q.Get("town") != "" || len(q) != wantKeys {
				t.Fatal("native query not safely encoded", q)
			}
			if good {
				coverage := r["output"].(map[string]any)["coverage"].(map[string]any)
				if coverage["inspection_complete"] != false || coverage["more_results_remaining"] != true {
					t.Fatal("bounded preview claimed complete coverage")
				}
				out, err := app.toolDatasetRead(ctx, map[string]any{"run_id": id, "limit": 20})
				if err != nil {
					t.Fatal(err)
				}
				rows := out.(map[string]any)["items"].([]map[string]any)
				if len(rows) != 2 || rows[0]["record_type"] != "model" || rows[0]["requested_country_code"] != country || rows[0]["messages_filter"] != "exclude" {
					t.Fatal("unstructured search output", rows)
				}
				if mode == "blank_location" {
					if rows[0]["location"] != "unavailable" || rows[0]["location_status"] != "unverified" {
						t.Fatal("blank location guessed", rows[0])
					}
				} else if rows[0]["location"] == "unavailable" || rows[0]["location_status"] != "reported" {
					t.Fatal("readable location lost", rows[0])
				}
			}
		})
	}
}
