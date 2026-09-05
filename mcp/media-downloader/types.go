package main

import "context"

type downloadJob struct {
	ID                string             `json:"id"`
	ProjectID         string             `json:"project_id,omitempty"`
	URL               string             `json:"url"`
	Status            string             `json:"status"`
	Stage             string             `json:"stage"`
	Progress          float64            `json:"progress"`
	Title             string             `json:"title,omitempty"`
	Extractor         string             `json:"extractor,omitempty"`
	Mode              string             `json:"mode"`
	Ingest            bool               `json:"ingest,omitempty"`
	Quality           string             `json:"quality"`
	FormatID          string             `json:"format_id,omitempty"`
	SourceProfileID   string             `json:"source_profile_id,omitempty"`
	StorageFolder     string             `json:"storage_folder"`
	StorageVisibility string             `json:"storage_visibility"`
	StorageFileID     int64              `json:"storage_file_id,omitempty"`
	StorageFileIDs    []int64            `json:"storage_file_ids,omitempty"`
	StorageURL        string             `json:"storage_url,omitempty"`
	OutputName        string             `json:"output_name,omitempty"`
	OutputBytes       int64              `json:"output_bytes,omitempty"`
	Metadata          *sourceMetadata    `json:"metadata,omitempty"`
	Artifacts         []downloadArtifact `json:"artifacts,omitempty"`
	Warnings          []string           `json:"warnings,omitempty"`
	Error             string             `json:"error,omitempty"`
	CreatedAt         string             `json:"created_at"`
	StartedAt         string             `json:"started_at,omitempty"`
	CompletedAt       string             `json:"completed_at,omitempty"`
	UpdatedAt         string             `json:"updated_at"`
}

// sourceMetadata is intentionally normalized. Raw yt-dlp metadata contains
// short-lived signed media URLs and extractor internals that should not be
// persisted or copied into Storage.
type sourceMetadata struct {
	ID              string         `json:"id,omitempty"`
	Title           string         `json:"title,omitempty"`
	Description     string         `json:"description,omitempty"`
	Channel         string         `json:"channel,omitempty"`
	ChannelID       string         `json:"channel_id,omitempty"`
	Uploader        string         `json:"uploader,omitempty"`
	UploaderID      string         `json:"uploader_id,omitempty"`
	WebpageURL      string         `json:"webpage_url,omitempty"`
	ThumbnailURL    string         `json:"thumbnail,omitempty"`
	PublishDate     string         `json:"publish_date,omitempty"`
	UploadDate      string         `json:"upload_date,omitempty"`
	DurationSeconds float64        `json:"duration,omitempty"`
	AgeLimit        int            `json:"age_limit,omitempty"`
	LiveStatus      string         `json:"live_status,omitempty"`
	Extractor       string         `json:"extractor,omitempty"`
	Tags            []string       `json:"tags,omitempty"`
	Categories      []string       `json:"categories,omitempty"`
	CaptionTracks   []captionTrack `json:"caption_tracks,omitempty"`
	FormatCount     int            `json:"format_count,omitempty"`
}

type captionTrack struct {
	Language string `json:"language"`
	Name     string `json:"name,omitempty"`
	Source   string `json:"source"` // manual | automatic
}

type downloadArtifact struct {
	Kind          string `json:"kind"`
	StorageFileID int64  `json:"storage_file_id"`
	StorageURL    string `json:"storage_url,omitempty"`
	Name          string `json:"name"`
	ContentType   string `json:"content_type,omitempty"`
	Bytes         int64  `json:"bytes,omitempty"`
	Language      string `json:"language,omitempty"`
	CaptionSource string `json:"caption_source,omitempty"`
}

type sourceProfile struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id,omitempty"`
	Name            string `json:"name"`
	Provider        string `json:"provider"`
	AuthType        string `json:"auth_type"`
	Status          string `json:"status"`
	LastValidatedAt string `json:"last_validated_at,omitempty"`
	LastError       string `json:"last_error,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

type mediaSearchResult struct {
	ID              string  `json:"id"`
	Title           string  `json:"title"`
	URL             string  `json:"url"`
	Channel         string  `json:"channel,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	Thumbnail       string  `json:"thumbnail,omitempty"`
	AgeLimit        int     `json:"age_limit,omitempty"`
	LiveStatus      string  `json:"live_status,omitempty"`
	UploadDate      string  `json:"upload_date,omitempty"`
}

type storedProfile struct {
	sourceProfile
	EncryptedPayload string
}

type profilePayload struct {
	CookiesNetscape string `json:"cookies_netscape,omitempty"`
}

type downloadRequest struct {
	ProjectID          string
	URL                string
	Mode               string
	Quality            string
	FormatID           string
	SourceProfileID    string
	StorageFolder      string
	StorageVisibility  string
	AudioFormat        string
	FFmpegLocation     string
	YoutubePlayer      string
	YTDLPExtraArgs     []string
	ProxyURL           string
	MaxDownloadBytes   int64
	Tags               []string
	Ingest             bool
	CaptionLanguages   []string
	CaptionTracks      []captionTrack
	FallbackTranscribe bool
}

type runningDownload struct {
	cancel    context.CancelFunc
	projectID string
}
