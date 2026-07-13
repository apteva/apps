package main

// Inbound sender classification.
//
// Automated / no-reply senders (noreply@..., mailer-daemon, list mail,
// transactional notifications) are not human CRM leads. The inbound
// handler uses this classifier before contact upsert so bounces and
// automated provider mail do not pollute the CRM.
//
// Detection prefers RFC-standard headers (Auto-Submitted, Precedence,
// List-*) — the same signals Zendesk uses — and falls back to address
// heuristics. Headers arrive in the inbound payload's forwarded RFC-822
// map (messaging's parseRawEml captures every header).

import "strings"

// tagAutomated is applied to contacts whose inbound was classified as
// machine/no-reply. Segment with tag_not_in:["automated"] to exclude.
const tagAutomated = "automated"

// automatedLocalParts are no-reply / system local-parts. Matched as a
// whole local-part or as a separated prefix (noreply, no-reply.bounce…)
// to avoid false hits like "automation-team" → wait, that one IS auto;
// the separators keep "info"/"autumn" etc. from matching.
var automatedLocalParts = []string{
	"noreply", "no-reply", "no_reply", "no.reply",
	"donotreply", "do-not-reply", "do_not_reply",
	"mailer-daemon", "mailerdaemon", "mailer",
	"postmaster", "bounce", "bounces",
	"notification", "notifications", "automated",
}

// automatedSenderDomains are provider domains that commonly send delivery
// failures, complaints, and other machine mail from unpredictable local-parts.
// Suffix matching handles subdomains such as email.amazonses.com.
var automatedSenderDomains = []string{
	"amazonses.com",
}

// isAutomatedSender classifies an inbound sender as machine/no-reply.
// Returns (automated, reason). Reason is for logging/audit only.
func isAutomatedSender(channel, from string, headers map[string]any) (bool, string) {
	// Header signals (email only) — the strongest, RFC-standard ones.
	if channel == channelEmail {
		if v := headerVal(headers, "Auto-Submitted"); v != "" && !strings.EqualFold(strings.TrimSpace(v), "no") {
			return true, "Auto-Submitted: " + v
		}
		if v := headerVal(headers, "Precedence"); v != "" {
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "bulk", "list", "junk":
				return true, "Precedence: " + v
			}
		}
		if headerPresent(headers, "List-Unsubscribe") || headerPresent(headers, "List-Id") {
			return true, "List-* header"
		}
		if headerPresent(headers, "X-Auto-Response-Suppress") {
			return true, "X-Auto-Response-Suppress"
		}
		if strings.TrimSpace(headerVal(headers, "Return-Path")) == "<>" {
			return true, "null Return-Path"
		}
	}

	// Address heuristic — local-part match. Works on the canonicalised
	// bare address (canonicalAddress already stripped any display name).
	if local := strings.ToLower(localPartOf(from)); local != "" {
		for _, pat := range automatedLocalParts {
			if local == pat ||
				strings.HasPrefix(local, pat+".") ||
				strings.HasPrefix(local, pat+"-") ||
				strings.HasPrefix(local, pat+"_") ||
				strings.HasPrefix(local, pat+"+") {
				return true, "no-reply address: " + local
			}
		}
	}
	if domain := domainOf(from); domain != "" {
		for _, pat := range automatedSenderDomains {
			if domain == pat || strings.HasSuffix(domain, "."+pat) {
				return true, "automated sender domain: " + domain
			}
		}
	}
	return false, ""
}

func localPartOf(addr string) string {
	addr = strings.TrimSpace(addr)
	if at := strings.IndexByte(addr, '@'); at > 0 {
		return addr[:at]
	}
	return ""
}

// headerVal does a case-insensitive lookup against the forwarded header
// map (Go canonicalises keys, but the JSON round-trip + other producers
// make a defensive case-insensitive scan worthwhile).
func headerVal(headers map[string]any, key string) string {
	for k, v := range headers {
		if strings.EqualFold(k, key) {
			return headerToString(v)
		}
	}
	return ""
}

func headerPresent(headers map[string]any, key string) bool {
	for k := range headers {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

func headerToString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		if len(x) > 0 {
			if s, ok := x[0].(string); ok {
				return s
			}
		}
	}
	return ""
}
