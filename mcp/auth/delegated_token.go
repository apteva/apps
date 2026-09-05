package main

type aptevaDelegatedToken struct {
	AccessToken   string `json:"access_token"`
	TokenType     string `json:"token_type"`
	ExpiresIn     int    `json:"expires_in"`
	ExpiresAt     string `json:"expires_at"`
	KeyPrefix     string `json:"key_prefix"`
	ProjectID     string `json:"project_id"`
	OAuthClientID string `json:"oauth_client_id"`
}

// The current platform mint API has no Auth-session binding or revocation API.
// Do not mint independently valid credentials that survive logout or disable.
func mintAptevaDelegatedToken(projectID string, org *Organization, user *User, client *Client) (*aptevaDelegatedToken, error) {
	return nil, nil
}
