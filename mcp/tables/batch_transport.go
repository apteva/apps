package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
)

// Compressed operation lists use the same parser, limits and transaction as
// ordinary batches. Bounds apply before decoding and before any DB work.
const maxCompressedBatchBytes = 8 << 20
const maxDecodedBatchBytes = 32 << 20

func decodeBatchOperations(args map[string]any) ([]any, error) {
	compressed, compressedPresent := args["operations_gzip_base64"]
	plain, plainPresent := args["operations"]
	if !compressedPresent {
		ops, ok := plain.([]any)
		if !ok || len(ops) == 0 {
			return nil, errf("operations is required and must be non-empty")
		}
		return ops, nil
	}
	if plainPresent {
		return nil, errf("provide either operations or operations_gzip_base64, not both")
	}
	encoded, ok := compressed.(string)
	if !ok || encoded == "" || len(encoded) > base64.StdEncoding.EncodedLen(maxCompressedBatchBytes) {
		return nil, errf("invalid or oversized operations_gzip_base64")
	}
	packed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(packed) > maxCompressedBatchBytes {
		return nil, errf("invalid or oversized operations_gzip_base64")
	}
	reader, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		return nil, errf("invalid gzip operations")
	}
	defer reader.Close()
	decoded, err := io.ReadAll(io.LimitReader(reader, maxDecodedBatchBytes+1))
	if err != nil {
		return nil, errf("invalid gzip operations")
	}
	if len(decoded) > maxDecodedBatchBytes {
		return nil, errf("decoded operations exceeds 32 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.UseNumber()
	var ops []any
	if err := decoder.Decode(&ops); err != nil || len(ops) == 0 {
		return nil, errf("compressed operations must be a non-empty JSON array")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errf("trailing data in compressed operations")
	}
	return ops, nil
}
