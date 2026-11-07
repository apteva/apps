package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResponsesHideUpstreamErrorDetails(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{errors.New("dial tcp 10.0.0.4:5280: connect: refused https://secret.example"), "The scene source is unavailable."},
		{publicError{"Unknown scene source."}, "Unknown scene source."},
		{fmt.Errorf("wrapped: %w", context.DeadlineExceeded), "The scene source did not respond in time."},
	} {
		recorder := httptest.NewRecorder()
		(&App{}).respond(recorder, nil, tc.err)
		var body map[string]string
		_ = json.NewDecoder(recorder.Body).Decode(&body)
		if body["error"] != tc.want || strings.Contains(recorder.Body.String(), "secret") {
			t.Fatalf("error %q rendered %q", tc.err, body["error"])
		}
	}
}

func TestCacheReplacesOldestEntryWhenFull(t *testing.T) {
	var swept time.Time
	cache := map[string]topologyCache{}
	base := time.Now().Add(-time.Second)
	for i := 0; i < cacheCapacity; i++ {
		cache = storeCached(cache, &swept, fmt.Sprint(i), topologyCache{at: base.Add(time.Duration(i) * time.Microsecond)}, topologyTTL)
	}
	cache = storeCached(cache, &swept, "new", topologyCache{at: time.Now()}, topologyTTL)
	if len(cache) != cacheCapacity {
		t.Fatalf("cache grew past capacity: %d", len(cache))
	}
	if _, ok := cache["0"]; ok {
		t.Fatal("oldest entry should have been replaced")
	}
	if _, ok := cache["new"]; !ok {
		t.Fatal("new entry should be cached")
	}
}
