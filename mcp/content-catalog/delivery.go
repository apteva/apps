package main

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Delivery verification answers "is the ready provider video the exact master
// we meant to host, with the expected length?" It reads Storage, Media and the
// provider's video record only. It never uploads, re-fetches or reads the CDN.
const (
	// Bunny reports length in whole seconds, so up to one second of the
	// difference is provider rounding rather than a delivery problem.
	providerRoundingMS = 1000
	// Audio and container track lengths commonly differ by a few frames.
	containerToleranceMS = 500
	deliveryMaxAttempts  = 20
)

var sha256Hex = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

type DeliveryReceipt struct {
	DeliveryStatus    string              `json:"delivery_status"`
	Reason            string              `json:"reason,omitempty"`
	ProviderReadiness string              `json:"provider_readiness"`
	Source            DeliverySource      `json:"source"`
	Destination       DeliveryDestination `json:"destination"`
	Duration          DeliveryDuration    `json:"duration"`
	Warnings          []string            `json:"warnings"`
	CheckedAt         string              `json:"checked_at"`
	VerifiedAt        string              `json:"verified_at,omitempty"`
	NextCheckAt       string              `json:"next_check_at,omitempty"`
}

type DeliverySource struct {
	StorageFileID         string `json:"storage_file_id"`
	HostedSHA256          string `json:"hosted_sha256"`
	CurrentSHA256         string `json:"current_sha256"`
	CurrentChecksumStatus string `json:"current_checksum_status"`
	MediaSHA256           string `json:"media_sha256"`
	HashMatch             string `json:"hash_match"`
	ProviderHash          string `json:"provider_hash,omitempty"`
	ProviderHashMatch     string `json:"provider_hash_match"`
}

type DeliveryDestination struct {
	Provider            string `json:"provider"`
	ConnectionID        int64  `json:"connection_id"`
	LibraryID           string `json:"library_id"`
	CollectionID        string `json:"collection_id"`
	RemoteID            string `json:"remote_id"`
	GUIDConfirmed       bool   `json:"guid_confirmed"`
	LibraryConfirmed    bool   `json:"library_confirmed"`
	CollectionConfirmed bool   `json:"collection_confirmed"`
	ProviderStatus      *int64 `json:"provider_status"`
	ProviderStage       string `json:"provider_stage"`
	EmbedURL            string `json:"embed_url,omitempty"`
}

type DeliveryDuration struct {
	SourceMS              int64  `json:"source_ms"`
	ProviderSeconds       int64  `json:"provider_seconds"`
	DifferenceMS          int64  `json:"difference_ms"`
	ProviderRoundingMS    int64  `json:"provider_rounding_ms"`
	ContainerDifferenceMS int64  `json:"container_difference_ms"`
	ToleranceMS           int64  `json:"tolerance_ms"`
	Status                string `json:"status"`
}

// deliverySourceEvidence is everything read outside the provider. Read errors
// are kept as reasons so evaluateDelivery stays a pure decision.
type deliverySourceEvidence struct {
	StorageFileID  string
	StorageError   string
	ChecksumStatus string
	CurrentSHA256  string
	MediaBound     bool
	MediaError     string
	MediaFound     bool
	MediaSHA256    string
	MediaProbe     string
	MediaMS        int64
}

func readDeliverySource(ctx *sdk.AppCtx, pid string, h *Hosting) deliverySourceEvidence {
	ev := deliverySourceEvidence{}
	asset, err := assetByID(ctx.AppDB(), pid, h.AssetID)
	if err != nil {
		ev.StorageError = err.Error()
		return ev
	}
	ev.StorageFileID = asset.StorageFileID
	fileID, err := strconv.ParseInt(asset.StorageFileID, 10, 64)
	if err != nil {
		ev.StorageError = "asset has no numeric Storage file ID"
	} else if status, e := refreshAssetStorageChecksum(ctx, pid, asset, fileID, false); e != nil {
		ev.StorageError = e.Error()
	} else {
		ev.ChecksumStatus, ev.CurrentSHA256 = status, asset.SHA256
	}
	if ctx.IntegrationFor("media") == nil {
		return ev
	}
	ev.MediaBound = true
	var media struct {
		Found bool `json:"found"`
		Media struct {
			FileID       string `json:"file_id"`
			SourceSHA256 string `json:"source_sha256"`
			ProbeStatus  string `json:"probe_status"`
			DurationMS   int64  `json:"duration_ms"`
		} `json:"media"`
	}
	if e := ctx.PlatformAPI().CallAppResult("media", "media_get", map[string]any{"_project_id": pid, "file_id": asset.StorageFileID}, &media); e != nil {
		ev.MediaError = e.Error()
		return ev
	}
	if media.Found && media.Media.FileID == asset.StorageFileID {
		ev.MediaFound = true
		ev.MediaSHA256, ev.MediaProbe, ev.MediaMS = media.Media.SourceSHA256, media.Media.ProbeStatus, media.Media.DurationMS
	}
	return ev
}

// evaluateDelivery decides pending/verified/failed from one provider
// observation and source evidence. Pending reasons are specific so the SOP can
// report them; failed is reserved for evidence that contradicts the upload.
func evaluateDelivery(h *Hosting, obs HostObservation, ev deliverySourceEvidence, checkedAt string) DeliveryReceipt {
	r := DeliveryReceipt{DeliveryStatus: "pending", ProviderReadiness: obs.Status, Warnings: []string{}, CheckedAt: checkedAt,
		Source: DeliverySource{StorageFileID: ev.StorageFileID, HostedSHA256: h.SourceSHA256, CurrentSHA256: ev.CurrentSHA256, CurrentChecksumStatus: ev.ChecksumStatus,
			MediaSHA256: ev.MediaSHA256, HashMatch: "unavailable", ProviderHashMatch: "unavailable"},
		Destination: DeliveryDestination{Provider: h.Provider, ConnectionID: h.ConnectionID, LibraryID: h.LibraryID, CollectionID: h.CollectionID, RemoteID: h.RemoteID,
			GUIDConfirmed: obs.GUIDConfirmed, ProviderStatus: obs.ProviderStatus, ProviderStage: obs.ProviderStage, EmbedURL: obs.EmbedURL},
		Duration: DeliveryDuration{ProviderSeconds: obs.DurationSeconds, ToleranceMS: providerRoundingMS + containerToleranceMS, Status: "unavailable"}}
	r.Destination.LibraryConfirmed = obs.ReportedLibraryID != "" && obs.ReportedLibraryID == h.LibraryID
	r.Destination.CollectionConfirmed = obs.CollectionReported && strings.EqualFold(obs.CollectionID, h.CollectionID)
	if h.CollectionID != "" && !obs.CollectionReported {
		r.Warnings = append(r.Warnings, "provider did not report a collection")
	}
	pending := func(reason string) DeliveryReceipt { r.Reason = reason; return r }
	failed := func(reason string) DeliveryReceipt { r.DeliveryStatus, r.Reason = "failed", reason; return r }

	// Master hash: the hosted record must still describe Storage's current
	// verified bytes. A replaced master is a definite mismatch.
	switch {
	case ev.StorageError != "":
	case h.SourceSHA256 == "":
	case ev.ChecksumStatus == "verified" && ev.CurrentSHA256 != "":
		if ev.CurrentSHA256 == h.SourceSHA256 {
			r.Source.HashMatch = "verified"
		} else {
			r.Source.HashMatch = "mismatch"
		}
	}
	// Bunny does not document originalHash. Only an exact SHA-256 equality
	// counts; any other value is recorded but never treated as a mismatch.
	if v := strings.TrimSpace(obs.ProviderHash); v != "" {
		r.Source.ProviderHash = v
		if sha256Hex.MatchString(v) && strings.EqualFold(v, h.SourceSHA256) {
			r.Source.ProviderHashMatch = "verified"
		} else {
			r.Source.ProviderHashMatch = "unrecognized"
			r.Warnings = append(r.Warnings, "provider hash is not a matching SHA-256; recorded only")
		}
	}

	switch obs.Status {
	case "failed":
		return failed("provider_failed")
	case "ready":
	default:
		return pending("provider_processing")
	}
	if r.Source.HashMatch == "mismatch" {
		return failed("source_hash_mismatch")
	}
	if !obs.GUIDConfirmed {
		return pending("provider_identity_unconfirmed")
	}
	if !r.Destination.LibraryConfirmed {
		return pending("provider_library_unconfirmed")
	}
	switch {
	case ev.StorageError != "":
		r.Warnings = append(r.Warnings, "Storage: "+ev.StorageError)
		return pending("storage_checksum_unavailable")
	case h.SourceSHA256 == "":
		return pending("hosted_source_hash_unknown")
	case r.Source.HashMatch != "verified":
		return pending("storage_checksum_" + firstNonEmpty(ev.ChecksumStatus, "unverified"))
	}

	if !obs.DurationReported || obs.DurationSeconds <= 0 {
		return pending("provider_duration_unavailable")
	}
	switch {
	case !ev.MediaBound:
		return pending("media_not_bound")
	case ev.MediaError != "":
		r.Warnings = append(r.Warnings, "Media: "+ev.MediaError)
		return pending("source_timing_unavailable")
	case !ev.MediaFound || ev.MediaMS <= 0 || (ev.MediaProbe != "" && ev.MediaProbe != "ok" && ev.MediaProbe != "completed"):
		return pending("source_timing_unavailable")
	case ev.MediaSHA256 != h.SourceSHA256:
		// Media timing for different bytes says nothing about this upload.
		return pending("source_timing_not_hash_bound")
	}
	diff := obs.DurationSeconds*1000 - ev.MediaMS
	if diff < 0 {
		diff = -diff
	}
	r.Duration.SourceMS, r.Duration.DifferenceMS = ev.MediaMS, diff
	r.Duration.ProviderRoundingMS = min(diff, providerRoundingMS)
	r.Duration.ContainerDifferenceMS = diff - r.Duration.ProviderRoundingMS
	if diff > r.Duration.ToleranceMS {
		r.Duration.Status = "mismatch"
		return failed("duration_mismatch")
	}
	r.Duration.Status = "verified"
	if r.Duration.ContainerDifferenceMS > 0 {
		r.Warnings = append(r.Warnings, "audio/container length differs beyond provider rounding, within tolerance")
	}
	r.DeliveryStatus, r.VerifiedAt = "verified", checkedAt
	return r
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// nextDeliveryCheck schedules retries only for a ready video that is still
// pending. Retries re-read evidence; they never start another transfer.
func nextDeliveryCheck(r *DeliveryReceipt, attempts int) string {
	if r.ProviderReadiness != "ready" || r.DeliveryStatus != "pending" || r.Reason == "media_not_bound" {
		return ""
	}
	if attempts >= deliveryMaxAttempts {
		r.Warnings = append(r.Warnings, "automatic delivery checks exhausted; run content_catalog_hosting_check")
		return ""
	}
	return time.Now().UTC().Add(hostingBackoff(attempts)).Format(time.RFC3339)
}

// queueDeliveryCheck asks the background checker to verify a ready video soon,
// e.g. after an existing provider video is linked.
func queueDeliveryCheck(ctx *sdk.AppCtx, pid, id string) error {
	_, err := ctx.AppDB().Exec(`UPDATE hostings SET delivery_status='pending',delivery_reason='queued',delivery_attempts=0,next_check_at=? WHERE project_id=? AND id=? AND status='ready'`, now(), pid, id)
	return err
}
