package main

import "encoding/json"

type Envelope struct {
	ID              int64  `json:"id"`
	PublicID        string `json:"public_id"`
	ProjectID       string `json:"project_id,omitempty"`
	SourceFileID    int64  `json:"source_file_id"`
	SourceName      string `json:"source_name"`
	SourceSHA256    string `json:"source_sha256"`
	CompletedFileID int64  `json:"completed_file_id,omitempty"`
	CompletedSHA256 string `json:"completed_sha256,omitempty"`
	AuditFileID     int64  `json:"audit_file_id,omitempty"`
	Title           string `json:"title"`
	SenderName      string `json:"sender_name"`
	Message         string `json:"message"`
	Status          string `json:"status"`
	DeliveryMode    string `json:"delivery_mode"`
	ExpiresAt       string `json:"expires_at"`
	SentAt          string `json:"sent_at,omitempty"`
	CompletedAt     string `json:"completed_at,omitempty"`
	TerminalReason  string `json:"terminal_reason,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

type Recipient struct {
	ID             int64  `json:"id"`
	EnvelopeID     int64  `json:"envelope_id"`
	ProjectID      string `json:"project_id,omitempty"`
	Name           string `json:"name"`
	Email          string `json:"email,omitempty"`
	Role           string `json:"role"`
	SigningOrder   int    `json:"signing_order"`
	Status         string `json:"status"`
	TokenExpiresAt string `json:"token_expires_at,omitempty"`
	ViewedAt       string `json:"viewed_at,omitempty"`
	CompletedAt    string `json:"completed_at,omitempty"`
	DeclinedReason string `json:"declined_reason,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type Field struct {
	ID          int64   `json:"id"`
	EnvelopeID  int64   `json:"envelope_id"`
	RecipientID int64   `json:"recipient_id"`
	ProjectID   string  `json:"project_id,omitempty"`
	FieldType   string  `json:"field_type"`
	Page        int     `json:"page"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Width       float64 `json:"width"`
	Height      float64 `json:"height"`
	Label       string  `json:"label"`
	Required    bool    `json:"required"`
	CreatedAt   string  `json:"created_at"`
}

type FieldValue struct {
	FieldID     int64  `json:"field_id"`
	EnvelopeID  int64  `json:"envelope_id"`
	RecipientID int64  `json:"recipient_id"`
	ValueText   string `json:"value"`
	SignedAt    string `json:"signed_at"`
}

type AuditEvent struct {
	ID          int64           `json:"id"`
	EnvelopeID  int64           `json:"envelope_id"`
	RecipientID int64           `json:"recipient_id,omitempty"`
	EventType   string          `json:"event_type"`
	Detail      json.RawMessage `json:"detail"`
	OccurredAt  string          `json:"occurred_at"`
}

type EnvelopeDetail struct {
	Envelope
	Recipients []Recipient  `json:"recipients"`
	Fields     []Field      `json:"fields"`
	Audit      []AuditEvent `json:"audit,omitempty"`
}

type SigningSession struct {
	Envelope  Envelope  `json:"envelope"`
	Recipient Recipient `json:"recipient"`
	Fields    []Field   `json:"fields"`
}

type StorageFile struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	ContentType   string `json:"content_type"`
	SizeBytes     int64  `json:"size_bytes"`
	ContentBase64 string `json:"content_base64,omitempty"`
}

type StorageUpload struct {
	ID        int64  `json:"id"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Folder    string `json:"folder"`
	Name      string `json:"name"`
}

type completionValue struct {
	Field
	ValueText     string `json:"value"`
	RecipientName string `json:"recipient_name"`
	SignedAt      string `json:"signed_at"`
}
