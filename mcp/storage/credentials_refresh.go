package main

import (
	"errors"
	sdk "github.com/apteva/app-sdk"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"time"
)

// The SDK serializes provider calls. Refreshing credentials preserves the
// original object location; an endpoint/region change requires a restart and
// the backend identity/migration check.
type refreshingS3Credentials struct {
	app          *sdk.AppCtx
	connectionID int64
	location     s3ResolvedConnection
	value        credentials.Value
	expires      time.Time
}

func (p *refreshingS3Credentials) Retrieve() (credentials.Value, error) {
	if time.Now().Before(p.expires) {
		return p.value, nil
	}
	raw, err := p.app.PlatformAPI().GetConnectionCredentials(p.connectionID)
	if err != nil {
		return credentials.Value{}, err
	}
	resolved, err := resolveS3Connection(raw)
	if err != nil {
		return credentials.Value{}, err
	}
	if resolved.endpoint != p.location.endpoint || resolved.region != p.location.region || resolved.useSSL != p.location.useSSL || resolved.forcePathStyle != p.location.forcePathStyle || resolved.forceVirtualHost != p.location.forceVirtualHost {
		return credentials.Value{}, errors.New("backend location changed; verified migration required")
	}
	p.value = credentials.Value{AccessKeyID: resolved.accessKey, SecretAccessKey: resolved.secretKey, SignerType: credentials.SignatureV4}
	p.expires = time.Now().Add(5 * time.Minute)
	return p.value, nil
}
func (p *refreshingS3Credentials) IsExpired() bool { return !time.Now().Before(p.expires) }

func (p *refreshingS3Credentials) RetrieveWithCredContext(_ *credentials.CredContext) (credentials.Value, error) {
	return p.Retrieve()
}
