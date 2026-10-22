package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/pion/turn/v5"
)

type credentialRequest struct {
	ProjectID  string `json:"project_id"`
	SessionID  string `json:"session_id"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

type iceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
}

type credentials struct {
	ICEServers []iceServer `json:"ice_servers"`
	ExpiresAt  int64       `json:"expires_at"`
	TTLSeconds int         `json:"ttl_seconds"`
}

func shortHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

func restPassword(secret, username string) string {
	// The TURN REST credential convention specifies HMAC-SHA1, not a password hash.
	mac := hmac.New(sha1.New, []byte(secret))
	_, _ = mac.Write([]byte(username))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func (a *App) issueCredentials(req credentialRequest, now time.Time) (credentials, error) {
	var out credentials
	if req.ProjectID == "" || len(req.ProjectID) > 256 || req.SessionID == "" || len(req.SessionID) > 256 {
		return out, errors.New("project_id and session_id are required (at most 256 characters)")
	}
	if a.project != "" && req.ProjectID != a.project {
		return out, errors.New("project does not match this installation")
	}
	ttl := req.TTLSeconds
	if ttl == 0 {
		ttl = int(a.cfg.ttl / time.Second)
	}
	if ttl < 60 || ttl > int(a.cfg.ttl/time.Second) {
		return out, errors.New("ttl_seconds must be between 60 and the configured credential lifetime")
	}
	a.mu.Lock()
	admit := a.admittingLocked(now)
	limited := admit && !a.issuance.AllowN(now, 1)
	a.mu.Unlock()
	if !admit {
		return out, errNotAdmitting
	}
	if limited {
		return out, errRateLimited
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return out, errors.New("credential generation unavailable")
	}
	expires := now.Unix() + int64(ttl)
	username := strconv.FormatInt(expires, 10) + ":" + shortHash(req.ProjectID) + ":" + shortHash(req.SessionID) + ":" + base64.RawURLEncoding.EncodeToString(nonce[:])
	endpoint := "turn:" + a.cfg.publicHost + ":" + strconv.Itoa(turnPort)
	urls := []string{endpoint + "?transport=udp", endpoint + "?transport=tcp"}
	if a.currentTLSConfig().tlsValid(now) {
		urls = append(urls, "turns:"+a.cfg.publicHost+":"+strconv.Itoa(tlsPort)+"?transport=tcp")
	}
	out = credentials{ICEServers: []iceServer{{URLs: urls, Username: username, Credential: restPassword(a.cfg.secret, username)}}, ExpiresAt: expires, TTLSeconds: ttl}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.admittingLocked(time.Now()) {
		return credentials{}, errNotAdmitting
	}
	a.stats.issued.Add(1)
	return out, nil
}

func (a *App) authenticate(ra *turn.RequestAttributes) (string, []byte, bool) {
	if ra.Realm != a.cfg.realm || len(ra.Username) > 128 {
		return "", nil, false
	}
	parts := strings.Split(ra.Username, ":")
	if len(parts) != 4 {
		return "", nil, false
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	now := time.Now()
	if err != nil || expires <= now.Unix() || expires > now.Add(a.cfg.ttl).Unix()+1 {
		return "", nil, false
	}
	for _, part := range parts[1:] {
		decoded, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil || len(decoded) != 12 {
			return "", nil, false
		}
	}
	if a.project != "" && parts[1] != shortHash(a.project) {
		return "", nil, false
	}
	a.mu.Lock()
	allowed := a.trafficAllowedLocked(now)
	a.mu.Unlock()
	if !allowed {
		return "", nil, false
	}
	return ra.Username, turn.GenerateAuthKey(ra.Username, a.cfg.realm, restPassword(a.cfg.secret, ra.Username)), true
}
