package main

import "context"

type downloadJob struct {
	ID                string  `json:"id"`
	ProjectID         string  `json:"project_id,omitempty"`
	URL               string  `json:"url"`
	Status            string  `json:"status"`
	Stage             string  `json:"stage"`
	Progress          float64 `json:"progress"`
	Title             string  `json:"title,omitempty"`
	Extractor         string  `json:"extractor,omitempty"`
	Mode              string  `json:"mode"`
	Quality           string  `json:"quality"`
	FormatID          string  `json:"format_id,omitempty"`
	SourceProfileID   string  `json:"source_profile_id,omitempty"`
	StorageFolder     string  `json:"storage_folder"`
	StorageVisibility string  `json:"storage_visibility"`
	StorageFileID     int64   `json:"storage_file_id,omitempty"`
	StorageURL        string  `json:"storage_url,omitempty"`
	OutputName        string  `json:"output_name,omitempty"`
	OutputBytes       int64   `json:"output_bytes,omitempty"`
	Error             string  `json:"error,omitempty"`
	CreatedAt         string  `json:"created_at"`
	StartedAt         string  `json:"started_at,omitempty"`
	CompletedAt       string  `json:"completed_at,omitempty"`
	UpdatedAt         string  `json:"updated_at"`
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
	ProjectID         string
	URL               string
	Mode              string
	Quality           string
	FormatID          string
	SourceProfileID   string
	StorageFolder     string
	StorageVisibility string
	AudioFormat       string
	FFmpegLocation    string
	YoutubePlayer     string
	YTDLPExtraArgs    []string
	ProxyURL          string
	MaxDownloadBytes  int64
	Tags              []string
}

type runningDownload struct {
	cancel    context.CancelFunc
	projectID string
}
