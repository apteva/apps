package main

import (
	"encoding/json"
	"os"
	"testing"
)

func jsonFile(name string, out any) error {
	raw, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func TestCrawlDefinitionValidationAndUFCFixture(t *testing.T) {
	definition := map[string]any{}
	if err := jsonFile("examples/ufcstats.json", &definition); err != nil {
		t.Fatal(err)
	}
	def, _, err := decodeActorDefinition(definition["definition"])
	if err != nil {
		t.Fatal(err)
	}
	if def.SchemaVersion != 2 || len(def.Crawl.Routes) != 4 || len(def.Crawl.Datasets) != 4 {
		t.Fatalf("unexpected crawl definition: %+v", def.Crawl)
	}
}

func TestGenericCrawlTemplateIsValid(t *testing.T) {
	definition := map[string]any{}
	if err := jsonFile("examples/crawl-template.json", &definition); err != nil {
		t.Fatal(err)
	}
	def, _, err := decodeActorDefinition(definition["definition"])
	if err != nil {
		t.Fatal(err)
	}
	if def.SchemaVersion != 2 || len(def.Crawl.Routes) != 1 || len(def.Crawl.Datasets) != 1 {
		t.Fatalf("unexpected generic crawl template: %+v", def.Crawl)
	}
}

func TestCrawlExtractionFanoutAndTransforms(t *testing.T) {
	crawl := crawlDefinition{
		Frontier: crawlFrontier{MaxDepth: 1},
		Datasets: map[string]crawlDataset{"events": {Key: "event_url", Schema: map[string]string{"event_url": "url", "date": "date", "location": "string"}}},
		Routes: map[string]crawlRoute{"index": {Match: "/events", Extract: []crawlExtract{{Dataset: "events", Items: "tr", Fields: map[string]crawlField{
			"event_url": {actorField: actorField{Selector: "a", Attribute: "href", Type: "url", Required: true}},
			"date":      {actorField: actorField{Selector: ".date", Type: "string", Required: true}},
			"location":  {actorField: actorField{Selector: ".location", Type: "string", Required: true}},
		}, Transforms: map[string]string{"date": "date"}}}, Follow: []crawlFollow{{Field: "event_url", Route: "event"}}}, "event": {Match: "/event-details/"}},
	}
	page, err := extractCrawlPage(crawl, crawl.Routes["index"], &crawlQueueItem{URL: "https://www.ufcstats.com/statistics/events/completed"}, &browserExtractResult{
		CurrentURL: "https://www.ufcstats.com/statistics/events/completed",
		HTML:       `<table><tbody><tr><td><a href="/event-details/abc">UFC 1</a></td><td class="date">June 22, 2002</td><td class="location">Las Vegas, Nevada, USA</td></tr></tbody></table>`,
		Rendered:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || len(page.Links) != 1 {
		t.Fatalf("records=%v links=%v", page.Records, page.Links)
	}
	if page.Records[0].Item["date"] != "2002-06-22" || page.Links[0].URL != "https://www.ufcstats.com/event-details/abc" {
		t.Fatalf("unexpected extraction: %+v %+v", page.Records[0], page.Links[0])
	}
	if landed, err := transformCrawlValue("43 of 78", "ratio_landed"); err != nil || landed != int64(43) {
		t.Fatalf("ratio transform: %v %v", landed, err)
	}
	if seconds, err := transformCrawlValue("3:34", "duration_seconds"); err != nil || seconds != int64(214) {
		t.Fatalf("duration transform: %v %v", seconds, err)
	}
}

func TestCrawlURLCanonicalization(t *testing.T) {
	a := canonicalCrawlURL("HTTP://WWW.UFCSTATS.COM:80/event-details/abc#row")
	b := canonicalCrawlURL("http://www.ufcstats.com/event-details/abc")
	if a != b {
		t.Fatalf("canonical URLs differ: %q != %q", a, b)
	}
}

func TestCrawlMigrationCreatesQueueAndMaterializedTables(t *testing.T) {
	ctx, _ := newTestCtx(t, newFakePlatform())
	for _, table := range []string{"actors_crawl_queue", "actors_crawl_records", "actors_crawl_materialized"} {
		var name string
		if err := ctx.AppDB().QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil || name != table {
			t.Fatalf("missing crawl table %s: %v", table, err)
		}
	}
}

func TestCommitCrawlPageMaterializesKeyedRecords(t *testing.T) {
	ctx, _ := newTestCtx(t, newFakePlatform())
	ctx = ctx.WithProject("crawl-project")
	result, err := ctx.AppDB().Exec(`INSERT INTO actors_runs(project_id,kind,input_json,status,actor_id,actor_revision,definition_snapshot_json) VALUES(?,?,?,?,?,?,?)`, "crawl-project", "actor", "{}", "running", 7, 2, "{}")
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := result.LastInsertId()
	if _, err := ctx.AppDB().Exec(`INSERT INTO actors_crawl_queue(project_id,run_id,url,route,dedupe_key,status,fence) VALUES(?,?,?,?,?,?,?)`, "crawl-project", runID, "https://example.com/events/1", "event", "https://example.com/events/1", "running", 1); err != nil {
		t.Fatal(err)
	}
	run := &actorQueuedRun{ID: runID, ActorID: 7}
	definition := actorDefinition{AllowedHosts: []string{"example.com"}, Crawl: &crawlDefinition{Routes: map[string]crawlRoute{"event": {Match: "/events/"}}}}
	page := &crawlPage{Records: []crawlRecord{{Dataset: "events", Key: "event-1", Item: map[string]any{"id": "event-1", "name": "Example"}}}}
	if err := commitCrawlPage(ctx, run, definition, &crawlQueueItem{ID: 1, URL: "https://example.com/events/1", Route: "event", Fence: 1}, page, 100); err != nil {
		t.Fatal(err)
	}
	var item string
	if err := ctx.AppDB().QueryRow(`SELECT item_json FROM actors_crawl_materialized WHERE project_id=? AND actor_id=? AND dataset=? AND record_key=?`, "crawl-project", 7, "events", "event-1").Scan(&item); err != nil {
		t.Fatal(err)
	}
	if item != `{"id":"event-1","name":"Example"}` {
		t.Fatalf("item=%s", item)
	}
}
