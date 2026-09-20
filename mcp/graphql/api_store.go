package main

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

type graphqlAPI struct {
	ID          int64
	ProjectID   string
	Slug        string
	Name        string
	Description string
	BasePath    string
	Hostname    string
	Status      string
	Default     bool
	CreatedAt   string
	UpdatedAt   string
}

var apiSlugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func normalizeAPISlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "default"
	}
	return value
}

func validateAPISlug(value string) error {
	if !apiSlugPattern.MatchString(value) {
		return invalid("api slug must start with a letter and contain only lowercase letters, numbers, and hyphens")
	}
	return nil
}

func storageProject(project, slug string) string {
	slug = normalizeAPISlug(slug)
	if slug == "default" {
		return project
	}
	return project + "\x00graphql_api:" + slug
}

func publicAPI(row graphqlAPI) map[string]any {
	endpoint := "/graphql/" + row.Slug
	if row.Default {
		endpoint = "/graphql"
	}
	return map[string]any{"id": row.ID, "project_id": row.ProjectID, "slug": row.Slug, "name": row.Name, "description": row.Description, "base_path": row.BasePath, "hostname": row.Hostname, "status": row.Status, "default": row.Default, "created_at": row.CreatedAt, "updated_at": row.UpdatedAt, "endpoint": endpoint}
}

func getGraphQLAPI(db *sql.DB, project, slug string) (*graphqlAPI, error) {
	slug = normalizeAPISlug(slug)
	var row graphqlAPI
	var defaultValue int
	err := db.QueryRow(`SELECT id, project_id, slug, name, description, base_path, hostname, status, default_api, created_at, updated_at FROM graphql_apis WHERE project_id=? AND slug=?`, project, slug).
		Scan(&row.ID, &row.ProjectID, &row.Slug, &row.Name, &row.Description, &row.BasePath, &row.Hostname, &row.Status, &defaultValue, &row.CreatedAt, &row.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.Default = defaultValue != 0
	return &row, nil
}

func ensureDefaultGraphQLAPI(db *sql.DB, project string) (*graphqlAPI, error) {
	if row, err := getGraphQLAPI(db, project, "default"); err != nil || row != nil {
		return row, err
	}
	now := nowUTC()
	_, err := db.Exec(`INSERT INTO graphql_apis(project_id,slug,name,status,default_api,created_at,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(project_id,slug) DO NOTHING`, project, "default", "Default GraphQL API", "active", 1, now, now)
	if err != nil {
		return nil, err
	}
	return getGraphQLAPI(db, project, "default")
}

func resolveGraphQLAPI(db *sql.DB, project, slug string) (*graphqlAPI, error) {
	slug = normalizeAPISlug(slug)
	row, err := getGraphQLAPI(db, project, slug)
	if err != nil {
		return nil, err
	}
	if row == nil && slug == "default" {
		return ensureDefaultGraphQLAPI(db, project)
	}
	if row == nil {
		return nil, invalid("graphql api %q not found", slug)
	}
	if row.Status != "active" {
		return nil, invalid("graphql api %q is not active", slug)
	}
	return row, nil
}

func listGraphQLAPIs(db *sql.DB, project string) ([]graphqlAPI, error) {
	if _, err := ensureDefaultGraphQLAPI(db, project); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, project_id, slug, name, description, base_path, hostname, status, default_api, created_at, updated_at FROM graphql_apis WHERE project_id=? ORDER BY default_api DESC, slug`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []graphqlAPI
	for rows.Next() {
		var row graphqlAPI
		var defaultValue int
		if err := rows.Scan(&row.ID, &row.ProjectID, &row.Slug, &row.Name, &row.Description, &row.BasePath, &row.Hostname, &row.Status, &defaultValue, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, err
		}
		row.Default = defaultValue != 0
		out = append(out, row)
	}
	return out, rows.Err()
}

func createGraphQLAPI(db *sql.DB, project, slug, name, description string) (*graphqlAPI, error) {
	slug = normalizeAPISlug(slug)
	if err := validateAPISlug(slug); err != nil {
		return nil, err
	}
	if slug == "default" {
		return nil, invalid("default api already exists")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = slug
	}
	now := nowUTC()
	if _, err := db.Exec(`INSERT INTO graphql_apis(project_id,slug,name,description,status,default_api,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, project, slug, name, strings.TrimSpace(description), "active", 0, now, now); err != nil {
		return nil, fmt.Errorf("create graphql api: %w", err)
	}
	return getGraphQLAPI(db, project, slug)
}

func apiSlugFromPath(path string) string {
	path = strings.TrimPrefix(path, "/graphql")
	path = strings.Trim(path, "/")
	if path == "" {
		return "default"
	}
	return strings.Split(path, "/")[0]
}

func realtimeAPISlugFromPath(path string) string {
	path = strings.TrimPrefix(path, "/realtime")
	path = strings.Trim(path, "/")
	if path == "" {
		return "default"
	}
	return strings.Split(path, "/")[0]
}

func getSchemaForAPI(db *sql.DB, project, apiSlug, environment string, version int, publishedOnly bool) (*schemaRecord, error) {
	return getSchema(db, storageProject(project, apiSlug), environment, version, publishedOnly)
}

func listSchemasForAPI(db *sql.DB, project, apiSlug, environment string) ([]schemaRecord, error) {
	return listSchemas(db, storageProject(project, apiSlug), environment)
}

func createSchemaForAPI(db *sql.DB, project, apiSlug, environment, sdl string, version int) (*schemaRecord, []string, error) {
	return createSchema(db, storageProject(project, apiSlug), environment, sdl, version)
}

func publishSchemaForAPI(db *sql.DB, project, apiSlug, environment string, version int) (*schemaRecord, error) {
	if err := validateSecurityBindings(db, project, apiSlug); err != nil {
		return nil, err
	}
	policy, err := getSecurity(db, project, apiSlug)
	if err != nil {
		return nil, err
	}
	if len(policy.Fields) > 0 {
		row, err := getSchemaForAPI(db, project, apiSlug, environment, version, false)
		if err != nil {
			return nil, err
		}
		if row == nil {
			return nil, invalid("schema not found")
		}
		schema, errs := validateSDL(row.SDL)
		if len(errs) > 0 {
			return nil, invalid("schema invalid")
		}
		for field := range policy.Fields {
			parts := strings.SplitN(field, ".", 2)
			typ := schema.Types[parts[0]]
			if typ == nil || typ.Fields.ForName(parts[1]) == nil {
				return nil, invalid("security policy field %s is not in the schema", field)
			}
		}
	}
	return publishSchema(db, storageProject(project, apiSlug), environment, version)
}

func createSourceForAPI(db *sql.DB, project, apiSlug, name, kind string, config map[string]any) (*sourceRecord, error) {
	return createSource(db, storageProject(project, apiSlug), name, kind, config)
}

func listSourcesForAPI(db *sql.DB, project, apiSlug string) ([]sourceRecord, error) {
	return listSources(db, storageProject(project, apiSlug))
}

func getSourceForAPI(db *sql.DB, project, apiSlug string, id int64, name string) (*sourceRecord, error) {
	return getSource(db, storageProject(project, apiSlug), id, name)
}

func upsertResolverForAPI(db *sql.DB, project, apiSlug, parentType, fieldName, operation string, sourceID int64, config map[string]any) (*resolverRecord, error) {
	if operation == aggregatePipelineOperation {
		policy, err := getSecurity(db, project, apiSlug)
		if err != nil {
			return nil, err
		}
		if len(policy.RowFilters[parentType+"."+fieldName]) > 0 {
			return nil, invalid("aggregate_pipeline cannot be combined with automatic row_filters; bind verified $identity parameters in its fixed SQL")
		}
	}
	return upsertResolver(db, storageProject(project, apiSlug), parentType, fieldName, operation, sourceID, config)
}

func getResolverForAPI(db *sql.DB, project, apiSlug, parentType, fieldName string) (*resolverRecord, error) {
	return getResolver(db, storageProject(project, apiSlug), parentType, fieldName)
}

func listResolversForAPI(db *sql.DB, project, apiSlug string) ([]resolverRecord, error) {
	return listResolvers(db, storageProject(project, apiSlug))
}

func createResolverModuleForAPI(db *sql.DB, project, apiSlug, name, description, outputType string, inputs, definition map[string]any, version int, deterministic bool) (*resolverModule, error) {
	return createResolverModule(db, storageProject(project, apiSlug), name, description, outputType, inputs, definition, version, deterministic)
}

func getResolverModuleForAPI(db *sql.DB, project, apiSlug, name string, version int, publishedOnly bool) (*resolverModule, error) {
	return getResolverModule(db, storageProject(project, apiSlug), name, version, publishedOnly)
}

func listResolverModulesForAPI(db *sql.DB, project, apiSlug string) ([]resolverModule, error) {
	return listResolverModules(db, storageProject(project, apiSlug))
}

func publishResolverModuleForAPI(db *sql.DB, project, apiSlug, name string, version int) (*resolverModule, error) {
	return publishResolverModule(db, storageProject(project, apiSlug), name, version)
}

func publicProjectID(value string) string {
	if index := strings.Index(value, "\x00graphql_api:"); index >= 0 {
		return value[:index]
	}
	return value
}
