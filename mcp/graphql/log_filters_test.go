package main

import (
	"net/url"
	"testing"
)

func TestLogFilterQueryParsesSlowAndHeavyDimensions(t *testing.T) {
	values := url.Values{
		"min_duration_ms":    {"500"},
		"min_response_bytes": {"1048576"},
		"min_rows":           {"100"},
		"min_resolvers":      {"20"},
		"status_code":        {"500"},
		"operation_name":     {"GetOrders"},
		"operation_type":     {"query"},
		"sort_by":            {"duration_ms"},
		"sort_order":         {"asc"},
		"limit":              {"25"},
	}
	filter, err := parseLogFiltersQuery(values)
	if err != nil {
		t.Fatal(err)
	}
	if filter.limit != 25 || filter.sortBy != "duration_ms" || filter.sortOrder != "asc" || filter.operationName != "GetOrders" {
		t.Fatalf("unexpected filter: %#v", filter)
	}
	if filter.minDurationMS == nil || *filter.minDurationMS != 500 || filter.minResponseBytes == nil || *filter.minResponseBytes != 1048576 || filter.statusCode == nil || *filter.statusCode != 500 {
		t.Fatalf("numeric filters not parsed: %#v", filter)
	}
}

func TestLogFilterRejectsInvalidBoundsAndSort(t *testing.T) {
	if _, err := parseLogFiltersQuery(url.Values{"min_duration_ms": {"5"}, "max_duration_ms": {"4"}}); err == nil {
		t.Fatal("expected inverted duration bounds to fail")
	}
	if _, err := parseLogFiltersQuery(url.Values{"sort_by": {"operation_hash"}}); err == nil {
		t.Fatal("expected unsupported sort field to fail")
	}
	if _, err := parseLogFiltersQuery(url.Values{"since": {"not-a-timestamp"}}); err == nil {
		t.Fatal("expected invalid timestamp to fail")
	}
}

func TestPublicLogsFilteredSortsAndLimits(t *testing.T) {
	db := testDB(t)
	for _, row := range []struct {
		name     string
		duration int
		bytes    int
	}{
		{"Fast", 20, 100},
		{"Slow", 900, 200},
		{"Slowest", 1500, 300},
	} {
		_, err := db.Exec(`INSERT INTO graphql_request_logs(project_id, operation_name, operation_type, status_code, duration_ms, error, created_at, operation_hash, api_release, response_bytes, row_count, resolver_count, source_timings_json, error_codes_json, authorization_scope, request_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "filter-project", row.name, "query", 200, row.duration, "", "2026-01-01T00:00:00Z", row.name, 1, row.bytes, 1, 1, `{}`, `[]`, "", row.name)
		if err != nil {
			t.Fatal(err)
		}
	}
	filter := defaultLogFilter()
	filter.minDurationMS = ptrInt64(500)
	filter.sortBy = "duration_ms"
	rows, err := publicLogsFiltered(db, "filter-project", filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["operation_name"] != "Slowest" || rows[1]["operation_name"] != "Slow" {
		t.Fatalf("unexpected filtered rows: %#v", rows)
	}
}

func ptrInt64(value int64) *int64 { return &value }
