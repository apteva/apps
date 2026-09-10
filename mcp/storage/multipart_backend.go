package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/minio/minio-go/v7"
)

// MultipartBackend is optional: old HTTP/MCP clients still send parts through
// Apteva. Browser clients negotiate direct transfer explicitly.
type multipartBackend interface {
	BeginMultipart(context.Context, string, string) (string, error)
	SignMultipartPart(context.Context, string, string, int, int64) (string, error)
	MultipartParts(context.Context, string, string) ([]remotePart, error)
	FinishMultipart(context.Context, string, string, []remotePart) error
	AbortMultipart(context.Context, string, string) error
}

type remotePart struct {
	Number int
	Size   int64
	ETag   string
}

func (s *s3Backend) BeginMultipart(c context.Context, key, contentType string) (string, error) {
	return (minio.Core{Client: s.client}).NewMultipartUpload(c, s.bucket, key, minio.PutObjectOptions{ContentType: contentType})
}
func (s *s3Backend) SignMultipartPart(c context.Context, key, id string, n int, size int64) (string, error) {
	// The browser sets Content-Length from the Blob. Signing it bounds each
	// capability to precisely the declared part size, including the final part.
	h := http.Header{"Content-Length": []string{strconv.FormatInt(size, 10)}}
	q := url.Values{"uploadId": []string{id}, "partNumber": []string{strconv.Itoa(n)}}
	u, err := s.client.PresignHeader(c, http.MethodPut, s.bucket, key, time.Hour, q, h)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
func (s *s3Backend) MultipartParts(c context.Context, key, id string) ([]remotePart, error) {
	core := minio.Core{Client: s.client}
	out := []remotePart{}
	marker := 0
	for {
		page, err := core.ListObjectParts(c, s.bucket, key, id, marker, 1000)
		if err != nil {
			return nil, err
		}
		for _, p := range page.ObjectParts {
			out = append(out, remotePart{p.PartNumber, p.Size, p.ETag})
		}
		if !page.IsTruncated {
			return out, nil
		}
		if page.NextPartNumberMarker <= marker || len(out) > maxPartNumber {
			return nil, fmt.Errorf("invalid multipart listing")
		}
		marker = page.NextPartNumberMarker
	}
}
func (s *s3Backend) FinishMultipart(c context.Context, key, id string, parts []remotePart) error {
	ps := make([]minio.CompletePart, len(parts))
	for i, p := range parts {
		ps[i] = minio.CompletePart{PartNumber: p.Number, ETag: p.ETag}
	}
	_, err := (minio.Core{Client: s.client}).CompleteMultipartUpload(c, s.bucket, key, id, ps, minio.PutObjectOptions{})
	return err
}
func (s *s3Backend) AbortMultipart(c context.Context, key, id string) error {
	err := (minio.Core{Client: s.client}).AbortMultipartUpload(c, s.bucket, key, id)
	if minio.ToErrorResponse(err).Code == "NoSuchUpload" {
		return nil
	}
	return err
}
