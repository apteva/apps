package main

import (
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type appleFixtureTransport func(*http.Request) (*http.Response, error)

func (f appleFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func appleGzipFixture(t *testing.T, text string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	if _, err := z.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestAppleAnalyticsSyncFixtures(t *testing.T) {
	ctx, fake, game := newStudioFixture(t)
	const (
		appID          = "6828775568"
		eventDate      = "2026-11-04"
		eventHeader    = "Date\tApp Apple Identifier\tEvent\tPage Type\tCounts\n"
		downloadHeader = "Date\tApp Apple Identifier\tDownload Type\tCounts\n"
	)
	fixtures := map[string][]byte{
		"discovery-a": appleGzipFixture(t, eventHeader+eventDate+"\t"+appID+"\tImpression\tNo page\t8\n"+eventDate+"\t"+appID+"\tPage view\tIn-app event\t13\n"),
		"discovery-b": appleGzipFixture(t, eventHeader+eventDate+"\t"+appID+"\tImpression\tNo page\t4\n"+eventDate+"\t"+appID+"\tPage view\tStore sheet\t2\n"),
		"downloads":   appleGzipFixture(t, downloadHeader+eventDate+"\t"+appID+"\tFirst-time Download\t3\n"+eventDate+"\t"+appID+"\tRedownload\t0\n"+eventDate+"\t"+appID+"\tAuto-update\t7\n"),
		"installs":    appleGzipFixture(t, eventHeader+eventDate+"\t"+appID+"\tInstall\tProduct page\t2\n"+eventDate+"\t"+appID+"\tDelete\tProduct page\t1\n"),
		"correction":  appleGzipFixture(t, eventHeader+eventDate+"\t"+appID+"\tImpression\tNo page\t1\n2026-11-06\t"+appID+"\tPage view\tProduct page\t5\n"),
		"late-old":    appleGzipFixture(t, eventHeader+eventDate+"\t"+appID+"\tImpression\tNo page\t100\n"),
		"bad-app":     appleGzipFixture(t, eventHeader+eventDate+"\t6830119281\tImpression\tNo page\t99\n"),
	}
	phase := 0
	created, downloads := 0, 0
	broken := ""
	priorClient := appleSegmentClient
	client := *priorClient
	client.Transport = appleFixtureTransport(func(req *http.Request) (*http.Response, error) {
		key := strings.TrimPrefix(req.URL.Path, "/reports/")
		data, ok := fixtures[key]
		if !ok || req.Method != http.MethodGet || req.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected download request %s %s", req.Method, req.URL.Path)
		}
		downloads++
		if key == broken {
			return nil, errors.New("fixture download failure")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	appleSegmentClient = &client
	t.Cleanup(func() { appleSegmentClient = priorClient })

	resource := func(id string, attrs map[string]any) map[string]any {
		return map[string]any{"id": id, "attributes": attrs}
	}
	list := func(items ...map[string]any) map[string]any {
		return map[string]any{"data": append([]map[string]any{}, items...)}
	}
	active := resource("request", map[string]any{"accessType": "ONGOING", "stoppedDueToInactivity": false})
	instance := func(id, date string) map[string]any {
		return resource(id, map[string]any{"granularity": "DAILY", "processingDate": date})
	}
	segment := func(id string) map[string]any {
		return resource(id, map[string]any{
			"url":         "https://asp-qa-us-west-2.s3.us-west-2.amazonaws.com/reports/" + id,
			"sizeInBytes": len(fixtures[id]), "checksum": fmt.Sprintf("%x", md5.Sum(fixtures[id])),
		})
	}
	fake.providerCall = func(tool string, input map[string]any) (any, error) {
		switch tool {
		case "get_app":
			return map[string]any{"data": map[string]any{"id": input["app_id"]}}, nil
		case "list_analytics_report_requests":
			if input["app_id"] != appID || input["access_type"] != "ONGOING" {
				t.Fatalf("unexpected request scope %v", input)
			}
			if created == 0 {
				return list(resource("expired", map[string]any{"accessType": "ONGOING", "stoppedDueToInactivity": true})), nil
			}
			return list(active), nil
		case "create_analytics_report_request":
			if input["app_id"] != appID || input["accessType"] != "ONGOING" {
				t.Fatalf("unexpected creation scope %v", input)
			}
			created++
			return map[string]any{"data": active}, nil
		case "list_analytics_reports":
			if phase == 0 {
				return list(), nil
			}
			return list(
				resource("detail", map[string]any{"name": appleAnalyticsReports[0].name + " Detailed"}),
				resource("discovery", map[string]any{"name": appleAnalyticsReports[0].name + " Standard"}),
				resource("downloads", map[string]any{"name": appleAnalyticsReports[1].name + " Standard"}),
				resource("installs", map[string]any{"name": appleAnalyticsReports[2].name + " Standard"}),
			), nil
		case "list_analytics_report_instances":
			if input["granularity"] != "DAILY" {
				t.Fatalf("unexpected granularity %v", input)
			}
			switch input["report_id"] {
			case "discovery":
				switch phase {
				case 1:
					return list(instance("original", "2026-11-06")), nil
				case 2:
					return list(instance("correction", "2026-11-07"), instance("late-old", "2026-11-05"), instance("original", "2026-11-06")), nil
				default:
					return list(instance("wrong-app", "2026-11-08")), nil
				}
			case "downloads", "installs":
				return list(instance(txt(input["report_id"]), "2026-11-06")), nil
			}
		case "list_analytics_report_segments":
			switch input["instance_id"] {
			case "original":
				if input["cursor"] == "page-two" {
					return list(segment("discovery-b")), nil
				}
				page := list(segment("discovery-a"))
				page["links"] = map[string]any{"next": "https://api.appstoreconnect.apple.com/v1/analyticsReportInstances/original/segments?cursor=page-two"}
				return page, nil
			case "wrong-app":
				return list(segment("bad-app")), nil
			default:
				return list(segment(txt(input["instance_id"]))), nil
			}
		}
		return nil, fmt.Errorf("unexpected connector call %s %v", tool, input)
	}
	args := map[string]any{"provider": "app-store-connect", "family": appleAnalyticsFamily, "external_id": appID, "connection_id": 9}
	source, err := metricSourceSet(ctx, game, args)
	if err != nil {
		t.Fatal(err)
	}
	id := txt(source.(map[string]any)["id"])
	sync := func(wantError bool) {
		t.Helper()
		if _, err := syncMetricSource(ctx, game, id, 7); (err != nil) != wantError {
			t.Fatalf("sync error = %v, want error %v", err, wantError)
		}
	}
	status := func(family, want, date string) {
		t.Helper()
		sources, err := metricSources(ctx, game)
		if err != nil || len(sources) != 1 {
			t.Fatal(sources, err)
		}
		r := object(object(sources[0]["report_status"])[family])
		if r["status"] != want || r["latest_data_date"] != date {
			t.Fatalf("%s status %v, want %s %s", family, r, want, date)
		}
	}
	query := func(scope GameScope) (string, error) {
		raw, err := gameMetricsQuery(ctx, scope, map[string]any{"topic": appleAnalyticsTopic, "source_id": id})
		encoded, _ := json.Marshal(raw)
		return string(encoded), err
	}
	check := func(want string) {
		t.Helper()
		got, err := query(game)
		if err != nil || got != fmt.Sprintf(`{"days":%s,"source_id":%q}`, want, id) {
			t.Fatalf("query %s (%v), want days %s", got, err, want)
		}
	}

	sync(false)
	status("discovery", "waiting", "")
	check("[]")
	phase, broken = 1, "discovery-b"
	sync(true)
	status("discovery", "failed", "")
	check("[]")
	broken = ""
	sync(false)
	status("installations", "reported", eventDate)
	check(`[{"date":"2026-11-04","counts":{"first_time_downloads":3,"impressions":12,"installations":2,"product_page_views":2,"redownloads":0}}]`)
	before := downloads
	sync(false)
	if downloads != before || created != 1 {
		t.Fatalf("retry downloaded %d segments and created %d requests", downloads-before, created)
	}

	phase = 2
	sync(false)
	const corrected = `[{"date":"2026-11-06","counts":{"product_page_views":5}},{"date":"2026-11-04","counts":{"first_time_downloads":3,"impressions":1,"installations":2,"redownloads":0}}]`
	check(corrected)
	status("discovery", "reported", "2026-11-06")
	phase = 3
	sync(true)
	check(corrected)
	if _, err := query(GameScope{ProjectID: game.ProjectID, GameID: "other-game"}); err == nil {
		t.Fatal("acquisition data escaped game scope")
	}
	if _, err := metricSourceSet(ctx, game, map[string]any{"provider": "app-store-connect", "external_id": appID, "connection_id": 9}); err == nil {
		t.Fatal("Sales and Trends unexpectedly accepted without a vendor number")
	}
}
