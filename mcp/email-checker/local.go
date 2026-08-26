package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"sort"
	"strings"
	"time"
)

const dnsTimeout = 3 * time.Second

type CheckResult struct {
	Email          string          `json:"email"`
	Valid          bool            `json:"valid"`
	Reasons        []string        `json:"reasons"`
	SyntaxOK       bool            `json:"syntax_ok"`
	Domain         string          `json:"domain,omitempty"`
	DomainStatus   string          `json:"domain_status,omitempty"`
	ImplicitMX     bool            `json:"implicit_mx"`
	MX             []string        `json:"mx,omitempty"`
	SuggestedEmail string          `json:"suggested_email,omitempty"`
	Disposable     bool            `json:"disposable"`
	Role           bool            `json:"role"`
	Free           bool            `json:"free"`
	SMTP           SMTPProbe       `json:"smtp"`
	Verdict        string          `json:"verdict"`
	Confidence     string          `json:"confidence"`
	Recommendation string          `json:"recommendation"`
	Provider       *ProviderResult `json:"provider,omitempty"`
}

type SMTPProbe struct {
	Checked     bool          `json:"checked"`
	Email       string        `json:"email,omitempty"`
	MX          string        `json:"mx,omitempty"`
	RcptStatus  string        `json:"rcpt_status,omitempty"` // ok | reject | catch_all | tempfail | timeout | connect_failed | unavailable | unknown
	Code        int           `json:"code,omitempty"`
	Response    string        `json:"response,omitempty"`
	Informative *bool         `json:"informative,omitempty"`
	CatchAll    *bool         `json:"catch_all,omitempty"`
	Retryable   bool          `json:"retryable"`
	Note        string        `json:"note,omitempty"`
	Attempts    []SMTPAttempt `json:"attempts,omitempty"`
}

type SMTPAttempt struct {
	MX       string `json:"mx"`
	Kind     string `json:"kind"` // recipient | catch_all
	Status   string `json:"status"`
	Code     int    `json:"code,omitempty"`
	Response string `json:"response,omitempty"`
}

type mailResolver interface {
	LookupMX(context.Context, string) ([]*net.MX, error)
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type smtpHostProbe func(context.Context, string, string, time.Duration) SMTPAttempt

type smtpDialFunc func(context.Context, string, time.Duration) (net.Conn, error)

type localVerifier struct {
	resolver    mailResolver
	probeHost   smtpHostProbe
	randomLocal func() string
}

var defaultLocalVerifier = localVerifier{
	resolver:    net.DefaultResolver,
	probeHost:   probeSMTPHost,
	randomLocal: randomProbeLocal,
}

func check(input string, withSMTP bool, smtpTimeout time.Duration) CheckResult {
	return defaultLocalVerifier.check(context.Background(), input, withSMTP, smtpTimeout)
}

func (v localVerifier) check(parent context.Context, input string, withSMTP bool, smtpTimeout time.Duration) (res CheckResult) {
	res = CheckResult{
		Email:   strings.TrimSpace(input),
		Reasons: []string{},
		SMTP:    SMTPProbe{Checked: false},
	}
	defer func() { applyLocalDecision(&res) }()

	addr, err := mail.ParseAddress(res.Email)
	if err != nil || addr.Address == "" {
		res.Reasons = append(res.Reasons, "bad_syntax")
		return res
	}
	at := strings.LastIndex(addr.Address, "@")
	if at <= 0 || at == len(addr.Address)-1 {
		res.Reasons = append(res.Reasons, "bad_syntax")
		return res
	}

	local := addr.Address[:at]
	domain := strings.ToLower(strings.TrimSuffix(addr.Address[at+1:], "."))
	if domain == "" {
		res.Reasons = append(res.Reasons, "bad_syntax")
		return res
	}
	res.SyntaxOK = true
	res.Domain = domain
	res.Email = local + "@" + domain

	classificationLocal := strings.ToLower(local)
	if plus := strings.IndexByte(classificationLocal, '+'); plus >= 0 {
		classificationLocal = classificationLocal[:plus]
	}
	res.Disposable = domainListed(domain, disposableDomains)
	res.Free = domainListed(domain, freeDomains)
	res.Role = roleLocalParts[classificationLocal]
	if res.Disposable {
		res.Reasons = append(res.Reasons, "disposable_domain")
	}
	if res.Role {
		res.Reasons = append(res.Reasons, "role_account")
	}
	if corrected := commonDomainTypos[domain]; corrected != "" {
		res.SuggestedEmail = local + "@" + corrected
		res.Reasons = append(res.Reasons, "possible_typo")
	}

	dnsCtx, cancel := context.WithTimeout(parent, dnsTimeout)
	routes, status := v.resolveMailRoutes(dnsCtx, domain)
	cancel()
	res.DomainStatus = status
	res.ImplicitMX = status == "implicit_mx"
	res.MX = routes
	switch status {
	case "null_mx":
		res.Reasons = append(res.Reasons, "domain_does_not_accept_mail")
	case "no_mail_server":
		res.Reasons = append(res.Reasons, "no_mail_server")
	case "dns_error":
		res.Reasons = append(res.Reasons, "dns_temporary_error")
	}

	res.Valid = res.SyntaxOK && len(routes) > 0 && !res.Disposable
	if withSMTP && len(routes) > 0 {
		res.SMTP = v.checkSMTP(parent, res.Email, routes, smtpTimeout)
	}
	return res
}

func (v localVerifier) resolveMailRoutes(ctx context.Context, domain string) ([]string, string) {
	mxs, err := v.resolver.LookupMX(ctx, domain)
	if err != nil && !dnsNotFound(err) {
		return nil, "dns_error"
	}
	if err == nil && len(mxs) == 1 && strings.TrimSpace(strings.TrimSuffix(mxs[0].Host, ".")) == "" {
		return nil, "null_mx"
	}

	type route struct {
		host string
		pref uint16
	}
	ordered := make([]route, 0, len(mxs))
	seen := make(map[string]bool, len(mxs))
	for _, mx := range mxs {
		if mx == nil {
			continue
		}
		host := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(mx.Host, ".")))
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		ordered = append(ordered, route{host: host, pref: mx.Pref})
	}
	if len(ordered) > 0 {
		sort.SliceStable(ordered, func(i, j int) bool {
			if ordered[i].pref == ordered[j].pref {
				return ordered[i].host < ordered[j].host
			}
			return ordered[i].pref < ordered[j].pref
		})
		routes := make([]string, 0, len(ordered))
		for _, item := range ordered {
			routes = append(routes, item.host)
		}
		return routes, "mx"
	}

	// RFC 5321 section 5.1: an address domain with no MX records uses the
	// domain's A/AAAA address as an implicit MX with preference 0.
	addresses, ipErr := v.resolver.LookupIPAddr(ctx, domain)
	if ipErr == nil && len(addresses) > 0 {
		return []string{domain}, "implicit_mx"
	}
	if ipErr != nil && !dnsNotFound(ipErr) {
		return nil, "dns_error"
	}
	return nil, "no_mail_server"
}

func (v localVerifier) checkSMTP(parent context.Context, email string, routes []string, timeout time.Duration) SMTPProbe {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	result := SMTPProbe{Checked: true, Email: email, Attempts: []SMTPAttempt{}}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	rejects := 0
	retryable := false
	for _, mxHost := range routes {
		attempt := v.probeHost(ctx, mxHost, email, timeout)
		attempt.MX = mxHost
		attempt.Kind = "recipient"
		result.Attempts = append(result.Attempts, attempt)
		result.MX, result.Code, result.Response = mxHost, attempt.Code, attempt.Response

		switch attempt.Status {
		case "ok":
			probeLocal := v.randomLocal()
			catchAllAddress := probeLocal + "@" + email[strings.LastIndex(email, "@")+1:]
			catchAttempt := v.probeHost(ctx, mxHost, catchAllAddress, timeout)
			catchAttempt.MX = mxHost
			catchAttempt.Kind = "catch_all"
			result.Attempts = append(result.Attempts, catchAttempt)
			result.RcptStatus = "ok"
			result.Code = attempt.Code
			result.Response = attempt.Response
			switch catchAttempt.Status {
			case "ok":
				result.RcptStatus = "catch_all"
				result.CatchAll = boolPtr(true)
				result.Informative = boolPtr(false)
				result.Note = "The server also accepted a generated nonexistent recipient; this domain appears to be catch-all."
			case "reject":
				result.CatchAll = boolPtr(false)
				result.Informative = boolPtr(true)
				result.Note = "The recipient was accepted while a generated recipient was rejected."
			default:
				result.Informative = boolPtr(false)
				result.Retryable = retryableSMTPStatus(catchAttempt.Status)
				result.Note = "The recipient was accepted, but the catch-all control probe was inconclusive."
			}
			return result
		case "reject":
			rejects++
		case "tempfail", "timeout", "connect_failed":
			retryable = true
		}
		if ctx.Err() != nil {
			retryable = true
			break
		}
	}

	result.Informative = boolPtr(false)
	result.Retryable = retryable
	if rejects == len(routes) && len(routes) > 0 {
		result.RcptStatus = "reject"
		result.Informative = boolPtr(true)
		result.Note = "Every reachable mail exchanger rejected the recipient."
		return result
	}
	if retryable {
		result.RcptStatus = "tempfail"
		result.Note = "Verification was temporarily unavailable; retry later."
		return result
	}
	if len(result.Attempts) > 0 {
		result.RcptStatus = result.Attempts[len(result.Attempts)-1].Status
	} else {
		result.RcptStatus = "unavailable"
	}
	result.Note = "No mail exchanger produced a definitive recipient response."
	return result
}

func probeSMTPHost(ctx context.Context, mxHost, email string, timeout time.Duration) SMTPAttempt {
	return probeSMTPHostWithDial(ctx, mxHost, email, timeout, func(ctx context.Context, address string, timeout time.Duration) (net.Conn, error) {
		return (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", address)
	})
}

func probeSMTPHostWithDial(ctx context.Context, mxHost, email string, timeout time.Duration, dial smtpDialFunc) SMTPAttempt {
	result := SMTPAttempt{MX: mxHost, Status: "unknown"}
	conn, err := dial(ctx, net.JoinHostPort(mxHost, "25"), timeout)
	if err != nil {
		result.Response = err.Error()
		if isTimeout(err) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.Status = "timeout"
		} else {
			result.Status = "connect_failed"
		}
		return result
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}

	client, err := smtp.NewClient(conn, mxHost)
	if err != nil {
		return smtpTransportFailure(result, err)
	}
	defer client.Close()

	helo := strings.TrimSpace(os.Getenv("EMAIL_CHECKER_SMTP_HELO"))
	if helo == "" {
		helo = "checker.apteva.local"
	}
	if err := client.Hello(helo); err != nil {
		return smtpTransportFailure(result, err)
	}
	mailFrom := strings.TrimSpace(os.Getenv("EMAIL_CHECKER_SMTP_FROM"))
	if err := client.Mail(mailFrom); err != nil {
		return smtpTransportFailure(result, err)
	}
	if err := client.Rcpt(email); err != nil {
		result.Response = err.Error()
		if code, message := parseSMTPErr(err); code != 0 {
			result.Code = code
			result.Response = message
			switch {
			case code >= 500 && code < 600:
				result.Status = "reject"
			case code >= 400 && code < 500:
				result.Status = "tempfail"
			default:
				result.Status = "unknown"
			}
		} else if isTimeout(err) {
			result.Status = "timeout"
		}
		return result
	}
	result.Code = 250
	result.Status = "ok"
	_ = client.Quit()
	return result
}

func smtpTransportFailure(result SMTPAttempt, err error) SMTPAttempt {
	result.Response = err.Error()
	if code, message := parseSMTPErr(err); code != 0 {
		result.Code = code
		result.Response = message
		if code >= 400 && code < 500 {
			result.Status = "tempfail"
		} else {
			result.Status = "connect_failed"
		}
	} else if isTimeout(err) {
		result.Status = "timeout"
	} else {
		result.Status = "connect_failed"
	}
	return result
}

func parseSMTPErr(err error) (int, string) {
	s := err.Error()
	if len(s) < 4 || s[3] != ' ' {
		return 0, s
	}
	code := 0
	for i := 0; i < 3; i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, s
		}
		code = code*10 + int(s[i]-'0')
	}
	return code, strings.TrimSpace(s[4:])
}

func dnsNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout())
}

func retryableSMTPStatus(status string) bool {
	return status == "tempfail" || status == "timeout" || status == "connect_failed" || status == "unavailable"
}

func domainListed(domain string, list map[string]bool) bool {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	for {
		if list[domain] {
			return true
		}
		dot := strings.IndexByte(domain, '.')
		if dot < 0 {
			return false
		}
		domain = domain[dot+1:]
	}
}

func randomProbeLocal() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err == nil {
		return "apteva-check-" + hex.EncodeToString(buffer)
	}
	return "apteva-check-" + time.Now().UTC().Format("20060102150405.000000000")
}
