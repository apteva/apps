package main

import (
	"fmt"
	"time"
)

func (a *App) loadDemo(project, actor string) (any, error) {
	now := a.clock()
	p := Provider{ID: 0, Slug: "example"}
	events := []FeedEvent{}
	football := []string{"Harbor FC", "Northbridge", "Riverside", "Valley Athletic"}
	tennis := []string{"Alex Morgan", "Sam Rivera", "Jordan Lee", "Taylor Hayes"}
	for _, sport := range []string{"football", "tennis"} {
		names := football
		if sport == "tennis" {
			names = tennis
		}
		for n := 0; n < 24; n++ {
			h, a := n%4, (n+1+n/4%2)%4
			if h == a {
				a = (a + 1) % 4
			}
			hs, as := 2, 0
			if n%3 == 0 {
				hs, as = 0, 2
			}
			if sport == "football" && n%5 == 0 {
				hs, as = 1, 1
			}
			start := time.Unix(now, 0).UTC().Truncate(24*time.Hour).AddDate(0, 0, -30+n).Unix()
			events = append(events, FeedEvent{ExternalID: fmt.Sprintf("%s-history-%d", sport, n), Sport: sport, Competition: "Example league", Home: names[h], Away: names[a], Start: start, Status: "finished", HomeScore: &hs, AwayScore: &as, Example: true})
		}
		for n := 0; n < 3; n++ {
			start := now + int64(3600*(n+2))
			qs := []Quote{}
			for i, book := range []string{"Example Book A", "Example Book B", "Example Book C"} {
				home := int64(2300000 + i*80000)
				away := int64(1850000 + i*50000)
				if sport == "football" {
					home = 2550000 + int64(i*150000)
					away = 2900000 + int64(i*100000)
					qs = append(qs, Quote{"draw", book, 3300000 + int64(i*50000), now})
				}
				qs = append(qs, Quote{"home", book, home, now}, Quote{"away", book, away, now})
			}
			events = append(events, FeedEvent{ExternalID: fmt.Sprintf("%s-upcoming-%d", sport, n), Sport: sport, Competition: "Example league", Home: names[n], Away: names[(n+1)%4], Start: start, Status: "scheduled", Example: true, Quotes: qs})
		}
	}
	_, err := a.importBatch(project, actor, p, "example", "", time.Unix(now, 0), events)
	if err != nil {
		return nil, err
	}
	// Explicit demo loading is repeatable, preserving existing proposals/bets.
	var count int
	err = a.db.QueryRow("SELECT COUNT(*) FROM bankrolls WHERE project_id=? AND example=1", project).Scan(&count)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		if _, err = a.createBankroll(project, actor, map[string]any{"name": "Example bankroll", "currency": "EUR", "initial_minor": int64(100000), "example": true}); err != nil {
			return nil, err
		}
	}
	return map[string]any{"events": len(events), "example": true}, nil
}
