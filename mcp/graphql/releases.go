package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type releaseLimits struct {
	MaxCost            int `json:"max_cost"`
	MaxDepth           int `json:"max_depth"`
	MaxRows            int `json:"max_rows"`
	MaxNestedResolvers int `json:"max_nested_resolvers"`
	MaxResponseBytes   int `json:"max_response_bytes"`
	MaxExecutionMS     int `json:"max_execution_ms"`
	MaxParallelism     int `json:"max_parallelism"`
	DefaultListSize    int `json:"default_list_size"`
}

func defaultReleaseLimits() releaseLimits {
	return releaseLimits{MaxCost: 100000, MaxDepth: 12, MaxRows: 10000, MaxNestedResolvers: 2000, MaxResponseBytes: 4 << 20, MaxExecutionMS: 15000, MaxParallelism: 8, DefaultListSize: 100}
}

func parseReleaseLimits(raw any) (releaseLimits, error) {
	limits := defaultReleaseLimits()
	if raw != nil {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return limits, invalid("invalid release limits")
		}
		decoder := json.NewDecoder(strings.NewReader(string(encoded)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&limits); err != nil {
			return limits, invalid("invalid release limits: %v", err)
		}
	}
	if limits.MaxCost < 1 || limits.MaxCost > 1_000_000_000 || limits.MaxDepth < 1 || limits.MaxDepth > 64 ||
		limits.MaxRows < 1 || limits.MaxRows > 10_000_000 || limits.MaxNestedResolvers < 1 || limits.MaxNestedResolvers > 1_000_000 ||
		limits.MaxResponseBytes < 1024 || limits.MaxResponseBytes > 64<<20 || limits.MaxExecutionMS < 10 || limits.MaxExecutionMS > 300_000 ||
		limits.MaxParallelism < 1 || limits.MaxParallelism > 64 || limits.DefaultListSize < 1 || limits.DefaultListSize > 10000 {
		return limits, invalid("release limits are outside supported bounds")
	}
	return limits, nil
}

type apiRelease struct {
	ID            int64            `json:"id"`
	ProjectID     string           `json:"project_id"`
	APISlug       string           `json:"api_slug"`
	Environment   string           `json:"environment"`
	Version       int              `json:"version"`
	Status        string           `json:"status"`
	SchemaVersion int              `json:"schema_version"`
	SchemaHash    string           `json:"schema_hash"`
	SchemaSDL     string           `json:"schema_sdl,omitempty"`
	Sources       []sourceRecord   `json:"sources"`
	Resolvers     []resolverRecord `json:"resolvers"`
	Security      securityPolicy   `json:"security"`
	Limits        releaseLimits    `json:"limits"`
	Modules       []resolverModule `json:"modules"`
	Checksum      string           `json:"checksum"`
	CreatedAt     string           `json:"created_at"`
	PublishedAt   string           `json:"published_at"`
}

const releaseColumns = `id,project_id,api_slug,environment,version,status,schema_version,schema_hash,schema_sdl,sources_json,resolvers_json,security_json,limits_json,modules_json,checksum,created_at,published_at`

func scanAPIRelease(scanner interface{ Scan(...any) error }) (*apiRelease, error) {
	var row apiRelease
	var sources, resolvers, security, limits, modules string
	err := scanner.Scan(&row.ID, &row.ProjectID, &row.APISlug, &row.Environment, &row.Version, &row.Status, &row.SchemaVersion, &row.SchemaHash, &row.SchemaSDL, &sources, &resolvers, &security, &limits, &modules, &row.Checksum, &row.CreatedAt, &row.PublishedAt)
	if err != nil {
		return nil, err
	}
	if json.Unmarshal([]byte(sources), &row.Sources) != nil || json.Unmarshal([]byte(resolvers), &row.Resolvers) != nil || json.Unmarshal([]byte(security), &row.Security) != nil || json.Unmarshal([]byte(limits), &row.Limits) != nil || json.Unmarshal([]byte(modules), &row.Modules) != nil {
		return nil, internal("invalid stored GraphQL release")
	}
	return &row, nil
}

func getAPIRelease(db *sql.DB, project, api, environment string, version int, activeOnly bool) (*apiRelease, error) {
	query := `SELECT ` + releaseColumns + ` FROM graphql_releases WHERE project_id=? AND api_slug=? AND environment=?`
	args := []any{project, normalizeAPISlug(api), normalizeEnvironment(environment)}
	if version > 0 {
		query += ` AND version=?`
		args = append(args, version)
	}
	if activeOnly {
		query += ` AND status='published'`
	}
	query += ` ORDER BY version DESC LIMIT 1`
	row, err := scanAPIRelease(db.QueryRow(query, args...))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return row, err
}

func getActiveAPIRelease(db *sql.DB, project, api string) (*apiRelease, error) {
	row, err := scanAPIRelease(db.QueryRow(`SELECT `+releaseColumns+` FROM graphql_releases WHERE project_id=? AND api_slug=? AND status='published' ORDER BY id DESC LIMIT 1`, project, normalizeAPISlug(api)))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return row, err
}

func listAPIReleases(db *sql.DB, project, api, environment string) ([]apiRelease, error) {
	rows, err := db.Query(`SELECT `+releaseColumns+` FROM graphql_releases WHERE project_id=? AND api_slug=? AND environment=? ORDER BY version DESC`, project, normalizeAPISlug(api), normalizeEnvironment(environment))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []apiRelease{}
	for rows.Next() {
		row, err := scanAPIRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *row)
	}
	return out, rows.Err()
}

func releaseChecksum(schema *schemaRecord, sources []sourceRecord, resolvers []resolverRecord, security securityPolicy, limits releaseLimits, modules []resolverModule) string {
	encoded, _ := json.Marshal([]any{schema.Hash, sources, resolvers, security, limits, modules})
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func validateReleaseSnapshot(schemaRow *schemaRecord, sources []sourceRecord, resolvers []resolverRecord, policy securityPolicy, modules []resolverModule) error {
	schema, problems := validateSDL(schemaRow.SDL)
	if len(problems) > 0 {
		return invalid("schema has validation errors")
	}
	if _, err := buildStandardSchema(schema, nil); err != nil {
		return invalid("schema is not executable: %s", err)
	}
	sourceByID := map[int64]sourceRecord{}
	for _, source := range sources {
		sourceByID[source.ID] = source
	}
	moduleByKey := map[string]resolverModule{}
	for _, module := range modules {
		moduleByKey[moduleKey(module.Name, module.Version)] = module
	}
	for _, resolver := range resolvers {
		definition := schema.Types[resolver.ParentType]
		if definition == nil || definition.Fields.ForName(resolver.FieldName) == nil {
			return invalid("resolver field %s.%s is not in the schema", resolver.ParentType, resolver.FieldName)
		}
		source, ok := sourceByID[resolver.SourceID]
		if !ok || source.Status != "active" {
			return invalid("resolver %s.%s references an unavailable source", resolver.ParentType, resolver.FieldName)
		}
		if source.Kind == "module" {
			name, _ := source.Config["module"].(string)
			key := moduleKey(name, moduleInt(source.Config["version"]))
			module, ok := moduleByKey[key]
			if !ok || module.Status != "published" {
				return invalid("resolver %s.%s references unpublished module %s", resolver.ParentType, resolver.FieldName, key)
			}
		}
		if source.Kind == "tables" {
			merged := mergeMaps(source.Config, resolver.Config)
			if err := validateRelationFilter(resolver.Operation, merged, sources); err != nil {
				return invalid("resolver %s.%s: %s", resolver.ParentType, resolver.FieldName, err)
			}
		}
	}
	for field := range policy.Fields {
		parts := strings.SplitN(field, ".", 2)
		definition := schema.Types[parts[0]]
		if definition == nil || definition.Fields.ForName(parts[1]) == nil {
			return invalid("security policy field %s is not in the schema", field)
		}
	}
	return nil
}

func publishAPIRelease(db *sql.DB, project, api, environment string, schemaVersion int, rawLimits any) (*apiRelease, error) {
	api = normalizeAPISlug(api)
	environment = normalizeEnvironment(environment)
	if _, err := resolveGraphQLAPI(db, project, api); err != nil {
		return nil, err
	}
	schemaRow, err := getSchemaForAPI(db, project, api, environment, schemaVersion, false)
	if err != nil || schemaRow == nil {
		return nil, invalid("schema version not found")
	}
	if len(schemaRow.ValidationError) > 0 || schemaRow.Status == "invalid" {
		return nil, invalid("schema has validation errors")
	}
	sources, err := listSourcesForAPI(db, project, api)
	if err != nil {
		return nil, err
	}
	resolvers, err := listResolversForAPI(db, project, api)
	if err != nil {
		return nil, err
	}
	policy, err := getSecurity(db, project, api)
	if err != nil {
		return nil, err
	}
	limits, err := parseReleaseLimits(rawLimits)
	if err != nil {
		return nil, err
	}
	allModules, err := listResolverModulesForAPI(db, project, api)
	if err != nil {
		return nil, err
	}
	modules := make([]resolverModule, 0, len(allModules))
	for _, module := range allModules {
		if module.Status == "published" {
			modules = append(modules, module)
		}
	}
	sort.Slice(modules, func(i, j int) bool {
		return moduleKey(modules[i].Name, modules[i].Version) < moduleKey(modules[j].Name, modules[j].Version)
	})
	if err := validateReleaseSnapshot(schemaRow, sources, resolvers, policy, modules); err != nil {
		return nil, err
	}
	encodedSources, _ := json.Marshal(sources)
	encodedResolvers, _ := json.Marshal(resolvers)
	encodedSecurity, _ := json.Marshal(policy)
	encodedLimits, _ := json.Marshal(limits)
	encodedModules, _ := json.Marshal(modules)
	checksum := releaseChecksum(schemaRow, sources, resolvers, policy, limits, modules)
	now := nowUTC()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	version := 1
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0)+1 FROM graphql_releases WHERE project_id=? AND api_slug=? AND environment=?`, project, api, environment).Scan(&version); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE graphql_releases SET status='archived' WHERE project_id=? AND api_slug=? AND environment=? AND status='published'`, project, api, environment); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE graphql_schemas SET status='archived' WHERE project_id=? AND environment=? AND status='published'`, storageProject(project, api), environment); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE graphql_schemas SET status='published',published_at=? WHERE id=?`, now, schemaRow.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO graphql_releases(project_id,api_slug,environment,version,status,schema_version,schema_hash,schema_sdl,sources_json,resolvers_json,security_json,limits_json,modules_json,checksum,created_at,published_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, project, api, environment, version, "published", schemaRow.Version, schemaRow.Hash, schemaRow.SDL, string(encodedSources), string(encodedResolvers), string(encodedSecurity), string(encodedLimits), string(encodedModules), checksum, now, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return getAPIRelease(db, project, api, environment, version, false)
}

func rollbackAPIRelease(db *sql.DB, project, api, environment string, version int) (*apiRelease, error) {
	if version <= 0 {
		return nil, invalid("release version is required")
	}
	api, environment = normalizeAPISlug(api), normalizeEnvironment(environment)
	target, err := getAPIRelease(db, project, api, environment, version, false)
	if err != nil || target == nil {
		return nil, invalid("release not found")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE graphql_releases SET status='archived' WHERE project_id=? AND api_slug=? AND environment=? AND status='published'`, project, api, environment); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE graphql_releases SET status='published',published_at=? WHERE id=?`, nowUTC(), target.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE graphql_schemas SET status='archived' WHERE project_id=? AND environment=? AND status='published'`, storageProject(project, api), environment); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE graphql_schemas SET status='published',published_at=? WHERE project_id=? AND environment=? AND version=?`, nowUTC(), storageProject(project, api), environment, target.SchemaVersion); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return getAPIRelease(db, project, api, environment, version, false)
}

func publicAPIRelease(row *apiRelease, includeSnapshot bool) map[string]any {
	if row == nil {
		return nil
	}
	out := map[string]any{"id": row.ID, "project_id": row.ProjectID, "api_slug": row.APISlug, "environment": row.Environment, "version": row.Version, "status": row.Status, "schema_version": row.SchemaVersion, "schema_hash": row.SchemaHash, "checksum": row.Checksum, "limits": row.Limits, "module_count": len(row.Modules), "source_count": len(row.Sources), "resolver_count": len(row.Resolvers), "created_at": row.CreatedAt, "published_at": row.PublishedAt}
	if includeSnapshot {
		out["schema_sdl"] = row.SchemaSDL
		out["sources"] = row.Sources
		out["resolvers"] = row.Resolvers
		out["security"] = row.Security
		out["modules"] = row.Modules
	}
	return out
}

func releaseDeadline(parent time.Time, limits releaseLimits) time.Time {
	deadline := time.Now().Add(time.Duration(limits.MaxExecutionMS) * time.Millisecond)
	if !parent.IsZero() && parent.Before(deadline) {
		return parent
	}
	return deadline
}

func releaseRuntimeKey(project, api, environment string, release int64) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d", project, normalizeAPISlug(api), normalizeEnvironment(environment), release)
}
