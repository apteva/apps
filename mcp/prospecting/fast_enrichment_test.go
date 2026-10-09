package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBusinessMailboxRoles(t *testing.T) {
	for _, test := range []struct {
		name, domain, text, want string
		links                    []webLink
	}{
		{name: "recruiting business description is not mailbox purpose", domain: "staffing.fr", text: "Staffing and recruiting services for businesses. Contact office@staffing.fr.", want: "office@staffing.fr"},
		{name: "commercial before recruitment", domain: "auxpiedssouslatable.fr", text: "Service commercial : contact@auxpiedssouslatable.frPôle recrutement : candidat@auxpiedssouslatable.fr", want: "contact@auxpiedssouslatable.fr"},
		{name: "restaurant before hotel reception", domain: "fr.mamashelter.com", want: "food.lille@mamashelter.com", links: []webLink{{URL: "mailto:reception.lille@mamashelter.com", Text: "Reception"}, {URL: "mailto:food.lille@mamashelter.com", Text: "Restaurant Email"}}},
		{name: "vendor credits cannot return through corpus or mailto", domain: "parisbrest.bzh", text: "Développement : Mr Nicolas Tranne – Contact : nictranne@gmail.com\nCrédit photos : Photographe freelance – thomas.pellan@yahoo.fr", links: []webLink{{URL: "mailto:nictranne@gmail.com", Text: "Contact"}}, want: ""},
		{name: "business gmail remains valid beside vendor credits", domain: "restaurant.fr", text: "Réservations : restaurant@gmail.com\nDéveloppement : Contact : developer@gmail.com", want: "restaurant@gmail.com"},
		{name: "labeled recruitment gmail cannot return through corpus", domain: "restaurant.fr", text: "Recrutement : hiring@gmail.com", want: ""},
		{name: "recruitment alone is not a lead", domain: "example.fr", text: "candidat@example.fr careers@example.fr", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := webExtractPage{URL: "https://" + test.domain, Text: test.text, Links: test.links}
			if got := extractBestEmail([]webExtractPage{p}, test.domain); got != test.want {
				t.Fatalf("got %q want %q", got, test.want)
			}
		})
	}
}

func TestSourceCrawlDeduplicatesAndStopsAtPublishedContact(t *testing.T) {
	p := &platformStub{extractPages: map[string]any{
		"https://business.fr/":         map[string]any{"url": "https://business.fr/", "status": 200, "links": []webLink{{URL: "/contact/", Text: "Contact"}, {URL: "/contact", Text: "Contact"}, {URL: "/mentions-legales", Text: "Mentions légales"}}},
		"https://business.fr/contact/": map[string]any{"url": "https://business.fr/contact/", "status": 200, "text": "contact@business.fr"},
	}}
	ctx := newTestContext(t, p)
	profile, _ := createProfile(ctx.AppDB(), ctx.CurrentProject(), map[string]any{"name": "Restaurants"})
	candidate, _, _ := insertCandidate(ctx.AppDB(), ctx.CurrentProject(), candidateInput{ProfileID: profile.ID, CompanyName: "Business", Website: "https://business.fr/"}, profile)
	out, err := qualifyCandidate(ctx, candidate.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if out["candidate"].(*Candidate).Email != "contact@business.fr" || len(p.calls) != 2 {
		t.Fatalf("out=%v calls=%+v", out, p.calls)
	}
	for _, call := range p.calls {
		if call.Input["source_only"] != true {
			t.Fatal("unnecessary browser call")
		}
	}
}

type reusableQualificationPlatform struct{ platformStub }

func (p *reusableQualificationPlatform) CallAppResultContext(c context.Context, app, tool string, args map[string]any, out any) error {
	if tool != "web_extract" {
		return p.platformStub.CallAppResult(app, tool, args, out)
	}
	p.calls = append(p.calls, recordedCall{App: app, Tool: tool, Input: args})
	page := map[string]any{"url": args["url"], "status": 200, "text": "Restaurant business"}
	if args["source_only"] != true {
		page["browser"] = map[string]any{"session_id": "br_reuse"}
		if strings.Contains(fmt.Sprint(args["url"]), "contact") {
			page["text"] = "contact@business.fr"
		} else {
			page["links"] = []webLink{{URL: "/contact", Text: "Contact"}}
		}
	}
	b, _ := json.Marshal(map[string]any{"page": page})
	return json.Unmarshal(b, out)
}
func TestRenderedFallbackReusesAndClosesBrowser(t *testing.T) {
	p := &reusableQualificationPlatform{}
	ctx := newTestContext(t, p)
	profile, _ := createProfile(ctx.AppDB(), ctx.CurrentProject(), map[string]any{"name": "Restaurants"})
	candidate, _, _ := insertCandidate(ctx.AppDB(), ctx.CurrentProject(), candidateInput{ProfileID: profile.ID, CompanyName: "Business", Website: "https://business.fr/"}, profile)
	_, err := qualifyCandidate(ctx, candidate.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	rendered := []recordedCall{}
	closed := false
	for _, call := range p.calls {
		if call.Tool == "web_session_close" {
			closed = true
		}
		if call.Tool == "web_extract" && call.Input["source_only"] == false {
			rendered = append(rendered, call)
		}
	}
	if len(rendered) != 2 || rendered[1].Input["session_id"] != "br_reuse" || !closed {
		t.Fatalf("calls=%+v", p.calls)
	}
}

type concurrentQualificationPlatform struct {
	placesPlatform
	activeMu          sync.Mutex
	active, maxActive int
}

func (p *concurrentQualificationPlatform) CallAppResultContext(c context.Context, app, tool string, args map[string]any, out any) error {
	if tool == "web_extract" {
		p.activeMu.Lock()
		p.active++
		if p.active > p.maxActive {
			p.maxActive = p.active
		}
		p.activeMu.Unlock()
		time.Sleep(25 * time.Millisecond)
		defer func() { p.activeMu.Lock(); p.active--; p.activeMu.Unlock() }()
	}
	return p.placesPlatform.CallAppResultContext(c, app, tool, args, out)
}
func TestParallelRunStopsExactlyAtCompleteLeadTarget(t *testing.T) {
	p := &concurrentQualificationPlatform{}
	p.pages = map[string]any{}
	p.extractPages = map[string]any{}
	places := []any{}
	for i := 0; i < 6; i++ {
		url := fmt.Sprintf("https://business%d.fr/", i)
		places = append(places, placeFixture(fmt.Sprint(i), "Business", url, ""))
		p.extractPages[url] = map[string]any{"url": url, "status": 200, "text": fmt.Sprintf("Restaurant contact@business%d.fr", i)}
	}
	p.pages[""] = map[string]any{"places": places}
	ctx := newTestContext(t, p)
	profile, _ := createProfile(ctx.AppDB(), ctx.CurrentProject(), map[string]any{"name": "Restaurants"})
	_, err := (&App{}).toolDiscoverySettings(ctx, map[string]any{"places_connection_id": 31})
	if err != nil {
		t.Fatal(err)
	}
	id := startPipeline(t, ctx, profile, map[string]any{"target_leads": 3, "concurrency": 3, "query": "restaurants in France"})
	j := finishPipeline(t, ctx, id)
	if j.Counts["complete_leads"] != 3 || j.Counts["skipped"] != 3 || p.extractCalls != 3 || p.maxActive != 3 {
		t.Fatalf("counts=%v calls=%d max=%d", j.Counts, p.extractCalls, p.maxActive)
	}
	for _, item := range j.Items {
		if item.Status == "qualified" {
			cand, _ := getCandidate(ctx.AppDB(), ctx.CurrentProject(), *item.CandidateID)
			if cand.Location != "123 Main St, Dallas, TX 75201, USA" {
				t.Fatalf("Places address lost: %q", cand.Location)
			}
		}
	}
}

func TestNewOnlySkipsExistingBusinessesWithoutChangingOldProspects(t *testing.T) {
	p := &placesPlatform{}
	p.pages = map[string]any{"": map[string]any{"places": []any{placeFixture("old", "Old", "https://old.fr/", ""), placeFixture("new", "New", "https://new.fr/", "")}, "nextPageToken": "unused"}}
	p.extractPages = map[string]any{"https://new.fr/": map[string]any{"url": "https://new.fr/", "status": 200, "text": "contact@new.fr"}}
	ctx, oldProfile := pipelineSetup(t, p)
	old, _, err := insertCandidate(ctx.AppDB(), ctx.CurrentProject(), candidateInput{ProfileID: oldProfile.ID, CompanyName: "Old", Website: "https://old.fr/", Email: "office@old.fr", Summary: "Do not change"}, oldProfile)
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := createProfile(ctx.AppDB(), ctx.CurrentProject(), map[string]any{"name": "New leads"})
	id := startPipeline(t, ctx, profile, map[string]any{"new_only": true, "query": "Restaurants in France", "max_places_requests": 1})
	j := finishPipeline(t, ctx, id)
	if j.Counts["excluded"] != 1 || j.Counts["created"] != 1 || j.Counts["complete_leads"] != 1 || len(p.integrationCalls) != 1 {
		t.Fatalf("counts=%v calls=%d", j.Counts, len(p.integrationCalls))
	}
	unchanged, _ := getCandidate(ctx.AppDB(), ctx.CurrentProject(), old.ID)
	if unchanged.Summary != old.Summary || unchanged.Email != old.Email {
		t.Fatal("old lead changed")
	}
}
