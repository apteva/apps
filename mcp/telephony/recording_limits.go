package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
)

// Storage's supported ceiling. This bounds temporary import/playback files;
// capture retains its own independent policy and the durable local original.
const maxRecordingFileBytes int64 = 1 << 30

type recordingSizeLimitError struct{ Limit int64 }

func (e *recordingSizeLimitError) Error() string {
	return fmt.Sprintf("recording exceeds the configured import/playback limit (%d bytes)", e.Limit)
}

func recordingImportBlockReason(err error) string {
	var storageErr *storageProxyError
	if errors.As(err, &storageErr) && storageErr.StatusCode == http.StatusRequestEntityTooLarge {
		return "storage_file_too_large"
	}
	var sizeErr *recordingSizeLimitError
	if errors.As(err, &sizeErr) {
		return "recording_file_too_large"
	}
	return ""
}
func recordingStorageLabel(r recordingRow) string {
	switch r.StorageStatus {
	case "stored":
		return "Stored successfully"
	case "failed":
		if r.ProviderStatus != "completed" {
			return "Recording failed"
		}
		return "Storage import failed"
	case "deleted":
		return "Deleted"
	}
	if r.Provider == localRecordingProvider && r.ProviderStatus == "completed" {
		return "Recorded locally"
	}
	return ""
}

func recordingMaxFileArg(value any) int64 {
	var n int64
	switch v := value.(type) {
	case int:
		n = int64(v)
	case int64:
		n = v
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) || v < 1 || v > float64(maxRecordingFileBytes) {
			return -1
		}
		n = int64(v)
	case json.Number:
		n, _ = v.Int64()
	case string:
		n, _ = strconv.ParseInt(v, 10, 64)
	default:
		return -1
	}
	if n <= 0 {
		return -1
	}
	return n
}
