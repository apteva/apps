package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// Only the transport is shared. Credentials live in each request's client and
// are never persisted, registered as a connection, or sent to an integration.
var directS3HTTPClient = &http.Client{
	Timeout:       30 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
}

type directS3Client struct {
	endpoint    *url.URL
	credentials aws.Credentials
	region      string
}

func newDirectS3Client(creds *ObjectStorageCredentials) (*directS3Client, error) {
	if creds == nil || creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return nil, errors.New("S3 credentials are required")
	}
	u, err := url.Parse(creds.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("provider returned an invalid HTTPS S3 endpoint")
	}
	region := creds.Region
	if region == "" {
		region = "us-east-1"
	}
	return &directS3Client{endpoint: u, region: region, credentials: aws.Credentials{AccessKeyID: creds.AccessKeyID, SecretAccessKey: creds.SecretAccessKey}}, nil
}
func (c *directS3Client) call(bucket, operation string, args map[string]any) ([]byte, error) {
	method, query := "", ""
	switch operation {
	case "head_bucket":
		method = "HEAD"
	case "create_bucket":
		method = "PUT"
	case "put_bucket_acl":
		method, query = "PUT", "acl"
	case "get_bucket_acl":
		method, query = "GET", "acl"
	case "get_bucket_policy":
		method, query = "GET", "policy"
	case "delete_bucket_policy":
		method, query = "DELETE", "policy"
	case "put_bucket_cors":
		method, query = "PUT", "cors"
	case "get_bucket_cors":
		method, query = "GET", "cors"
	case "delete_bucket_cors":
		method, query = "DELETE", "cors"
	case "put_object":
		method = "PUT"
	case "get_object":
		method = "GET"
	case "delete_object":
		method = "DELETE"
	default:
		return nil, errors.New("unsupported S3 setup operation")
	}
	u := *c.endpoint
	u.Path = "/" + bucket
	if key, ok := args["key"].(string); ok {
		u.Path += "/" + key
	}
	if query != "" {
		u.RawQuery = url.Values{query: []string{""}}.Encode()
	}
	body, _ := args["body"].(string)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	if operation == "put_bucket_acl" {
		req.Header.Set("x-amz-acl", "private")
	}
	if operation == "put_bucket_cors" || operation == "create_bucket" {
		req.Header.Set("Content-Type", "application/xml")
	}
	if operation == "put_object" {
		req.Header.Set("Content-Type", "text/plain")
	}
	if checksum, ok := args["content_md5"].(string); ok {
		req.Header.Set("Content-MD5", checksum)
	}
	digest := sha256.Sum256([]byte(body))
	hash := hex.EncodeToString(digest[:])
	req.Header.Set("x-amz-content-sha256", hash)
	if err = v4.NewSigner().SignHTTP(ctx, c.credentials, req, hash, "s3", c.region, time.Now()); err != nil {
		return nil, errors.New("S3 request signing failed")
	}
	resp, err := directS3HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("S3 %s transport failed: %w", operation, err)
	}
	defer resp.Body.Close()
	const limit = 2 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errors.New("S3 setup response exceeds 2 MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Code string `xml:"Code"`
		}
		_ = xml.Unmarshal(data, &e)
		// Do not persist raw error bodies: servers can echo signed request data.
		return nil, &s3SetupError{Status: resp.StatusCode, Code: e.Code}
	}
	return data, nil
}
