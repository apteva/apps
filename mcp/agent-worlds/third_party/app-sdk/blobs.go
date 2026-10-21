package sdk

import (
	"context"
	"errors"
)

// StoreBlobRequest registers server-owned bytes for one authorized thread.
// Data is a transport payload and must never be included in agent events.
type StoreBlobRequest struct {
	Scope    FileReferenceScope `json:"scope"`
	Filename string             `json:"filename,omitempty"`
	MIMEType string             `json:"mimeType"`
	Data     []byte             `json:"data"`
}

// BlobsClient is optional; existing custom PlatformClient implementations do
// not need new methods. The manifest permission is PermFileReferences.
type BlobsClient interface {
	StoreBlob(context.Context, StoreBlobRequest) (*FileHandle, error)
}

func (c *httpPlatformClient) StoreBlob(ctx context.Context, req StoreBlobRequest) (*FileHandle, error) {
	if int64(len(req.Data)) > MaxFileReferenceBytes {
		return nil, &FileReferenceError{Code: "file_too_large", Message: "blob exceeds 25 MiB", StatusCode: 413}
	}
	var out FileHandle
	err := c.fileTransportCall(ctx, c.slowClient, "/api/apps/callback/blobs", req, &out, 64<<10)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (p *projectScopedClient) StoreBlob(ctx context.Context, req StoreBlobRequest) (*FileHandle, error) {
	if req.Scope.ProjectID == "" {
		req.Scope.ProjectID = p.projectID
	}
	if req.Scope.ProjectID != p.projectID {
		return nil, errors.New("blob project differs from scoped client")
	}
	client, ok := p.inner.(BlobsClient)
	if !ok {
		return nil, errors.New("platform client does not support blobs")
	}
	return client.StoreBlob(ctx, req)
}
