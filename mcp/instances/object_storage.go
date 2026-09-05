package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var (
	errObjectStorageNotFound = errors.New("object storage not found")
	bucketNamePattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
)

type ObjectStorage struct {
	ID                   int64  `json:"id"`
	Name                 string `json:"name"`
	Provider             string `json:"provider"`
	ProviderConnectionID int64  `json:"provider_connection_id"`
	ProviderID           string `json:"provider_id"`
	Status               string `json:"status"`
	Region               string `json:"region,omitempty"`
	Plan                 string `json:"plan,omitempty"`
	Endpoint             string `json:"endpoint,omitempty"`
	Bucket               string `json:"bucket,omitempty"`
	AccessKeyID          string `json:"access_key_id,omitempty"`
	ProviderMetadataJSON string `json:"-"`
	ErrorMessage         string `json:"error,omitempty"`
	CreatedAt            string `json:"created_at,omitempty"`
	UpdatedAt            string `json:"updated_at,omitempty"`
}

type ObjectStorageCredentials struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region,omitempty"`
	Bucket          string `json:"bucket,omitempty"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	ExpiresAt       string `json:"expires_at,omitempty"`
	Scope           string `json:"scope,omitempty"`
	ShownOnce       bool   `json:"shown_once"`
}

type CreateObjectStorageInput struct {
	Name                 string
	Provider             string
	ProviderConnectionID int64
	Region               string
	Plan                 string
	Bucket               string
}

type objectStorageMetadata struct {
	ProjectID      string `json:"project_id,omitempty"`
	OrganizationID string `json:"organization_id,omitempty"`
	ApplicationID  string `json:"application_id,omitempty"`
	PolicyID       string `json:"policy_id,omitempty"`
	KeyExpiresAt   string `json:"key_expires_at,omitempty"`
	PendingStep    string `json:"pending_step,omitempty"`
	BucketCreated  bool   `json:"bucket_created,omitempty"`
	ClusterID      int    `json:"cluster_id,omitempty"`
}

func scanObjectStorage(s rowScanner) (*ObjectStorage, error) {
	var item ObjectStorage
	err := s.Scan(&item.ID, &item.Name, &item.Provider, &item.ProviderConnectionID, &item.ProviderID,
		&item.Status, &item.Region, &item.Plan, &item.Endpoint, &item.Bucket, &item.AccessKeyID,
		&item.ProviderMetadataJSON, &item.ErrorMessage, &item.CreatedAt, &item.UpdatedAt)
	return &item, err
}

const objectStorageCols = `id, name, provider, provider_connection_id, provider_id, status,
	region, plan, endpoint, bucket, access_key_id, provider_metadata_json, error_message,
	COALESCE(created_at,''), COALESCE(updated_at,'')`

func dbCreateObjectStorage(db *sql.DB, in CreateObjectStorageInput, providerID, status, endpoint, accessKeyID, metadata string) (*ObjectStorage, error) {
	result, err := db.Exec(`INSERT INTO object_storages
		(name, provider, provider_connection_id, provider_id, status, region, plan, endpoint, bucket, access_key_id, provider_metadata_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.Provider, in.ProviderConnectionID, providerID, status, in.Region, in.Plan,
		endpoint, in.Bucket, accessKeyID, nullStr(metadata, "{}"), nowUTC(), nowUTC())
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	return dbGetObjectStorage(db, id)
}

func dbGetObjectStorage(db *sql.DB, id int64) (*ObjectStorage, error) {
	item, err := scanObjectStorage(db.QueryRow(`SELECT `+objectStorageCols+` FROM object_storages WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errObjectStorageNotFound
	}
	return item, err
}

func dbListObjectStorages(db *sql.DB, provider string) ([]*ObjectStorage, error) {
	query := `SELECT ` + objectStorageCols + ` FROM object_storages`
	args := []any{}
	if provider = normalizeProvider(provider); provider != "" {
		query += ` WHERE provider=?`
		args = append(args, provider)
	}
	query += ` ORDER BY id DESC`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []*ObjectStorage{}
	for rows.Next() {
		item, scanErr := scanObjectStorage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func dbUpdateObjectStorage(db *sql.DB, id int64, fields map[string]any) error {
	columns, args := []string{}, []any{}
	for _, key := range []string{"provider_id", "region", "status", "endpoint", "access_key_id", "provider_metadata_json", "error_message"} {
		if value, ok := fields[key]; ok {
			columns = append(columns, key+"=?")
			args = append(args, value)
		}
	}
	if len(columns) == 0 {
		return nil
	}
	columns = append(columns, "updated_at=?")
	args = append(args, nowUTC(), id)
	_, err := db.Exec(`UPDATE object_storages SET `+strings.Join(columns, ",")+` WHERE id=?`, args...)
	return err
}

func dbDeleteObjectStorage(db *sql.DB, id int64) error {
	result, err := db.Exec(`DELETE FROM object_storages WHERE id=?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return errObjectStorageNotFound
	}
	return nil
}

func objectStorageBinding(ctx *sdk.AppCtx, provider string, connectionID int64) (string, *sdk.BoundIntegration, error) {
	provider = normalizeProvider(provider)
	if provider == "" && connectionID > 0 {
		for _, bound := range ctx.IntegrationsFor("provider") {
			if bound != nil && bound.ConnectionID == connectionID {
				provider = providerSlugForBinding(ctx, bound)
				break
			}
		}
	}
	if provider == "" {
		resolved, err := resolveInstanceProvider(ctx, "")
		if err != nil {
			return "", nil, err
		}
		provider = resolved
	}
	if provider != "scaleway" && provider != "vultr" {
		return "", nil, fmt.Errorf("provider %q does not support object-storage provisioning through Instances; supported providers: scaleway, vultr", provider)
	}
	bound, err := storageBinding(ctx, provider, connectionID)
	return provider, bound, err
}

func objectStorageProviders(ctx *sdk.AppCtx) []InstanceProviderBinding {
	providers := []InstanceProviderBinding{}
	for _, item := range boundInstanceProviders(ctx) {
		if item.Provider == "scaleway" || item.Provider == "vultr" {
			providers = append(providers, item)
		}
	}
	return providers
}

func executeObjectStorageTool(ctx *sdk.AppCtx, connectionID int64, provider, tool string, args map[string]any) (json.RawMessage, error) {
	return executeVolumeTool(ctx, connectionID, provider, tool, args)
}

func objectStorageCatalog(ctx *sdk.AppCtx, provider string, connectionID int64) (map[string]any, error) {
	provider, bound, err := objectStorageBinding(ctx, provider, connectionID)
	if err != nil {
		return nil, err
	}
	if provider == "scaleway" {
		return map[string]any{
			"provider": provider, "connection_id": bound.ConnectionID,
			"locations": []map[string]any{
				{"id": "fr-par", "name": "Paris", "country": "FR"},
				{"id": "nl-ams", "name": "Amsterdam", "country": "NL"},
				{"id": "pl-waw", "name": "Warsaw", "country": "PL"},
				{"id": "it-mil", "name": "Milan", "country": "IT"},
			},
			"plans": []map[string]any{{"id": "standard", "name": "Object Storage", "billing": "usage"}},
		}, nil
	}
	clustersData, err := executeObjectStorageTool(ctx, bound.ConnectionID, provider, "object_storage_clusters_list", map[string]any{})
	if err != nil {
		return nil, err
	}
	tiersData, err := executeObjectStorageTool(ctx, bound.ConnectionID, provider, "object_storage_tiers_list", map[string]any{})
	if err != nil {
		return nil, err
	}
	clustersRoot, _ := decodeJSONObject(clustersData)
	tiersRoot, _ := decodeJSONObject(tiersData)
	return map[string]any{
		"provider": provider, "connection_id": bound.ConnectionID,
		"locations": firstArrayValue(clustersRoot, "clusters", "object_storage_clusters"),
		"plans":     firstArrayValue(tiersRoot, "tiers", "object_storage_tiers"),
	}, nil
}

func firstArrayValue(root map[string]any, keys ...string) []any {
	for _, key := range keys {
		if items, ok := root[key].([]any); ok {
			return items
		}
	}
	return []any{}
}

func createObjectStorage(ctx *sdk.AppCtx, in CreateObjectStorageInput) (*ObjectStorage, *ObjectStorageCredentials, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return nil, nil, errors.New("name is required")
	}
	provider, bound, err := objectStorageBinding(ctx, in.Provider, in.ProviderConnectionID)
	if err != nil {
		return nil, nil, err
	}
	in.Provider, in.ProviderConnectionID = provider, bound.ConnectionID
	if provider == "scaleway" {
		return createScalewayObjectStorage(ctx, in)
	}
	return createVultrObjectStorage(ctx, in)
}

func normalizeScalewayObjectRegion(region string) (string, error) {
	region = strings.ToLower(strings.TrimSpace(region))
	if region == "" {
		region = "fr-par"
	}
	for _, candidate := range []string{"fr-par", "nl-ams", "pl-waw", "it-mil"} {
		if region == candidate || strings.HasPrefix(region, candidate+"-") {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("unsupported Scaleway Object Storage region %q; use fr-par, nl-ams, pl-waw, or it-mil", region)
}

func validatedBucketName(name, logicalName string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		base := strings.ToLower(logicalName)
		base = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(base, "-")
		base = strings.Trim(base, "-")
		if len(base) > 45 {
			base = strings.Trim(base[:45], "-")
		}
		if base == "" {
			base = "storage"
		}
		name = "apteva-" + base + "-" + objectStorageSuffix()
	}
	if len(name) < 3 || len(name) > 63 || !bucketNamePattern.MatchString(name) || strings.Contains(name, "..") {
		return "", errors.New("bucket must be 3-63 lowercase letters, numbers, or hyphens; it must start and end with a letter or number")
	}
	return name, nil
}

func objectStorageSuffix() string {
	var bytes [4]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return strings.ReplaceAll(newRequestID()[:8], "-", "")
	}
	return hex.EncodeToString(bytes[:])
}

func createScalewayObjectStorage(ctx *sdk.AppCtx, in CreateObjectStorageInput) (*ObjectStorage, *ObjectStorageCredentials, error) {
	region, err := normalizeScalewayObjectRegion(in.Region)
	if err != nil {
		return nil, nil, err
	}
	bucket, err := validatedBucketName(in.Bucket, in.Name)
	if err != nil {
		return nil, nil, err
	}
	in.Region, in.Bucket = region, bucket
	if in.Plan == "" {
		in.Plan = "standard"
	}
	// Resolve the default project and IAM policy before creating anything. This
	// keeps an invalid credential/project configuration a genuinely read-only
	// failure and avoids a bucket that immediately needs rollback.
	projectID, err := scalewayDefaultProjectForConnection(ctx, in.ProviderConnectionID)
	if err != nil {
		return nil, nil, err
	}
	projectData, err := executeObjectStorageTool(ctx, in.ProviderConnectionID, "scaleway", "project_get", map[string]any{"project_id": projectID})
	if err != nil {
		return nil, nil, err
	}
	organizationID := jsonStringAt(projectData, "organization_id")
	if organizationID == "" {
		return nil, nil, errors.New("Scaleway project response did not include organization_id")
	}
	keyExpiresAt, err := scalewayObjectStorageKeyExpiry(ctx, in.ProviderConnectionID, organizationID)
	if err != nil {
		return nil, nil, fmt.Errorf("load Scaleway API-key expiration policy: %w", err)
	}
	if _, headErr := executeObjectStorageTool(ctx, in.ProviderConnectionID, "scaleway", "object_bucket_head", map[string]any{"bucket": bucket, "region": region}); headErr == nil {
		return nil, nil, errors.New("bucket already exists; refusing to adopt or replace an existing bucket")
	} else if !strings.Contains(headErr.Error(), "status=404") {
		return nil, nil, fmt.Errorf("verify bucket is absent before creation: %w", headErr)
	}
	endpoint := "https://s3." + region + ".scw.cloud"
	meta := objectStorageMetadata{ProjectID: projectID, OrganizationID: organizationID, KeyExpiresAt: keyExpiresAt, PendingStep: "bucket create"}
	encoded, _ := json.Marshal(meta)
	item, err := dbCreateObjectStorage(ctx.AppDB(), in, bucket, "provisioning", endpoint, "", string(encoded))
	if err != nil {
		return nil, nil, err
	}
	save := func(step string) error {
		meta.PendingStep = step
		b, _ := json.Marshal(meta)
		return dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"provider_metadata_json": string(b), "error_message": ""})
	}
	completed := false
	defer func() {
		if !completed {
			status := "error"
			if meta.BucketCreated {
				status = "deleting"
			}
			_ = dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"status": status, "error_message": "Provisioning interrupted at " + meta.PendingStep + "; known resource cleanup is queued; unknown provider outcomes remain recorded for reconciliation. Do not repeat create."})
		}
	}()
	if _, err = executeObjectStorageTool(ctx, in.ProviderConnectionID, "scaleway", "object_bucket_create", map[string]any{"bucket": bucket, "region": region}); err != nil {
		return nil, nil, err
	}
	meta.BucketCreated = true
	if err = save("IAM application create"); err != nil {
		return nil, nil, err
	}

	resourceName := scalewayIAMName(in.Name)
	appData, err := executeObjectStorageTool(ctx, in.ProviderConnectionID, "scaleway", "iam_application_create", map[string]any{
		"name": resourceName, "description": "Object Storage credentials managed by Apteva Instances", "organization_id": organizationID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create scoped Scaleway IAM application: %w", err)
	}
	applicationID := jsonStringAt(appData, "id")
	if applicationID == "" {
		return nil, nil, errors.New("Scaleway application create response did not include id")
	}
	meta.ApplicationID = applicationID
	if err = save("IAM policy create"); err != nil {
		return nil, nil, err
	}
	policyData, err := executeObjectStorageTool(ctx, in.ProviderConnectionID, "scaleway", "iam_policy_create", map[string]any{
		"name": resourceName, "description": "Object Storage access for " + in.Name,
		"organization_id": organizationID, "application_id": applicationID,
		"rules": []map[string]any{{"permission_set_names": []string{"ObjectStorageFullAccess"}, "project_ids": []string{projectID}}},
		"tags":  []string{"managed-by=apteva-instances"},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create scoped Scaleway IAM policy: %w", err)
	}
	policyID := jsonStringAt(policyData, "id")
	if policyID == "" {
		return nil, nil, errors.New("Scaleway IAM policy create response did not include id")
	}
	meta.PolicyID = policyID
	if err = save("API key create"); err != nil {
		return nil, nil, err
	}
	keyArgs := map[string]any{
		"application_id": applicationID, "default_project_id": projectID, "description": "Object Storage key for " + in.Name,
	}
	if keyExpiresAt != "" {
		keyArgs["expires_at"] = keyExpiresAt
	}
	keyData, err := executeObjectStorageTool(ctx, in.ProviderConnectionID, "scaleway", "iam_api_key_create", keyArgs)
	if err != nil {
		return nil, nil, fmt.Errorf("create scoped Scaleway Object Storage credentials: %w", err)
	}
	accessKeyID, secret := jsonStringAt(keyData, "access_key"), jsonStringAt(keyData, "secret_key")
	if accessKeyID != "" {
		if err = dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"access_key_id": accessKeyID}); err != nil {
			return nil, nil, err
		}
	}
	if accessKeyID == "" || secret == "" {
		return nil, nil, errors.New("Scaleway API key response did not include the one-time access and secret key")
	}
	if err = dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"access_key_id": accessKeyID}); err != nil {
		return nil, nil, err
	}
	if err = save(""); err != nil {
		return nil, nil, err
	}
	if err = dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"status": "ready"}); err != nil {
		return nil, nil, err
	}
	completed = true
	item, err = dbGetObjectStorage(ctx.AppDB(), item.ID)
	return item, &ObjectStorageCredentials{Endpoint: endpoint, Region: region, Bucket: bucket, AccessKeyID: accessKeyID, SecretAccessKey: secret, ExpiresAt: keyExpiresAt, Scope: "Scaleway project " + projectID + " (all Object Storage resources)", ShownOnce: true}, err
}

func scalewayObjectStorageKeyExpiry(ctx *sdk.AppCtx, connectionID int64, organizationID string) (string, error) {
	data, err := executeObjectStorageTool(ctx, connectionID, "scaleway", "iam_security_settings_get", map[string]any{"organization_id": organizationID})
	if err != nil {
		return "", err
	}
	maximum := strings.TrimSpace(jsonStringAt(data, "max_api_key_expiration_duration"))
	if maximum == "" || maximum == "0" || maximum == "0s" {
		return "", nil
	}
	duration, err := time.ParseDuration(maximum)
	if err != nil || duration <= 0 {
		return "", fmt.Errorf("invalid max_api_key_expiration_duration %q", maximum)
	}
	// Stay below the provider's exact maximum to tolerate small clock skew.
	margin := 5 * time.Minute
	if duration <= 10*time.Minute {
		margin = time.Second
	}
	if duration <= margin {
		return "", fmt.Errorf("API-key maximum duration %s is too short", maximum)
	}
	return time.Now().UTC().Add(duration - margin).Format(time.RFC3339), nil
}

func scalewayIAMName(name string) string {
	name = regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(strings.TrimSpace(name), "-")
	name = strings.Trim(name, "-.")
	if name == "" {
		name = "object-storage"
	}
	value := "apteva-" + name + "-" + objectStorageSuffix()
	if len(value) > 64 {
		value = value[:64]
	}
	return value
}

func createVultrObjectStorage(ctx *sdk.AppCtx, in CreateObjectStorageInput) (*ObjectStorage, *ObjectStorageCredentials, error) {
	clusterID, err := strconv.Atoi(strings.TrimSpace(in.Region))
	if err != nil || clusterID <= 0 {
		return nil, nil, errors.New("region must be a Vultr Object Storage cluster ID from object_storage_list_plans")
	}
	args := map[string]any{"label": in.Name, "cluster_id": clusterID}
	if in.Plan != "" {
		tierID, parseErr := strconv.Atoi(in.Plan)
		if parseErr != nil || tierID <= 0 {
			return nil, nil, errors.New("plan must be a Vultr Object Storage tier ID")
		}
		args["tier_id"] = tierID
	}
	metaBytes, _ := json.Marshal(objectStorageMetadata{ClusterID: clusterID, PendingStep: "provider create"})
	item, err := dbCreateObjectStorage(ctx.AppDB(), in, "pending:"+newRequestID(), "provisioning", "", "", string(metaBytes))
	if err != nil {
		return nil, nil, err
	}
	data, err := executeObjectStorageTool(ctx, in.ProviderConnectionID, "vultr", "object_storage_create", args)
	if err != nil {
		return nil, nil, err
	}
	root, _ := decodeJSONObject(data)
	obj := root
	if nested, ok := root["object_storage"].(map[string]any); ok {
		obj = nested
	}
	providerID := mapString(obj, "id")
	endpoint := mapString(obj, "s3_hostname")
	accessKey, secret := mapString(obj, "s3_access_key"), mapString(obj, "s3_secret_key")
	if providerID == "" {
		_ = dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"error_message": "Provider create outcome unknown; reconcile before retrying"})
		return nil, nil, errors.New("Vultr create returned no identity; pending record retained")
	}
	if err = dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"provider_id": providerID}); err != nil {
		return nil, nil, err
	}
	if endpoint == "" || accessKey == "" || secret == "" {
		_ = dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"error_message": "Provisioning pending; refresh provider state, then rotate credentials when ready"})
		item, _ = dbGetObjectStorage(ctx.AppDB(), item.ID)
		return item, nil, nil
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}
	if region := mapString(obj, "region"); region != "" {
		in.Region = region
	}
	metaBytes, _ = json.Marshal(objectStorageMetadata{ClusterID: clusterID})
	status := strings.ToLower(mapString(obj, "status"))
	if status == "" {
		status = "ready"
	}
	if err = dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"status": status, "endpoint": endpoint, "region": in.Region, "access_key_id": accessKey, "provider_metadata_json": string(metaBytes)}); err != nil {
		return nil, nil, err
	}
	item, err = dbGetObjectStorage(ctx.AppDB(), item.ID)
	if err != nil {
		return nil, nil, err
	}
	return item, &ObjectStorageCredentials{Endpoint: endpoint, Region: in.Region, AccessKeyID: accessKey, SecretAccessKey: secret, ShownOnce: true}, nil
}

func parseObjectStorageMetadata(item *ObjectStorage) objectStorageMetadata {
	var metadata objectStorageMetadata
	_ = json.Unmarshal([]byte(item.ProviderMetadataJSON), &metadata)
	return metadata
}

func rotateObjectStorageCredentials(ctx *sdk.AppCtx, item *ObjectStorage) (*ObjectStorageCredentials, []string, error) {
	unlock, err := lockResource(ctx.AppDB(), "object_storage", item.ID)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	item, err = dbGetObjectStorage(ctx.AppDB(), item.ID)
	if err != nil {
		return nil, nil, err
	}

	warnings := []string{}
	if item.Provider == "vultr" {
		data, err := executeObjectStorageTool(ctx, item.ProviderConnectionID, item.Provider, "object_storage_rotate_credentials", map[string]any{"object_storage_id": item.ProviderID})
		if err != nil {
			return nil, nil, err
		}
		accessKey, secret := findJSONScalar(data, "s3_access_key"), findJSONScalar(data, "s3_secret_key")
		endpoint := findJSONScalar(data, "s3_hostname")
		if endpoint == "" {
			endpoint = item.Endpoint
		} else if !strings.HasPrefix(endpoint, "http") {
			endpoint = "https://" + endpoint
		}
		if accessKey == "" || secret == "" {
			return nil, nil, errors.New("Vultr credential rotation did not return the new key pair")
		}
		if err := dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"access_key_id": accessKey, "endpoint": endpoint, "error_message": ""}); err != nil {
			return nil, nil, err
		}
		return &ObjectStorageCredentials{Endpoint: endpoint, Region: item.Region, AccessKeyID: accessKey, SecretAccessKey: secret, ShownOnce: true}, warnings, nil
	}
	metadata := parseObjectStorageMetadata(item)
	if metadata.ApplicationID == "" || metadata.ProjectID == "" {
		return nil, nil, errors.New("managed Scaleway IAM metadata is incomplete; credentials cannot be rotated")
	}
	keyExpiresAt, err := scalewayObjectStorageKeyExpiry(ctx, item.ProviderConnectionID, metadata.OrganizationID)
	if err != nil {
		return nil, nil, fmt.Errorf("load Scaleway API-key expiration policy: %w", err)
	}
	keyArgs := map[string]any{
		"application_id": metadata.ApplicationID, "default_project_id": metadata.ProjectID, "description": "Rotated Object Storage key for " + item.Name,
	}
	if keyExpiresAt != "" {
		keyArgs["expires_at"] = keyExpiresAt
	}
	if metadata.PendingStep != "" {
		return nil, nil, fmt.Errorf("unfinished credential operation %s must be reconciled before rotation", metadata.PendingStep)
	}
	metadata.PendingStep = "credential rotation; response may be lost"
	encoded, _ := json.Marshal(metadata)
	if err = dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"provider_metadata_json": string(encoded)}); err != nil {
		return nil, nil, err
	}
	data, err := executeObjectStorageTool(ctx, item.ProviderConnectionID, item.Provider, "iam_api_key_create", keyArgs)
	if err != nil {
		return nil, nil, err
	}
	accessKey, secret := jsonStringAt(data, "access_key"), jsonStringAt(data, "secret_key")
	if accessKey == "" || secret == "" {
		return nil, nil, errors.New("Scaleway credential rotation did not return the new key pair")
	}
	metadata.KeyExpiresAt = keyExpiresAt
	metadata.PendingStep = ""
	if err = commitObjectStorageRotation(ctx, item, accessKey, metadata); err != nil {
		_, _ = ctx.AppDB().Exec(`INSERT OR IGNORE INTO object_storage_key_cleanup(object_storage_id,connection_id,access_key_id,error) VALUES(?,?,?,?)`, item.ID, item.ProviderConnectionID, accessKey, "New key commit failed; revoke it")
		return nil, nil, err
	}
	warnings = cleanupObjectStorageKeys(ctx, item.ID)

	return &ObjectStorageCredentials{Endpoint: item.Endpoint, Region: item.Region, Bucket: item.Bucket, AccessKeyID: accessKey, SecretAccessKey: secret, ExpiresAt: keyExpiresAt, Scope: "Scaleway project " + metadata.ProjectID + " (all Object Storage resources)", ShownOnce: true}, warnings, nil
}

func destroyObjectStorage(ctx *sdk.AppCtx, item *ObjectStorage) ([]string, error) {
	unlock, err := lockResource(ctx.AppDB(), "object_storage", item.ID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	item, err = dbGetObjectStorage(ctx.AppDB(), item.ID)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(item.ProviderID, "pending:") || (item.Provider == "scaleway" && parseObjectStorageMetadata(item).PendingStep == "bucket create") {
		return nil, errors.New("create ownership is unconfirmed; reconcile provider identity before deletion")
	}
	if err = dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"status": "deleting"}); err != nil {
		return nil, err
	}

	warnings := []string{}
	if item.Provider == "vultr" {
		_, err := executeObjectStorageTool(ctx, item.ProviderConnectionID, item.Provider, "object_storage_delete", map[string]any{"object_storage_id": item.ProviderID})
		if err != nil && !strings.Contains(err.Error(), "status=404") {
			return nil, err
		}
	} else {
		_, err := executeObjectStorageTool(ctx, item.ProviderConnectionID, item.Provider, "object_bucket_delete", map[string]any{"bucket": item.Bucket, "region": item.Region})
		if err != nil && !strings.Contains(err.Error(), "status=404") {
			return nil, fmt.Errorf("delete Scaleway bucket (it must be empty): %w", err)
		}
		// Confirm provider state before deleting local inventory or IAM access.
		// HEAD is read-only and a 404 is the only successful deletion proof.
		if _, headErr := executeObjectStorageTool(ctx, item.ProviderConnectionID, item.Provider, "object_bucket_head", map[string]any{"bucket": item.Bucket, "region": item.Region}); headErr == nil {
			return nil, errors.New("Scaleway accepted bucket deletion but the bucket still exists")
		} else if !strings.Contains(headErr.Error(), "status=404") {
			return nil, fmt.Errorf("verify Scaleway bucket deletion: %w", headErr)
		}
		metadata := parseObjectStorageMetadata(item)
		cleanupCalls := []struct {
			tool string
			args map[string]any
		}{
			{"iam_api_key_delete", map[string]any{"access_key": item.AccessKeyID}},
			{"iam_policy_delete", map[string]any{"policy_id": metadata.PolicyID}},
			{"iam_application_delete", map[string]any{"application_id": metadata.ApplicationID}},
		}
		for _, call := range cleanupCalls {
			missing := false
			for _, value := range call.args {
				if strings.TrimSpace(fmt.Sprint(value)) == "" {
					missing = true
				}
			}
			if missing {
				continue
			}
			if _, cleanupErr := executeObjectStorageTool(ctx, item.ProviderConnectionID, item.Provider, call.tool, call.args); cleanupErr != nil && !strings.Contains(cleanupErr.Error(), "status=404") {
				warnings = append(warnings, call.tool+": "+cleanupErr.Error())
			}
		}
	}
	metadata := parseObjectStorageMetadata(item)
	if (metadata.PendingStep == "IAM application create" && metadata.ApplicationID == "") || (metadata.PendingStep == "IAM policy create" && metadata.PolicyID == "") || (metadata.PendingStep == "API key create" && item.AccessKeyID == "") || strings.HasPrefix(metadata.PendingStep, "credential rotation") {
		warnings = append(warnings, "A provider mutation response was lost; reconcile the unconfirmed IAM operation before removing this inventory record: "+metadata.PendingStep)
	}
	warnings = append(warnings, cleanupObjectStorageKeys(ctx, item.ID)...)
	if len(warnings) > 0 {
		sort.Strings(warnings)
		return warnings, fmt.Errorf("provider resource was deleted, but credential cleanup is incomplete: %s", strings.Join(warnings, "; "))
	}
	if err := dbDeleteObjectStorage(ctx.AppDB(), item.ID); err != nil {
		return warnings, err
	}
	return warnings, nil
}

func (a *App) toolObjectStorageListProviders(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	providers := objectStorageProviders(ctx)
	return map[string]any{"providers": providers, "count": len(providers)}, nil
}

func (a *App) toolObjectStorageListPlans(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return objectStorageCatalog(ctx, strArg(args, "provider"), int64Arg(args, "provider_connection_id"))
}

func (a *App) toolObjectStoragePreflight(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	provider := normalizeProvider(strArg(args, "provider"))
	connectionID := int64Arg(args, "provider_connection_id")
	provider, bound, err := objectStorageBinding(ctx, provider, connectionID)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"provider": provider, "connection_id": bound.ConnectionID, "authenticated": true, "mutated": false}
	if provider != "scaleway" {
		plans, err := objectStorageCatalog(ctx, provider, bound.ConnectionID)
		if err != nil {
			return nil, err
		}
		result["plans"] = plans
		return result, nil
	}
	region, err := normalizeScalewayObjectRegion(strArg(args, "region"))
	if err != nil {
		return nil, err
	}
	projectID, err := scalewayDefaultProjectForConnection(ctx, bound.ConnectionID)
	if err != nil {
		return nil, fmt.Errorf("Scaleway credential/default-project check failed: %w", err)
	}
	projectData, err := executeObjectStorageTool(ctx, bound.ConnectionID, provider, "project_get", map[string]any{"project_id": projectID})
	if err != nil {
		return nil, fmt.Errorf("Scaleway project access check failed: %w", err)
	}
	organizationID := findJSONScalar(projectData, "organization_id")
	if organizationID != "" {
		if _, err = executeObjectStorageTool(ctx, bound.ConnectionID, provider, "iam_security_settings_get", map[string]any{"organization_id": organizationID}); err != nil {
			return nil, fmt.Errorf("Scaleway IAM capability check failed: %w", err)
		}
	}
	result["project_id"], result["region"], result["iam_access"] = projectID, region, true
	if bucket := strings.TrimSpace(strArg(args, "bucket")); bucket != "" {
		_, headErr := executeObjectStorageTool(ctx, bound.ConnectionID, provider, "object_bucket_head", map[string]any{"bucket": bucket, "region": region})
		result["bucket"] = bucket
		if headErr == nil {
			result["bucket_available"] = false
		} else if strings.Contains(headErr.Error(), "status=404") {
			result["bucket_available"] = true
		} else {
			return nil, fmt.Errorf("Scaleway bucket availability check failed: %w", headErr)
		}
	}
	return result, nil
}

func (a *App) toolObjectStorageCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	item, credentials, err := createObjectStorage(ctx, CreateObjectStorageInput{
		Name: strArg(args, "name"), Provider: strArg(args, "provider"), ProviderConnectionID: int64Arg(args, "provider_connection_id"),
		Region: strArg(args, "region"), Plan: strArg(args, "plan"), Bucket: strArg(args, "bucket"),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"object_storage": item, "credentials": credentials,
		"warning": "The secret is not stored by Instances and is shown only in this response. Copy it now.",
	}, nil
}

func (a *App) toolObjectStorageGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	item, err := dbGetObjectStorage(ctx.AppDB(), int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"object_storage": item, "secret_available": false}, nil
}

func (a *App) toolObjectStorageList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	items, err := dbListObjectStorages(ctx.AppDB(), strArg(args, "provider"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"object_storages": items, "count": len(items)}, nil
}

func (a *App) toolObjectStorageRotateCredentials(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	item, err := dbGetObjectStorage(ctx.AppDB(), int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	credentials, warnings, err := rotateObjectStorageCredentials(ctx, item)
	if err != nil {
		return nil, err
	}
	updated, _ := dbGetObjectStorage(ctx.AppDB(), item.ID)
	return map[string]any{
		"object_storage": updated, "credentials": credentials, "warnings": warnings,
		"warning": "The new secret is not stored by Instances and is shown only in this response. Copy it now.",
	}, nil
}

func (a *App) toolObjectStorageDestroy(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if !boolArg(args, "confirm", false) {
		return nil, errors.New("confirm=true is required; this permanently deletes the provider resource and may delete stored data")
	}
	item, err := dbGetObjectStorage(ctx.AppDB(), int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	warnings, err := destroyObjectStorage(ctx, item)
	if err != nil {
		_ = dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"status": "error", "error_message": err.Error()})
		return nil, err
	}
	sort.Strings(warnings)
	return map[string]any{"destroyed": true, "id": item.ID, "warnings": warnings}, nil
}
