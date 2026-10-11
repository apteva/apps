package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func pipelineSourceFixture(version, count int) (map[string]any, []sourceRecord) {
	config := map[string]any{"version": 1, "engine": "tables_query", "sources": []any{}, "sql": "SELECT COUNT(*) AS total FROM {prospects}", "result": "single", "max_rows": 1}
	if version == 2 {
		config = stagedConfig()
	} else if version == 3 {
		config = snapshotConfig()
	}
	sources := make([]sourceRecord, 0, count)
	names := make([]any, 0, count)
	for i := 0; i < count; i++ {
		name := "prospects"
		if i > 0 {
			name = fmt.Sprintf("source_%02d", i+1)
		}
		sources = append(sources, sourceRecord{ID: int64(i + 1), Name: name, Kind: "tables", Status: "active", Config: map[string]any{"table": name}})
		names = append(names, name)
	}
	config["sources"] = names
	return config, sources
}

func TestAggregatePipelineSourceDeclarationCeilingAllVersions(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			for _, count := range []int{32, 33, 64} {
				t.Run(fmt.Sprint(count), func(t *testing.T) {
					config, sources := pipelineSourceFixture(version, count)
					plan, err := validateAggregatePipeline(aggregatePipelineOperation, config, sources, 1)
					if err != nil || len(plan.Tables) != count {
						t.Fatal(plan, err)
					}
					// The last declaration is allowed for SQL as well as the first one.
					if version == 1 {
						config["sql"] = strings.ReplaceAll(config["sql"].(string), "{prospects}", fmt.Sprintf("{source_%02d}", count))
					} else {
						first := config["stages"].([]any)[0].(map[string]any)
						first["sql"] = strings.ReplaceAll(first["sql"].(string), "{prospects}", fmt.Sprintf("{source_%02d}", count))
					}
					if _, err := validateAggregatePipeline(aggregatePipelineOperation, config, sources, 1); err != nil {
						t.Fatal("last declared table rejected", err)
					}
				})
			}
			for _, count := range []int{0, 65} {
				config, sources := pipelineSourceFixture(version, count)
				_, err := validateAggregatePipeline(aggregatePipelineOperation, config, sources, 1)
				if err == nil || !strings.Contains(err.Error(), "sources must contain 1 to 64 source names") {
					t.Fatal(count, err)
				}
			}
		})
	}
}

func TestAggregatePipelineExpandedDeclarationsPreserveSourceAuthorization(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			edits := map[string]func(map[string]any, []sourceRecord){
				"duplicate":     func(c map[string]any, s []sourceRecord) { c["sources"].([]any)[63] = "prospects" },
				"unknown":       func(c map[string]any, s []sourceRecord) { c["sources"].([]any)[63] = "unknown" },
				"inactive":      func(c map[string]any, s []sourceRecord) { s[63].Status = "inactive" },
				"wrong kind":    func(c map[string]any, s []sourceRecord) { s[63].Kind = "http" },
				"missing table": func(c map[string]any, s []sourceRecord) { delete(s[63].Config, "table") },
				"dynamic table": func(c map[string]any, s []sourceRecord) {
					s[63].Config["table"] = map[string]any{"from": "$args.table"}
				},
				"invalid table":        func(c map[string]any, s []sourceRecord) { s[63].Config["table"] = "invalid table" },
				"bound source omitted": func(c map[string]any, s []sourceRecord) { c["sources"] = c["sources"].([]any)[1:] },
				"undeclared SQL table": func(c map[string]any, s []sourceRecord) {
					if version == 1 {
						c["sql"] = "SELECT * FROM {undeclared}"
					} else {
						stage := c["stages"].([]any)[0].(map[string]any)
						stage["sql"] = strings.ReplaceAll(stage["sql"].(string), "{prospects}", "{undeclared}")
					}
				},
			}
			for _, key := range []string{"where", "identity_where", "relation", "relation_filter", "distinct_by"} {
				edits["restricted "+key] = func(c map[string]any, s []sourceRecord) { s[63].Config[key] = map[string]any{} }
			}
			for name, edit := range edits {
				t.Run(name, func(t *testing.T) {
					c, s := pipelineSourceFixture(version, 64)
					edit(c, s)
					if _, err := validateAggregatePipeline(aggregatePipelineOperation, c, s, 1); err == nil {
						t.Fatal("unsafe declaration accepted")
					}
				})
			}
		})
	}
}

func TestAggregatePipelineMoreDeclarationsDoNotAddReadsOrChangeLimits(t *testing.T) {
	ctx := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Tenant: "north"})
	for _, version := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			baseline, source := pipelineSourceFixture(version, 1)
			expanded, sources := pipelineSourceFixture(version, 64)
			before, err := validateAggregatePipeline(aggregatePipelineOperation, baseline, source, 1)
			if err != nil {
				t.Fatal(err)
			}
			after, err := validateAggregatePipeline(aggregatePipelineOperation, expanded, sources, 1)
			if err != nil {
				t.Fatal(err)
			}
			first, err := buildAggregatePipelineInput(ctx, before, map[string]any{"extra": 3}, nil, "p1")
			if err != nil {
				t.Fatal(err)
			}
			second, err := buildAggregatePipelineInput(ctx, after, map[string]any{"extra": 3}, nil, "p1")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(before.Stages, after.Stages) || before.MaxRows != after.MaxRows || before.OnTruncated != after.OnTruncated {
				t.Fatal("declarations changed execution contract", first, second)
			}
			if err := after.validateStageLimits(defaultReleaseLimits()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAggregatePipeline64SourcesPreserveRequestPermissionChecks(t *testing.T) {
	platform := &stagedPlatform{db: stagedDB(t)}
	app := secureTestApp(t, platform)
	db := app.ctx.AppDB()
	schema, issues, err := createSchemaForAPI(db, "p1", "default", "production", `type Query {summary(extra:Int=3): Metric} type Metric{total:Int!}`, 0)
	if err != nil || len(issues) > 0 {
		t.Fatal(issues, err)
	}
	config, sources := pipelineSourceFixture(3, 64)
	var bound int64
	for _, s := range sources {
		source, err := createSourceForAPI(db, "p1", "default", s.Name, s.Kind, s.Config)
		if err != nil {
			t.Fatal(err)
		}
		if s.Name == "prospects" {
			bound = source.ID
		}
	}
	if _, err := upsertResolverForAPI(db, "p1", "default", "Query", "summary", aggregatePipelineOperation, bound, config); err != nil {
		t.Fatal(err)
	}
	if _, err := setSecurity(db, "p1", "default", map[string]any{"mode": "auth", "tenant_id": "north", "environment": "production", "fields": map[string]any{"Query.summary": []any{"summary:read"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := publishAPIRelease(db, "p1", "default", "production", schema.Version, nil); err != nil {
		t.Fatal(err)
	}
	app.invalidateRuntime("p1", "default")
	ctx := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Subject: "user1", Issuer: "apteva:auth:north", Project: "p1", API: "default", Tenant: "north", Expires: time.Now().Add(time.Hour)})
	denied, err := app.execute(ctx, "p1", "default", "production", graphqlRequest{Query: `{summary{total}}`})
	if errorCode(err) != "permission_denied" || denied.Data != nil {
		t.Fatal("permission check bypassed", denied, err)
	}
	if platform.calls != 0 {
		t.Fatal("unauthorized request reached Tables", platform.calls)
	}
	authorized := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Subject: "user1", Issuer: "apteva:auth:north", Project: "p1", API: "default", Tenant: "north", Permissions: []string{"summary:read"}, Expires: time.Now().Add(time.Hour)})
	out, err := app.execute(authorized, "p1", "default", "production", graphqlRequest{Query: `{summary{total}}`})
	if err != nil || len(out.Errors) > 0 || fmt.Sprint(out.Data["summary"].(map[string]any)["total"]) != "5" || platform.calls != 1 {
		t.Fatal(out, err, platform.calls)
	}
}
