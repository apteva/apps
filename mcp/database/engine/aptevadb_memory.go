package engine

import native "github.com/apteva/db"

// On-demand diagnostics for database_describe. File-cache residency and process
// RSS overlap other categories and must not be added to private buffer totals.
type NativeMemoryInfo struct {
	ProcessResidentBytes        int64 `json:"processResidentBytes"`
	MappedResidentEstimateBytes int64 `json:"mappedResidentEstimateBytes"`
	MappedPageCacheBytes        int64 `json:"mappedPageCacheBytes"`
	ManagedBufferBytes          int64 `json:"managedBufferBytes"`
	PinnedBytes                 int64 `json:"pinnedBytes"`
	QueryWorkingBytes           int64 `json:"queryWorkingBytes"`
	MappedTargetBytes           int64 `json:"mappedTargetBytes"`
	ManagedTargetBytes          int64 `json:"managedTargetBytes"`
	ReclaimedMappedBytes        int64 `json:"reclaimedMappedBytes"`
}

func nativeMemoryInfo(s native.MemoryStats) *NativeMemoryInfo {
	return &NativeMemoryInfo{
		ProcessResidentBytes: s.ProcessResidentBytes, MappedResidentEstimateBytes: s.MappedResidentEstimateBytes,
		MappedPageCacheBytes: s.MappedPageCacheBytes, ManagedBufferBytes: s.ManagedBufferBytes,
		PinnedBytes: s.PinnedBytes, QueryWorkingBytes: s.QueryWorkingBytes, MappedTargetBytes: s.MappedTargetBytes,
		ManagedTargetBytes: s.ManagedTargetBytes, ReclaimedMappedBytes: s.ReclaimedMappedBytes,
	}
}
