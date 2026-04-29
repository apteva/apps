package main

// Curated lists used by the live checker.
//
// disposableDomains — providers whose addresses are throwaway by
// design (Mailinator, GuerrillaMail, 10MinuteMail, etc.). Match
// by full domain. Sub-domains aren't listed individually because
// most disposable providers rotate them faster than we'd refresh.
//
// freeDomains — major consumer mail providers. Useful as a signal
// (an enterprise signup from gmail might be lower trust) but not
// disqualifying.
//
// roleLocalParts — generic mailboxes that aren't a single human
// (info@, support@). Useful when the agent needs to know whether
// it's emailing a person or a queue.
//
// Lists are intentionally short + curated rather than the
// 3000-entry public lists — those bring more noise (defunct
// providers, false positives) than signal at this scope. Refresh
// in the next release if a new provider becomes commonly abused.

var disposableDomains = map[string]bool{
	// Big-name throwaway providers — these account for the bulk of
	// disposable signups in practice.
	"mailinator.com":      true,
	"guerrillamail.com":   true,
	"guerrillamail.net":   true,
	"guerrillamail.org":   true,
	"guerrillamail.biz":   true,
	"guerrillamailblock.com": true,
	"sharklasers.com":     true,
	"grr.la":              true,
	"10minutemail.com":    true,
	"10minutemail.net":    true,
	"yopmail.com":         true,
	"yopmail.net":         true,
	"yopmail.fr":          true,
	"tempmail.com":        true,
	"temp-mail.org":       true,
	"temp-mail.io":        true,
	"throwawaymail.com":   true,
	"trashmail.com":       true,
	"trashmail.net":       true,
	"mailnesia.com":       true,
	"maildrop.cc":         true,
	"dispostable.com":     true,
	"fakeinbox.com":       true,
	"getnada.com":         true,
	"emailondeck.com":     true,
	"mohmal.com":          true,
	"mintemail.com":       true,
	"sneakemail.com":      true,
	"spam4.me":            true,
	"spamgourmet.com":     true,
	"jetable.org":         true,
	"meltmail.com":        true,
	"mytemp.email":        true,
	"tempr.email":         true,
	"discard.email":       true,
	"discardmail.com":     true,
	"mailcatch.com":       true,
	"my10minutemail.com":  true,
	"trashinbox.com":      true,
	"tmail.ws":            true,
	"33mail.com":          true,
}

var freeDomains = map[string]bool{
	// Western consumer providers
	"gmail.com":       true,
	"googlemail.com":  true,
	"outlook.com":     true,
	"hotmail.com":     true,
	"hotmail.co.uk":   true,
	"hotmail.fr":      true,
	"live.com":        true,
	"msn.com":         true,
	"yahoo.com":       true,
	"yahoo.co.uk":     true,
	"yahoo.fr":        true,
	"yahoo.de":        true,
	"yahoo.es":        true,
	"yahoo.it":        true,
	"ymail.com":       true,
	"icloud.com":      true,
	"me.com":          true,
	"mac.com":         true,
	"aol.com":         true,
	"aim.com":         true,
	"protonmail.com":  true,
	"proton.me":       true,
	"pm.me":           true,
	"tutanota.com":    true,
	"tuta.io":         true,
	"fastmail.com":    true,
	"fastmail.fm":     true,
	"zoho.com":        true,
	"gmx.com":         true,
	"gmx.de":          true,
	"gmx.net":         true,
	"web.de":          true,
	"mail.com":        true,
	// Major non-Western
	"qq.com":     true,
	"163.com":    true,
	"126.com":    true,
	"sina.com":   true,
	"yandex.com": true,
	"yandex.ru":  true,
	"mail.ru":    true,
	"naver.com":  true,
	"daum.net":   true,
}

var roleLocalParts = map[string]bool{
	"info":          true,
	"contact":       true,
	"hello":         true,
	"hi":            true,
	"team":          true,
	"office":        true,
	"admin":         true,
	"administrator": true,
	"root":          true,
	"webmaster":     true,
	"hostmaster":    true,
	"postmaster":    true,
	"noreply":       true,
	"no-reply":      true,
	"donotreply":    true,
	"do-not-reply":  true,
	"support":       true,
	"help":          true,
	"helpdesk":      true,
	"service":       true,
	"customerservice": true,
	"sales":         true,
	"billing":       true,
	"finance":       true,
	"accounts":      true,
	"accounting":    true,
	"hr":            true,
	"jobs":          true,
	"careers":       true,
	"recruiting":    true,
	"marketing":     true,
	"press":         true,
	"media":         true,
	"pr":            true,
	"legal":         true,
	"privacy":       true,
	"abuse":         true,
	"security":      true,
	"compliance":    true,
}
