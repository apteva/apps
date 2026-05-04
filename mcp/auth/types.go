package main

// Domain types — JSON shapes returned by HTTP and MCP. Kept thin: each
// row in the schema gets one struct, with omitempty everywhere that
// nullability is meaningful so JSON output doesn't leak null padding.

type User struct {
	ID              int64  `json:"id"`
	ProjectID       string `json:"project_id,omitempty"`
	Email           string `json:"email"`
	EmailVerifiedAt string `json:"email_verified_at,omitempty"`
	DisplayName     string `json:"display_name,omitempty"`
	AvatarURL       string `json:"avatar_url,omitempty"`
	Status          string `json:"status"`
	HasPassword     bool   `json:"has_password"`
	MFAEnabled      bool   `json:"mfa_enabled"`
	LastLoginAt     string `json:"last_login_at,omitempty"`
	LockedUntil     string `json:"locked_until,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

type Client struct {
	ID                       int64    `json:"id"`
	ClientID                 string   `json:"client_id"`
	Name                     string   `json:"name"`
	Type                     string   `json:"type"`
	RedirectURIs             []string `json:"redirect_uris"`
	AllowedOrigins           []string `json:"allowed_origins"`
	AllowedGrantTypes        []string `json:"allowed_grant_types"`
	TokenEndpointAuthMethod  string   `json:"token_endpoint_auth_method"`
	RequirePKCE              bool     `json:"require_pkce"`
	RequireMFA               bool     `json:"require_mfa"`
	JWTAudience              string   `json:"jwt_audience,omitempty"`
	AccessTokenTTLSeconds    int      `json:"access_token_ttl_seconds,omitempty"`
	RefreshTokenTTLSeconds   int      `json:"refresh_token_ttl_seconds,omitempty"`
	RefreshRotation          bool     `json:"refresh_rotation"`
	DisabledAt               string   `json:"disabled_at,omitempty"`
	CreatedAt                string   `json:"created_at,omitempty"`
}

type Session struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"user_id"`
	ClientID     string `json:"client_id"`
	UserAgent    string `json:"user_agent,omitempty"`
	IP           string `json:"ip,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	LastSeenAt   string `json:"last_seen_at,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	RevokedAt    string `json:"revoked_at,omitempty"`
}

type AuditEvent struct {
	ID         int64  `json:"id"`
	UserID     *int64 `json:"user_id,omitempty"`
	ClientID   string `json:"client_id,omitempty"`
	Event      string `json:"event"`
	IP         string `json:"ip,omitempty"`
	UserAgent  string `json:"user_agent,omitempty"`
	Metadata   string `json:"metadata,omitempty"` // raw JSON, returned as-is to the dashboard
	OccurredAt string `json:"occurred_at"`
}

// MFAFactor — public shape (secret never leaves the DB).
type MFAFactor struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	Label       string `json:"label,omitempty"`
	ConfirmedAt string `json:"confirmed_at,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}
