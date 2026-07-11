package main

import (
	"fmt"
	"os"
	"strconv"
)

type importLimits struct {
	CompressedBytes int64
	FileBytes       int64
	TotalBytes      int64
	Files           int
}

func currentImportLimits() importLimits {
	return importLimits{
		CompressedBytes: envLimit("CODE_IMPORT_MAX_COMPRESSED_BYTES", 128<<20),
		FileBytes:       envLimit("CODE_IMPORT_MAX_FILE_BYTES", 64<<20),
		TotalBytes:      envLimit("CODE_IMPORT_MAX_TOTAL_BYTES", 256<<20),
		Files:           int(envLimit("CODE_IMPORT_MAX_FILES", 20_000)),
	}
}

func envLimit(name string, fallback int64) int64 {
	if raw := os.Getenv(name); raw != "" {
		if value, err := strconv.ParseInt(raw, 10, 64); err == nil && value > 0 {
			return value
		}
	}
	return fallback
}

func checkImportEntry(limits importLimits, name string, size, total int64, count int) error {
	if count > limits.Files {
		return fmt.Errorf("archive contains too many files (max %d)", limits.Files)
	}
	if size < 0 || size > limits.FileBytes {
		return fmt.Errorf("archive entry %q is too large (max %d bytes)", name, limits.FileBytes)
	}
	if total > limits.TotalBytes-size {
		return fmt.Errorf("archive expands beyond %d bytes", limits.TotalBytes)
	}
	return nil
}
