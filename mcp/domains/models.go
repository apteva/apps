package main

import (
	"encoding/json"
	"time"
)

// ─── Domain types ──────────────────────────────────────────────────

type Domain struct {
	ID              int64  `json:"id"`
	ProjectID       string `json:"project_id,omitempty"`
	Name            string `json:"name"`
	RegistrarSlug   string `json:"registrar_slug,omitempty"`
	DNSProviderSlug string `json:"dns_provider_slug,omitempty"`
	// ConnectionID pins this domain to one specific DNS provider
	// connection. Zero with connection_mode=unmanaged disables DNS operations.
	ConnectionID   int64  `json:"connection_id,omitempty"`
	ConnectionMode string `json:"connection_mode"`
	ExpiresAt      string `json:"expires_at,omitempty"`
	Notes          string `json:"notes,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

// DNSRecord is the canonical shape we hand back to callers — flat,
// provider-agnostic. The proxy layer translates each provider's actual
// shape into this struct. Raw is reserved for providers whose delete
// endpoint needs type-specific fields that are not part of the common
// display/edit surface.
type DNSRecord struct {
	ID       string         `json:"id"`    // provider-side record id when available
	Name     string         `json:"name"`  // FQDN or provider-local name (e.g. "mail.acme.com" or "mail")
	Type     string         `json:"type"`  // A | AAAA | CNAME | MX | TXT | NS | SRV | CAA | ...
	Value    string         `json:"value"` // record content
	TTL      int            `json:"ttl"`
	Prio     int            `json:"prio"` // MX priority etc.
	Notes    string         `json:"notes,omitempty"`
	Raw      map[string]any `json:"raw,omitempty"`
	Disabled bool           `json:"disabled,omitempty"`
	Warnings []string       `json:"warnings,omitempty"`
}

type DomainAvailability struct {
	Domain       string          `json:"domain"`
	Available    bool            `json:"available"`
	Known        bool            `json:"known"`
	MinDuration  int             `json:"min_duration,omitempty"`
	Provider     string          `json:"provider"`
	ConnectionID int64           `json:"connection_id,omitempty"`
	Source       string          `json:"source,omitempty"`
	Confidence   string          `json:"confidence,omitempty"`
	Warning      string          `json:"warning,omitempty"`
	Price        string          `json:"price,omitempty"`
	Currency     string          `json:"currency,omitempty"`
	Premium      bool            `json:"premium,omitempty"`
	Raw          json.RawMessage `json:"raw,omitempty"`
}

type DomainRegistrationRequest struct {
	CostCents      int64
	IdempotencyKey string
	DryRun         bool
	Domain         string
	Years          int
	AutoRenew      bool
	WhoisPrivacy   bool
	Coupon         string
}

type RegistrationIntent struct {
	CostCents    int64
	Result       json.RawMessage
	AttemptedAt  string
	UpdatedAt    string
	Token        string
	ProjectID    string
	Domain       string
	Years        int
	AutoRenew    bool
	WhoisPrivacy bool
	Coupon       string
	Notes        string
	Provider     string
	ConnectionID int64
	Price        string
	Currency     string
	Status       string
	Raw          json.RawMessage
	Error        string
	ExpiresAt    time.Time
}
