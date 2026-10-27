package main

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
)

type publicationURLPolicy struct {
	base  string
	hosts []string
}

var publicationURLPolicies = map[string]publicationURLPolicy{
	"facebook":  {"https://www.facebook.com/", []string{"facebook.com", "www.facebook.com", "m.facebook.com", "web.facebook.com", "fb.watch"}},
	"instagram": {"https://www.instagram.com/", []string{"instagram.com", "www.instagram.com"}},
	"youtube":   {"https://www.youtube.com/", []string{"youtube.com", "www.youtube.com", "m.youtube.com", "youtu.be"}},
	"tiktok":    {"https://www.tiktok.com/", []string{"tiktok.com", "www.tiktok.com", "m.tiktok.com", "vm.tiktok.com", "vt.tiktok.com"}},
	"twitter":   {"https://x.com/", []string{"x.com", "www.x.com", "twitter.com", "www.twitter.com"}},
	"pinterest": {"https://www.pinterest.com/", []string{"pinterest.com", "www.pinterest.com", "pin.it"}},
	"linkedin":  {"https://www.linkedin.com/", []string{"linkedin.com", "www.linkedin.com"}},
	"reddit":    {"https://www.reddit.com/", []string{"reddit.com", "www.reddit.com", "old.reddit.com", "redd.it"}},
	"threads":   {"https://www.threads.com/", []string{"threads.com", "www.threads.com", "threads.net", "www.threads.net"}},
}

// This handles publication links only. Asset URLs and audience setup links use
// their own contracts. Never derive a missing link from a native ID.
func normalizePublicationURL(platform, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	policy, ok := publicationURLPolicies[platform]
	if !ok {
		return "", errors.New("publication URL policy unavailable for this platform")
	}
	if strings.TrimSpace(raw) != raw || strings.Contains(raw, "\\") || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return "", errors.New("invalid publication URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil {
		return "", errors.New("malformed publication URL")
	}
	for key := range u.Query() {
		switch strings.ToLower(key) {
		case "access_token", "refresh_token", "upload_token", "client_secret", "api_key", "authorization":
			return "", errors.New("credential-bearing publication URL rejected")
		}
	}
	absolute := u.IsAbs()
	if !absolute && u.Host == "" && u.Path == "" && u.RawQuery == "" {
		return "", errors.New("publication URL has no path or query")
	}
	if !absolute {
		base, _ := url.Parse(policy.base)
		u = base.ResolveReference(u)
	}
	if u.Scheme != "https" && u.Scheme != "http" || u.Host == "" || u.User != nil {
		return "", errors.New("unsafe publication URL scheme")
	}
	if p := u.Port(); p != "" && !(u.Scheme == "https" && p == "443") && !(u.Scheme == "http" && p == "80") {
		return "", errors.New("unexpected publication URL port")
	}
	allowed := false
	for _, host := range policy.hosts {
		if strings.EqualFold(u.Hostname(), host) {
			allowed = true
			break
		}
	}
	if !allowed || strings.HasSuffix(u.Hostname(), ".") {
		return "", errors.New("unexpected publication URL host")
	}
	if absolute {
		return raw, nil
	}
	return u.String(), nil
}
func safePublicationURL(platform, raw string) string {
	value, _ := normalizePublicationURL(platform, raw)
	return value
}
