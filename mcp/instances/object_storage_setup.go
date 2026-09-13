package main

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Provider adapters provision accounts and credentials. Everything below uses
// the same S3 protocol and the platform credential vault, regardless of provider.
type ObjectStorageSetup struct {
	ProbeKey         string          `json:"probe_key,omitempty"`
	Private          bool            `json:"private"`
	CORSOrigins      []string        `json:"cors_origins"`
	CreateConnection bool            `json:"create_connection"`
	ConnectionID     int64           `json:"connection_id,omitempty"`
	Stage            string          `json:"stage"`
	Error            string          `json:"error,omitempty"`
	BucketStarted    bool            `json:"bucket_started,omitempty"`
	BucketCreated    bool            `json:"bucket_created,omitempty"`
	Capabilities     map[string]bool `json:"capabilities"`
}

func objectStorageSetupSchema() map[string]any {
	return schemaObject(map[string]any{
		"private":           map[string]any{"type": "boolean", "enum": []bool{true}, "description": "Private access is required; public buckets are unsupported."},
		"cors_origins":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Exact HTTP(S) browser origins; no wildcard. Empty disables CORS."},
		"create_connection": map[string]any{"type": "boolean", "description": "Keep the platform-managed S3 connection (default true). False exports credentials once and revokes the temporary setup connection."},
	}, nil)
}

func parseObjectStorageSetup(args map[string]any) (*ObjectStorageSetup, error) {
	out := &ObjectStorageSetup{Private: true, CreateConnection: true, CORSOrigins: []string{}, Stage: "provider", Capabilities: map[string]bool{}}
	if raw, ok := args["setup"]; ok {
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		// Decode only caller-owned desired state, never IDs or completed stages.
		in := struct {
			Private          bool     `json:"private"`
			CORSOrigins      []string `json:"cors_origins"`
			CreateConnection bool     `json:"create_connection"`
		}{Private: true, CreateConnection: true}
		if string(b) == "null" {
			return nil, errors.New("setup must be an object")
		}
		if err = dec.Decode(&in); err != nil {
			return nil, err
		}
		out.Private, out.CORSOrigins, out.CreateConnection = in.Private, in.CORSOrigins, in.CreateConnection
	}
	if !out.Private {
		return nil, errors.New("public buckets are unsupported; setup.private must be true")
	}
	if len(out.CORSOrigins) > 20 {
		return nil, errors.New("at most 20 CORS origins are supported")
	}
	for _, origin := range out.CORSOrigins {
		u, e := url.Parse(origin)
		if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.Contains(origin, "*") {
			return nil, fmt.Errorf("invalid CORS origin %q: supply an exact HTTP(S) origin without a path", origin)
		}
	}
	return out, nil
}

func persistObjectSetup(ctx *sdk.AppCtx, item *ObjectStorage) error {
	b, err := json.Marshal(item.Setup)
	if err != nil {
		return err
	}
	status := "configuring"
	if item.Setup.Stage == "ready" {
		status = "ready"
	}
	if item.Setup.Error != "" {
		status = "error"
	}
	item.Status = status
	item.ErrorMessage = item.Setup.Error
	return dbUpdateObjectStorage(ctx.AppDB(), item.ID, map[string]any{"setup_json": string(b), "status": status, "error_message": item.ErrorMessage, "bucket": item.Bucket})
}

func saveObjectStorageConnection(ctx *sdk.AppCtx, item *ObjectStorage, creds *ObjectStorageCredentials) error {
	if item.Setup == nil || creds == nil {
		return nil
	}
	endpoint, err := url.Parse(creds.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return errors.New("provider returned an invalid HTTPS S3 endpoint")
	}
	region := creds.Region
	if region == "" {
		region = "us-east-1"
	}
	conn, err := sdk.EnsureManagedConnection(ctx.PlatformAPI(), sdk.ManagedConnectionRequest{
		Key: fmt.Sprintf("instances:object-storage:%d", item.ID), AppSlug: "s3-compatible", Name: item.Name + " S3", AuthType: "aws_sigv4",
		Fields: map[string]string{"endpoint": strings.TrimRight(creds.Endpoint, "/"), "region": region, "bucket": item.Bucket, "access_key_id": creds.AccessKeyID, "secret_access_key": creds.SecretAccessKey, "force_path_style": "true"},
	})
	if err != nil {
		return fmt.Errorf("save managed S3 connection: %w", err)
	}
	if conn == nil || conn.ID <= 0 {
		return errors.New("managed S3 connection returned no ID")
	}
	item.Setup.ConnectionID = conn.ID
	return persistObjectSetup(ctx, item)
}

func objectSetupResponse(item *ObjectStorage, credentials *ObjectStorageCredentials) map[string]any {
	out := map[string]any{"object_storage": item, "credentials": credentials}
	if item.Setup != nil {
		out["setup"] = item.Setup
		out["connection_id"] = item.Setup.ConnectionID
		if item.Setup.Error != "" {
			out["warning"] = "Setup incomplete. Resume object_storage_create with this resource's id; no new subscription will be purchased."
		}
	} else if credentials != nil {
		out["warning"] = "Credentials are returned once; store them securely."
	}
	return out
}

func (a *App) ensureObjectStorage(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if id := int64Arg(args, "id"); id > 0 {
		return resumeObjectSetup(ctx, id, args)
	}
	// A durable caller key provides retry safety even when the create response is lost.
	key := strings.TrimSpace(strArg(args, "request_key"))
	if key != "" {
		var id int64
		err := ctx.AppDB().QueryRow(`SELECT id FROM object_storages WHERE request_key=?`, key).Scan(&id)
		if err == nil {
			return resumeObjectSetup(ctx, id, map[string]any{})
		}
	}
	in := CreateObjectStorageInput{Name: strArg(args, "name"), Provider: strArg(args, "provider"), ProviderConnectionID: int64Arg(args, "provider_connection_id"), Region: strArg(args, "region"), Plan: strArg(args, "plan"), Bucket: strArg(args, "bucket"), RequestKey: key}
	if !boolArg(args, "provision_only", false) {
		var err error
		in.Setup, err = parseObjectStorageSetup(args)
		if err != nil {
			return nil, err
		}
		if _, ok := ctx.PlatformAPI().(sdk.ManagedConnectionClient); !ok {
			return nil, errors.New("platform managed connections are required for S3 setup")
		}
		in.Bucket, err = validatedBucketName(in.Bucket, in.Name)
		if err != nil {
			return nil, err
		}
	}
	item, credentials, err := createObjectStorage(ctx, in)
	if err != nil {
		// Provider adapters persist identity before subsequent steps. Surface that
		// record on failure so callers can reconcile instead of repurchasing.
		if key != "" {
			var id int64
			if ctx.AppDB().QueryRow(`SELECT id FROM object_storages WHERE request_key=?`, key).Scan(&id) == nil {
				item, _ = dbGetObjectStorage(ctx.AppDB(), id)
				if item != nil {
					return objectSetupResponse(item, nil), nil
				}
			}
		}
		return nil, err
	}
	if item.Setup == nil {
		return objectSetupResponse(item, credentials), nil
	}
	unlock, err := lockResource(ctx.AppDB(), "object_storage", item.ID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if credentials == nil {
		item.Setup.Stage = "provider"
		item.Setup.Error = "Provider provisioning is pending; resume this resource by id."
		_ = persistObjectSetup(ctx, item)
		return objectSetupResponse(item, nil), nil
	}
	if err = saveObjectStorageConnection(ctx, item, credentials); err == nil {
		err = configureObjectStorage(ctx, item)
	}
	if err != nil {
		item.Setup.Error = err.Error()
		_ = persistObjectSetup(ctx, item)
	}
	if item.Setup.CreateConnection {
		credentials = nil
	}
	return objectSetupResponse(item, credentials), nil
}

func resumeObjectSetup(ctx *sdk.AppCtx, id int64, args map[string]any) (any, error) {
	// Rotation owns its own resource lock. It is only needed if a one-time
	// credential response was lost before reaching the credential vault.
	item, err := dbGetObjectStorage(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if item.Setup == nil {
		if _, ok := args["setup"]; !ok {
			return nil, errors.New("pass setup:{} to configure this existing resource")
		}
		desired, e := parseObjectStorageSetup(args)
		if e != nil {
			return nil, e
		}
		unlock, e := lockResource(ctx.AppDB(), "object_storage", id)
		if e != nil {
			return nil, e
		}
		item, e = dbGetObjectStorage(ctx.AppDB(), id)
		if e == nil && item.Status == "deleting" {
			e = errors.New("resource is deleting")
		}
		if e == nil && item.Setup == nil {
			if item.Bucket == "" {
				item.Bucket, e = validatedBucketName(strArg(args, "bucket"), item.Name)
			}
			if e == nil {
				item.Setup = desired
				e = persistObjectSetup(ctx, item)
			}
		}
		unlock()
		if e != nil {
			return nil, e
		}
	}
	if item.Setup.Stage == "ready" && args["setup"] == nil {
		return objectSetupResponse(item, nil), nil
	}
	if item.Status == "deleting" || strings.HasPrefix(item.ProviderID, "pending:") {
		return nil, errors.New("provider identity is unconfirmed or deleting; reconcile before setup")
	}
	var exported *ObjectStorageCredentials
	if item.Setup.ConnectionID == 0 {
		if item.Endpoint == "" {
			if err = refreshObjectStorageEndpoint(ctx, item); err != nil {
				return nil, err
			}
		}

		if exported, _, err = rotateObjectStorageCredentials(ctx, item); err != nil {
			return nil, err
		}
	}
	unlock, err := lockResource(ctx.AppDB(), "object_storage", id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	item, err = dbGetObjectStorage(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if item.Status == "deleting" {
		return nil, errors.New("resource is deleting")
	}
	if _, ok := args["setup"]; ok {
		desired, e := parseObjectStorageSetup(args)
		if e != nil {
			return nil, e
		}
		if desired.CreateConnection != item.Setup.CreateConnection {
			return nil, errors.New("create_connection cannot change after provisioning")
		}
		item.Setup.CORSOrigins = desired.CORSOrigins
	}
	item.Setup.Error = ""
	if err = configureObjectStorage(ctx, item); err != nil {
		item.Setup.Error = err.Error()
		_ = persistObjectSetup(ctx, item)
	}
	if item.Setup.CreateConnection {
		exported = nil
	}
	return objectSetupResponse(item, exported), nil
}

type s3SetupError struct {
	Status int
	Code   string
}

func (e *s3SetupError) Error() string {
	return fmt.Sprintf("S3 operation failed (status=%d code=%s); provider may not support this capability", e.Status, e.Code)
}
func s3SetupCall(ctx *sdk.AppCtx, item *ObjectStorage, tool string, args map[string]any) ([]byte, error) {
	if args == nil {
		args = map[string]any{}
	}
	args["bucket"] = item.Bucket
	result, err := ctx.PlatformAPI().ExecuteIntegrationTool(item.Setup.ConnectionID, tool, args)
	if err != nil {
		return nil, fmt.Errorf("S3 %s: %w", tool, err)
	}
	if result == nil {
		return nil, errors.New("empty S3 response")
	}
	data := []byte(result.Data)
	var text string
	if json.Unmarshal(data, &text) == nil {
		data = []byte(text)
	}
	var binary struct {
		Binary bool   `json:"_binary"`
		Base64 string `json:"base64"`
	}
	if json.Unmarshal(result.Data, &binary) == nil && binary.Binary {
		decoded, e := base64.StdEncoding.DecodeString(binary.Base64)
		if e != nil {
			return nil, e
		}
		data = decoded
	}
	if !result.Success || result.Status >= 400 {
		var e struct {
			Code string `xml:"Code"`
		}
		_ = xml.Unmarshal(data, &e)
		return nil, &s3SetupError{Status: result.Status, Code: e.Code}
	}
	return data, nil
}
func s3Missing(err error) bool { var e *s3SetupError; return errors.As(err, &e) && e.Status == 404 }

func configureObjectStorage(ctx *sdk.AppCtx, item *ObjectStorage) error {
	stage := func(name string) error { item.Setup.Stage = name; return persistObjectSetup(ctx, item) }
	item.Setup.Capabilities = map[string]bool{}
	if err := stage("bucket"); err != nil {
		return err
	}
	if !item.Setup.BucketCreated {
		if objectStorageOwnsBucket(item) {
			item.Setup.BucketCreated = true
		} else {
			_, err := s3SetupCall(ctx, item, "head_bucket", nil)
			if err == nil && !item.Setup.BucketStarted {
				return errors.New("bucket already exists and is not owned by this setup; choose a new bucket name")
			}
			if err != nil && !s3Missing(err) {
				return err
			}
			if s3Missing(err) {
				item.Setup.BucketStarted = true
				if err = stage("bucket"); err != nil {
					return err
				}
				if _, err = s3SetupCall(ctx, item, "create_bucket", map[string]any{"body": ""}); err != nil {
					return err
				}
			}
			item.Setup.BucketCreated = true
		}
		if err := persistObjectSetup(ctx, item); err != nil {
			return err
		}
	}
	if _, err := s3SetupCall(ctx, item, "head_bucket", nil); err != nil {
		return err
	}
	item.Setup.Capabilities["bucket"] = true
	if err := stage("privacy"); err != nil {
		return err
	}
	if _, err := s3SetupCall(ctx, item, "put_bucket_acl", nil); err != nil {
		return err
	}
	if _, err := s3SetupCall(ctx, item, "delete_bucket_policy", nil); err != nil && !s3Missing(err) {
		return err
	}
	acl, err := s3SetupCall(ctx, item, "get_bucket_acl", nil)
	if err != nil {
		return err
	}
	var policy struct {
		Owner struct {
			ID string `xml:"ID"`
		} `xml:"Owner"`
		Grants []struct {
			Grantee struct {
				ID  string `xml:"ID"`
				URI string `xml:"URI"`
			} `xml:"Grantee"`
		} `xml:"AccessControlList>Grant"`
	}
	if xml.Unmarshal(acl, &policy) != nil || policy.Owner.ID == "" || len(policy.Grants) == 0 {
		return errors.New("cannot verify private bucket ACL")
	}
	for _, grant := range policy.Grants {
		if grant.Grantee.URI != "" || grant.Grantee.ID != policy.Owner.ID {
			return errors.New("bucket ACL grants access outside its owner")
		}
	}
	if _, err = s3SetupCall(ctx, item, "get_bucket_policy", nil); !s3Missing(err) {
		return errors.New("cannot verify absence of bucket policy")
	}
	item.Setup.Capabilities["private"] = true
	if err = stage("cors"); err != nil {
		return err
	}
	if len(item.Setup.CORSOrigins) == 0 {
		if _, err = s3SetupCall(ctx, item, "delete_bucket_cors", nil); err != nil && !s3Missing(err) {
			return err
		}
		if _, err = s3SetupCall(ctx, item, "get_bucket_cors", nil); !s3Missing(err) {
			return errors.New("cannot verify CORS removal")
		}
	} else {
		rule := s3CORSRule{Origins: item.Setup.CORSOrigins, Methods: []string{"GET", "HEAD", "PUT", "POST", "DELETE"}, Headers: []string{"*"}, ExposeHeaders: []string{"ETag"}, MaxAge: 3600}
		config := s3CORSConfig{XMLNS: "http://s3.amazonaws.com/doc/2006-03-01/", Rules: []s3CORSRule{rule}}
		body, _ := xml.Marshal(config)
		checksum := md5.Sum(body) // S3 PutBucketCors requires Content-MD5.
		if _, err = s3SetupCall(ctx, item, "put_bucket_cors", map[string]any{"body": string(body), "content_md5": base64.StdEncoding.EncodeToString(checksum[:])}); err != nil {
			return err
		}
		data, e := s3SetupCall(ctx, item, "get_bucket_cors", nil)
		if e != nil {
			return e
		}
		var actual s3CORSConfig
		if xml.Unmarshal(data, &actual) != nil || len(actual.Rules) != 1 || !sameStrings(actual.Rules[0].Origins, rule.Origins) || !sameStrings(actual.Rules[0].Methods, rule.Methods) || !sameStrings(actual.Rules[0].Headers, rule.Headers) || !sameStrings(actual.Rules[0].ExposeHeaders, rule.ExposeHeaders) || actual.Rules[0].MaxAge != rule.MaxAge {
			return errors.New("CORS verification did not match requested origins and methods")
		}
	}
	item.Setup.Capabilities["cors"] = true
	if err = stage("verify"); err != nil {
		return err
	}
	if item.Setup.ProbeKey == "" {
		item.Setup.ProbeKey = ".apteva-setup-" + newRequestID()
		if err = persistObjectSetup(ctx, item); err != nil {
			return err
		}
	}
	key := item.Setup.ProbeKey
	payload := "Apteva S3 setup verification"
	if _, err = s3SetupCall(ctx, item, "put_object", map[string]any{"key": key, "body": payload}); err != nil {
		return err
	}
	data, readErr := s3SetupCall(ctx, item, "get_object", map[string]any{"key": key})
	_, deleteErr := s3SetupCall(ctx, item, "delete_object", map[string]any{"key": key})
	if deleteErr != nil {
		return fmt.Errorf("verification object %s cleanup failed: %w", key, deleteErr)
	}
	if readErr != nil {
		return readErr
	}
	if string(data) != payload {
		return errors.New("S3 roundtrip content mismatch")
	}
	item.Setup.ProbeKey = ""
	item.Setup.Capabilities["read_write_delete"] = true
	if !item.Setup.CreateConnection {
		if err = sdk.RevokeManagedConnection(ctx.PlatformAPI(), item.Setup.ConnectionID); err != nil {
			return err
		}
		item.Setup.ConnectionID = 0
	}
	item.Setup.Capabilities["managed_connection"] = item.Setup.CreateConnection
	return stage("ready")
}

type s3CORSConfig struct {
	XMLName xml.Name     `xml:"CORSConfiguration"`
	XMLNS   string       `xml:"xmlns,attr,omitempty"`
	Rules   []s3CORSRule `xml:"CORSRule"`
}
type s3CORSRule struct {
	Origins       []string `xml:"AllowedOrigin"`
	Methods       []string `xml:"AllowedMethod"`
	Headers       []string `xml:"AllowedHeader"`
	ExposeHeaders []string `xml:"ExposeHeader"`
	MaxAge        int      `xml:"MaxAgeSeconds"`
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, v := range a {
		m[v]++
	}
	for _, v := range b {
		m[v]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}
