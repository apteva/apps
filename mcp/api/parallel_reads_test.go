package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestGatewayReadPoolDoesNotWaitForWriterAndInvalidatesRoutes(t *testing.T) {
	_, ctx := mountTestApp(t)
	path := filepath.Join(t.TempDir(), "api.db")
	if _, err := ctx.AppDB().Exec("VACUUM INTO ?", path); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	defer releaseRouteCache(writer)
	reader, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(1000)")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer releaseRouteCache(reader)
	bindRouteReadPool(writer, reader)
	api, err := dbCreateAPI(writer, apiInput{ProjectID: testProject, Slug: "parallel-reads"})
	if err != nil {
		t.Fatal(err)
	}
	route, _, err := dbUpsertRoute(writer, routeInput{ProjectID: testProject, APIID: api.ID, Method: "POST", PathPattern: "/session", TargetKind: "http", TargetRef: "https://example.com", Enabled: true, TimeoutMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMaxOpenConns(1)
	tx, err := writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	done := make(chan error, 1)
	go func() { _, _, err := dbMatchRoute(reader, testProject, api.ID, "POST", "/session"); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("route lookup waited for occupied writer")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := dbDeleteRoute(writer, testProject, route.ID); err != nil {
		t.Fatal(err)
	}
	found, _, err := dbMatchRoute(reader, testProject, api.ID, "POST", "/session")
	if err != nil || found != nil {
		t.Fatalf("read pool retained deleted route: %+v %v", found, err)
	}
}
