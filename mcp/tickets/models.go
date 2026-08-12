package main

import "encoding/json"

type Actor struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref,omitempty"`
	Name string `json:"name,omitempty"`
}

type Area struct {
	ID        int64  `json:"id"`
	ProjectID string `json:"project_id,omitempty"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	SortOrder int    `json:"sort_order"`
	Archived  bool   `json:"archived"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type Ticket struct {
	ID                    int64  `json:"id"`
	Key                   string `json:"key"`
	ProjectID             string `json:"project_id,omitempty"`
	AreaID                *int64 `json:"area_id,omitempty"`
	AreaSlug              string `json:"area_slug,omitempty"`
	AreaName              string `json:"area_name,omitempty"`
	AreaColor             string `json:"area_color,omitempty"`
	Title                 string `json:"title"`
	Description           string `json:"description"`
	Type                  string `json:"type"`
	Status                string `json:"status"`
	Priority              string `json:"priority"`
	Source                string `json:"source"`
	RequesterName         string `json:"requester_name,omitempty"`
	RequesterEmail        string `json:"requester_email,omitempty"`
	RequesterOrganization string `json:"requester_organization,omitempty"`
	RequesterCRMContactID *int64 `json:"requester_crm_contact_id,omitempty"`
	AssigneeKind          string `json:"assignee_kind,omitempty"`
	AssigneeRef           string `json:"assignee_ref,omitempty"`
	AssigneeName          string `json:"assignee_name,omitempty"`
	DueAt                 string `json:"due_at,omitempty"`
	PortalToken           string `json:"-"`
	PortalURL             string `json:"portal_url,omitempty"`
	CreatedByKind         string `json:"created_by_kind"`
	CreatedByRef          string `json:"created_by_ref,omitempty"`
	CreatedByName         string `json:"created_by_name,omitempty"`
	ResolvedAt            string `json:"resolved_at,omitempty"`
	ClosedAt              string `json:"closed_at,omitempty"`
	CreatedAt             string `json:"created_at"`
	UpdatedAt             string `json:"updated_at"`
	PublicCommentCount    int    `json:"public_comment_count,omitempty"`
	InternalNoteCount     int    `json:"internal_note_count,omitempty"`
	AttachmentCount       int    `json:"attachment_count,omitempty"`
}

type Comment struct {
	ID         int64  `json:"id"`
	TicketID   int64  `json:"ticket_id"`
	Visibility string `json:"visibility"`
	Body       string `json:"body"`
	AuthorKind string `json:"author_kind"`
	AuthorRef  string `json:"author_ref,omitempty"`
	AuthorName string `json:"author_name,omitempty"`
	EditedAt   string `json:"edited_at,omitempty"`
	CreatedAt  string `json:"created_at"`
}

type Event struct {
	ID         int64           `json:"id"`
	TicketID   int64           `json:"ticket_id"`
	EventType  string          `json:"event_type"`
	Visibility string          `json:"visibility"`
	ActorKind  string          `json:"actor_kind"`
	ActorRef   string          `json:"actor_ref,omitempty"`
	ActorName  string          `json:"actor_name,omitempty"`
	Data       json.RawMessage `json:"data"`
	CreatedAt  string          `json:"created_at"`
}

type Attachment struct {
	ID             int64  `json:"id"`
	TicketID       int64  `json:"ticket_id"`
	CommentID      *int64 `json:"comment_id,omitempty"`
	StorageFileID  string `json:"storage_file_id,omitempty"`
	Name           string `json:"name"`
	ContentType    string `json:"content_type"`
	SizeBytes      int64  `json:"size_bytes"`
	URL            string `json:"url,omitempty"`
	Visibility     string `json:"visibility"`
	UploadedByKind string `json:"uploaded_by_kind"`
	UploadedByRef  string `json:"uploaded_by_ref,omitempty"`
	UploadedByName string `json:"uploaded_by_name,omitempty"`
	CreatedAt      string `json:"created_at"`
}

type Link struct {
	ID            int64           `json:"id"`
	TicketID      int64           `json:"ticket_id"`
	Kind          string          `json:"kind"`
	Label         string          `json:"label,omitempty"`
	AppName       string          `json:"app_name,omitempty"`
	ExternalID    string          `json:"external_id,omitempty"`
	URL           string          `json:"url,omitempty"`
	Metadata      json.RawMessage `json:"metadata"`
	CreatedByKind string          `json:"created_by_kind"`
	CreatedByRef  string          `json:"created_by_ref,omitempty"`
	CreatedByName string          `json:"created_by_name,omitempty"`
	CreatedAt     string          `json:"created_at"`
}

type TicketDetail struct {
	Ticket      *Ticket       `json:"ticket"`
	Comments    []*Comment    `json:"comments"`
	Events      []*Event      `json:"events"`
	Attachments []*Attachment `json:"attachments"`
	Links       []*Link       `json:"links"`
}

type Portal struct {
	ProjectID string `json:"project_id,omitempty"`
	Token     string `json:"-"`
	Title     string `json:"title"`
	Welcome   string `json:"welcome_text"`
	Enabled   bool   `json:"enabled"`
	IntakeURL string `json:"intake_url,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type TicketFilter struct {
	Q              string
	Status         string
	Area           string
	Type           string
	Priority       string
	RequesterEmail string
	Limit          int
	Offset         int
}
