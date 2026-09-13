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
// the same direct S3 protocol, regardless of provider. Secrets remain in memory.
type ObjectStorageSetup struct {
	ProbeKey      string          `json:"probe_key,omitempty"`
	Private       bool            `json:"private"`
	CORSOrigins   []string        `json:"cors_origins"`
	Stage         string          `json:"stage"`
	Error         string          `json:"error,omitempty"`
	BucketStarted bool            `json:"bucket_started,omitempty"`
	BucketCreated bool            `json:"bucket_created,omitempty"`
	Capabilities  map[string]bool `json:"capabilities"`
}

func objectStorageSetupSchema() map[string]any {
	return schemaObject(map[string]any{
		"private":      map[string]any{"type": "boolean", "enum": []bool{true}, "description": "Private access is required; public buckets are unsupported."},
		"cors_origins": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Exact HTTP(S) browser origins; no wildcard. Empty disables CORS."},
	}, nil)
}

func parseObjectStorageSetup(args map[string]any) (*ObjectStorageSetup, error) {
	out := &ObjectStorageSetup{Private: true, CORSOrigins: []string{}, Stage: "provider", Capabilities: map[string]bool{}}
	if raw, ok := args["setup"]; ok {
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		if string(b) == "null" {
			return nil, errors.New("setup must be an object")
		}
		in := struct {
			Private     bool     `json:"private"`
			CORSOrigins []string `json:"cors_origins"`
		}{Private: true}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err = dec.Decode(&in); err != nil {
			return nil, err
		}
		out.Private, out.CORSOrigins = in.Private, in.CORSOrigins
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
	if item.Setup.Stage == "provider" {
		status = "provisioning"
	}
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

func objectSetupResponse(item *ObjectStorage, credentials *ObjectStorageCredentials) map[string]any {
	out := map[string]any{"object_storage": item, "credentials": credentials}
	if item.Status == "provisioning" {
		out["pending"] = true
		out["message"] = "Provider provisioning is pending; resume the same resource by id."
	}
	if item.Setup != nil {
		out["setup"] = item.Setup
		if item.Setup.Error != "" {
			out["warning"] = "Setup incomplete. Resume object_storage_create with this resource's id and the returned credentials; no new subscription will be purchased."
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
	key := strings.TrimSpace(strArg(args, "request_key"))
	if key != "" {
		var id int64
		if ctx.AppDB().QueryRow(`SELECT id FROM object_storages WHERE request_key=?`, key).Scan(&id) == nil {
			return resumeObjectSetup(ctx, id, args)
		}
	}
	in := CreateObjectStorageInput{Name: strArg(args, "name"), Provider: strArg(args, "provider"), ProviderConnectionID: int64Arg(args, "provider_connection_id"), Region: strArg(args, "region"), Plan: strArg(args, "plan"), Bucket: strArg(args, "bucket"), RequestKey: key}
	if !boolArg(args, "provision_only", false) {
		var err error
		in.Setup, err = parseObjectStorageSetup(args)
		if err != nil {
			return nil, err
		}
		in.Bucket, err = validatedBucketName(in.Bucket, in.Name)
		if err != nil {
			return nil, err
		}
	}
	item, creds, err := createObjectStorage(ctx, in)
	if err != nil {
		return nil, err
	}
	if item.Setup == nil {
		return objectSetupResponse(item, creds), nil
	}
	unlock, err := lockResource(ctx.AppDB(), "object_storage", item.ID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if item.Status == "provisioning" {
		item.Setup.Stage = "provider"
		item.Setup.Error = ""
		_ = persistObjectSetup(ctx, item)
		return objectSetupResponse(item, creds), nil
	}
	if creds == nil {
		item.Setup.Stage = "credentials"
		item.Setup.Error = "Credentials unavailable; supply credentials or explicitly set rotate_credentials=true."
		_ = persistObjectSetup(ctx, item)
		return objectSetupResponse(item, nil), nil
	}
	if err = configureObjectStorage(ctx, item, creds); err != nil {
		item.Setup.Error = err.Error()
		_ = persistObjectSetup(ctx, item)
	}
	return objectSetupResponse(item, creds), nil
}

func resumeObjectSetup(ctx *sdk.AppCtx, id int64, args map[string]any) (any, error) {
	unlock, err := lockResource(ctx.AppDB(), "object_storage", id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	item, err := dbGetObjectStorage(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if item.Status == "deleting" || strings.HasPrefix(item.ProviderID, "pending:") {
		return nil, errors.New("provider identity is unconfirmed or deleting; reconcile before setup")
	}
	var desired *ObjectStorageSetup
	if args["setup"] != nil || item.Setup == nil {
		desired, err = parseObjectStorageSetup(args)
		if err != nil {
			return nil, err
		}
	}
	// Refresh first, including when credentials were supplied or inventory says ready.
	// The helper persists metadata AND updates item before constructing credentials.
	providerCreds, pending, err := refreshObjectStorageDetails(ctx, item)
	if err != nil {
		return nil, err
	}
	if pending {
		return objectSetupResponse(item, nil), nil
	}
	if item.Setup != nil && item.Setup.Stage == "ready" && args["setup"] == nil && args["credentials"] == nil && !boolArg(args, "rotate_credentials", false) {
		return objectSetupResponse(item, nil), nil
	}
	creds, err := objectSetupCredentials(item, args)
	if err != nil {
		return nil, err
	}
	if boolArg(args, "rotate_credentials", false) {
		if creds, _, err = rotateObjectStorageCredentialsLocked(ctx, item); err != nil {
			return nil, err
		}
	} else if creds == nil {
		creds = providerCreds
	}
	if item.Setup == nil {
		item.Setup = desired
	}
	if item.Bucket == "" {
		item.Bucket, err = validatedBucketName(strArg(args, "bucket"), item.Name)
		if err != nil {
			return nil, err
		}
	}
	if desired != nil {
		item.Setup.CORSOrigins = desired.CORSOrigins
	}
	if creds == nil {
		item.Setup.Stage = "credentials"
		item.Setup.Error = "Credentials unavailable from provider. Supply credentials.access_key_id and credentials.secret_access_key, or explicitly set rotate_credentials=true if the keys were lost; no keys were rotated."
		if err = persistObjectSetup(ctx, item); err != nil {
			return nil, err
		}
		return objectSetupResponse(item, nil), nil
	}
	creds.Bucket = item.Bucket
	item.Setup.Error = ""
	if err = configureObjectStorage(ctx, item, creds); err != nil {
		item.Setup.Error = err.Error()
		_ = persistObjectSetup(ctx, item)
	}
	return objectSetupResponse(item, creds), nil
}

func objectSetupCredentials(item *ObjectStorage, args map[string]any) (*ObjectStorageCredentials, error) {
	raw, ok := args["credentials"]
	if !ok {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var fields struct {
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&fields); err != nil {
		return nil, errors.New("credentials must contain only access_key_id and secret_access_key")
	}
	if fields.AccessKeyID == "" || fields.SecretAccessKey == "" {
		return nil, errors.New("both access_key_id and secret_access_key are required")
	}
	return &ObjectStorageCredentials{Endpoint: item.Endpoint, Region: objectStorageSigningRegion(item), Bucket: item.Bucket, AccessKeyID: fields.AccessKeyID, SecretAccessKey: fields.SecretAccessKey, ShownOnce: true}, nil
}

type s3SetupError struct {
	Status int
	Code   string
}

func (e *s3SetupError) Error() string {
	if e.Status == 401 || e.Status == 403 {
		return "S3 rejected the credentials or bucket permissions; supply valid credentials or explicitly request rotation if keys were lost. No keys were automatically rotated."
	}
	return fmt.Sprintf("S3 operation failed (status=%d code=%s); provider may not support this capability", e.Status, e.Code)
}
func s3Missing(err error) bool { var e *s3SetupError; return errors.As(err, &e) && e.Status == 404 }

func configureObjectStorage(ctx *sdk.AppCtx, item *ObjectStorage, creds *ObjectStorageCredentials) error {
	client, err := newDirectS3Client(creds)
	if err != nil {
		return err
	}
	s3SetupCall := func(_ *sdk.AppCtx, _ *ObjectStorage, tool string, args map[string]any) ([]byte, error) {
		return client.call(item.Bucket, tool, args)
	}
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
