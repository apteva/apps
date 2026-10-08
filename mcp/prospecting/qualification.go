package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	sdk "github.com/apteva/app-sdk"
)

var deterministicNoiseDomains = []string{
	"reddit.com", "zocdoc.com", "yelp.com", "healthgrades.com", "vitals.com",
	"facebook.com", "instagram.com", "linkedin.com", "tiktok.com", "youtube.com",
	"x.com", "twitter.com", "pinterest.com", "wikipedia.org", "mapquest.com",
	"yellowpages.com", "bbb.org", "indeed.com", "glassdoor.com", "ziprecruiter.com",
	"etsy.com", "amazon.com", "ebay.com", "walmart.com", "alibaba.com", "aliexpress.com",
	"scribd.com", "template.net", "jotform.com", "pdffiller.com", "signnow.com", "dochub.com",
	"myftpupload.com", "modento.io", "jarvisanalytics.com", "floridaweekly.com",
	"blackowneddentalpractices.com", "dhpsupply.com", "ada.org", "dentistemaillist.com",
	"issuu.com", "hofstra.edu", "namwolf.org", "wherewespend.com", "publicleads.com",
}

var genericPageTitles = map[string]bool{
	"about": true, "about us": true, "contact": true, "contact us": true,
	"home": true, "homepage": true, "website": true, "official website": true,
	"new patient": true, "new patients": true,
	"new patient forms": true, "patient forms": true, "patient information": true,
	"patient resources": true, "request appointment": true, "schedule appointment": true,
	"meet the team": true, "our team": true, "team": true,
}

var emailPattern = regexp.MustCompile(`(?i)[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+`)
var phonePattern = regexp.MustCompile(`(?:\+?\d[\d().\s-]{6,}\d)`)
var usAddressPattern = regexp.MustCompile(`(?i)\b[A-Za-z][A-Za-z .'-]{1,40},?\s+(AL|AK|AZ|AR|CA|CO|CT|DE|FL|GA|HI|ID|IL|IN|IA|KS|KY|LA|ME|MD|MA|MI|MN|MS|MO|MT|NE|NV|NH|NJ|NM|NY|NC|ND|OH|OK|OR|PA|RI|SC|SD|TN|TX|UT|VT|VA|WA|WV|WI|WY|DC)\s+\d{5}(?:-\d{4})?\b`)
var doctorNamePattern = regexp.MustCompile(`(?i)\b(?:Dr\.?|Dra\.?)\s+([A-ZÀ-ÖØ-Ý][\p{L}'’-]+(?:\s+[A-ZÀ-ÖØ-Ý][\p{L}'’.()-]+){1,4})`)

type signalDefinition struct {
	Key      string
	Label    string
	Weight   int
	Patterns []string
}

var signalDefinitions = []signalDefinition{
	{Key: "manual_confirmation", Label: "Manual appointment confirmation", Weight: 12, Patterns: []string{"call to confirm", "contact you to confirm", "office will contact", "team will contact", "confirm the day and time"}},
	{Key: "missed_calls", Label: "Missed-call or after-hours workflow", Weight: 12, Patterns: []string{"missed call", "after hours", "leave a message", "voicemail", "emergency line"}},
	{Key: "insurance_admin", Label: "Insurance administration", Weight: 10, Patterns: []string{"insurance information", "insurance verification", "verify insurance", "insurance provider", "insurance accepted", "insurance forms"}},
	{Key: "front_desk_workload", Label: "Front-desk coordination workload", Weight: 10, Patterns: []string{"patient coordinator", "front desk", "multiple telephone lines", "answer, screen, and forward calls", "check-in and check-out", "schedule appointments"}},
	{Key: "reminders_recalls", Label: "Reminder or recall workflow", Weight: 10, Patterns: []string{"appointment reminder", "text reminder", "recall appointment", "patient recall", "reactivation", "follow-up reminder"}},
	{Key: "appointment_request", Label: "Online appointment requests", Weight: 8, Patterns: []string{"request appointment", "schedule appointment", "book appointment", "book online", "online appointment", "make an appointment", "pedir cita"}},
	{Key: "new_patient_intake", Label: "New-patient intake forms", Weight: 8, Patterns: []string{"new patient form", "patient forms", "patient intake", "medical history form", "complete forms", "patient registration"}},
	{Key: "chat_messaging", Label: "Chat or messaging intake", Weight: 8, Patterns: []string{"whatsapp", "live chat", "chat with us", "text us", "send us a message"}},
	{Key: "multiple_locations", Label: "Multiple-location routing", Weight: 6, Patterns: []string{"our locations", "multiple locations", "choose a location", "find a location", "locations near you"}},
	{Key: "payments_financing", Label: "Payment or financing administration", Weight: 5, Patterns: []string{"payment plan", "patient financing", "financing options", "collect payments", "membership plan"}},
}

type extractedPerson struct {
	FirstName   string
	LastName    string
	DisplayName string
	JobTitle    string
	Rank        int
}

func isNoiseDomain(domain string) (bool, string) {
	domain = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(domain), "www."))
	for _, blocked := range deterministicNoiseDomains {
		if domain == blocked || strings.HasSuffix(domain, "."+blocked) {
			return true, "known directory, marketplace, social, community, or job platform"
		}
	}
	return false, ""
}

func classifySearchResult(result webSearchResult, domain string) (bool, string) {
	if blocked, reason := isNoiseDomain(domain); blocked {
		return false, reason
	}
	title := strings.ToLower(strings.TrimSpace(result.Title))
	snippet := strings.ToLower(strings.TrimSpace(result.Snippet))
	providerSignals := []string{"marketing agency", "website design for dentists", "dental marketing", "software for dental", "dentist directory", "find a dentist"}
	if containsAny(title+" "+snippet, providerSignals) {
		return false, "provider or directory content rather than an operating target company"
	}
	if containsAny(title+" "+snippet, []string{"patient form template", "dental form template", "printable dental form", "download dental form", "editable dental form", "dental forms pdf"}) {
		return false, "form template or marketplace content rather than an operating target company"
	}
	if containsAny(title, []string{"top 10 ", "best dental websites", "dental website examples", "how to ", "guide to ", "marketing ideas"}) {
		return false, "editorial or list content rather than an operating target company"
	}
	if strings.HasSuffix(strings.ToLower(result.URL), ".pdf") || containsAny(title+" "+snippet, []string{
		"email list", "verified us practices", "dental supplies", "calendar of events", "scholarship",
		"audit report", "interactive map directory", "download pdf",
	}) {
		return false, "document, directory, or supplier content rather than an operating target company"
	}
	return true, ""
}

func parseDuckDuckGoText(text string, limit int) []webSearchResult {
	rawLines := strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	results := make([]webSearchResult, 0, limit)
	seen := map[string]bool{}
	for i, line := range lines {
		candidateURL := line
		if !strings.Contains(candidateURL, "://") && strings.Contains(candidateURL, ".") && !strings.Contains(candidateURL, " ") {
			candidateURL = "https://" + strings.TrimPrefix(candidateURL, "//")
		}
		website, domain := normalizeWebsite(candidateURL)
		if website == "" || domain == "" || domain == "duckduckgo.com" || seen[website] {
			continue
		}
		title := domain
		if i > 0 {
			title = lines[i-1]
		}
		snippet := ""
		if i+1 < len(lines) {
			snippet = lines[i+1]
			if _, nextDomain := normalizeWebsite(snippet); nextDomain != "" {
				snippet = ""
			}
		}
		seen[website] = true
		results = append(results, webSearchResult{Title: title, URL: website, Snippet: snippet, Source: "duckduckgo", Rank: len(results) + 1, FetchedAt: nowUTC(), Confidence: "medium"})
		if len(results) >= limit {
			break
		}
	}
	return results
}

func executeWebSearch(ctx *sdk.AppCtx, query string, limit int, engine, fallbackEngine string) (webSearchOutput, string, bool, error) {
	return executeWebSearchContext(context.Background(), ctx, query, limit, engine, fallbackEngine)
}

func executeWebSearchContext(c context.Context, ctx *sdk.AppCtx, query string, limit int, engine, fallbackEngine string) (webSearchOutput, string, bool, error) {
	engine = strings.ToLower(defaultString(engine, "google"))
	fallbackEngine = strings.ToLower(defaultString(fallbackEngine, "duckduckgo"))
	call := func(selected string) (webSearchOutput, error) {
		var out webSearchOutput
		deadline, cancel := context.WithTimeout(c, 45*time.Second)
		defer cancel()
		err := sdk.CallAppResultContext(deadline, ctx.PlatformAPI(), "web", "web_search", map[string]any{
			"query": query, "limit": limit, "engine": selected, "visit_top": false, "store": true,
		}, &out)
		if len(out.Results) == 0 && selected == "duckduckgo" && strings.TrimSpace(out.Page.Text) != "" {
			out.Results = parseDuckDuckGoText(out.Page.Text, limit)
			out.Count = len(out.Results)
		}
		return out, err
	}
	out, err := call(engine)
	blocked := searchShouldFallback(err)
	blocked = blocked || out.Blocked
	if !blocked || fallbackEngine == "" || fallbackEngine == engine {
		if err != nil {
			return out, engine, false, err
		}
		if out.Blocked {
			return out, engine, false, errors.New(defaultString(out.Error, "search provider blocked the request"))
		}
		return out, engine, false, nil
	}
	fallback, fallbackErr := call(fallbackEngine)
	if fallbackErr != nil {
		return fallback, fallbackEngine, true, fmt.Errorf("%s blocked; %s fallback failed: %w", engine, fallbackEngine, fallbackErr)
	}
	if fallback.Blocked {
		return fallback, fallbackEngine, true, errors.New(defaultString(fallback.Error, "fallback search provider blocked the request"))
	}
	return fallback, fallbackEngine, true, nil
}

func searchShouldFallback(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "search_blocked") || strings.Contains(lower, "search_extraction_incomplete")
}

func qualifyCandidate(ctx *sdk.AppCtx, id int64, maxPages int) (map[string]any, error) {
	return qualifyCandidateContext(context.Background(), ctx, id, maxPages)
}

func qualifyCandidateContext(c context.Context, ctx *sdk.AppCtx, id int64, maxPages int) (map[string]any, error) {
	if ctx == nil || ctx.AppDB() == nil {
		return nil, errors.New("prospecting context unavailable")
	}
	if err := requireOptionalApp(ctx, "web"); err != nil {
		return nil, err
	}
	pid := ctx.CurrentProject()
	candidate, err := getCandidate(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if candidate == nil {
		return nil, sql.ErrNoRows
	}
	if candidate.Status == "accepted" || candidate.Status == "rejected" || candidate.Status == "deferred" {
		return nil, fmt.Errorf("%s candidates cannot be automatically requalified", candidate.Status)
	}
	profile, err := getProfile(ctx.AppDB(), pid, candidate.ProfileID)
	if err != nil || profile == nil {
		return nil, fmt.Errorf("load target profile: %w", err)
	}
	maxPages = clamp(maxPages, 1, 5)
	startURL := defaultString(candidate.SourceURL, candidate.Website)
	if candidate.Source == "google_places" {
		startURL = candidate.Website
	}
	if startURL == "" {
		return nil, errors.New("candidate needs a website or source_url for qualification")
	}
	queue := []string{startURL}
	seen := map[string]bool{}
	queued := map[string]bool{startURL: true}
	pages := make([]webExtractPage, 0, maxPages)
	errorsByURL := map[string]string{}
	for len(queue) > 0 && len(pages) < maxPages && len(seen) < maxPages*2 {
		if c.Err() != nil {
			return nil, c.Err()
		}
		pageURL := queue[0]
		queue = queue[1:]
		if seen[pageURL] {
			continue
		}
		seen[pageURL] = true
		var out webExtractOutput
		deadline, cancel := context.WithTimeout(c, 45*time.Second)
		callErr := sdk.CallAppResultContext(deadline, ctx.PlatformAPI(), "web", "web_extract", map[string]any{
			"url": pageURL, "formats": []string{"links", "structured_data", "metadata", "text"},
			"max_chars": 50000, "store": true, "snapshot": false,
		}, &out)
		cancel()
		if callErr != nil {
			errorsByURL[pageURL] = callErr.Error()
			continue
		}
		page := out.Page
		if page.URL == "" {
			page.URL = pageURL
		}
		if page.Error != "" || page.Status >= 400 {
			errorsByURL[pageURL] = defaultString(page.Error, fmt.Sprintf("HTTP %d", page.Status))
			continue
		}
		pages = append(pages, page)
		artifactID := (*int64)(nil)
		if page.Artifact != nil && page.Artifact.ID > 0 {
			v := page.Artifact.ID
			artifactID = &v
		}
		_ = addEvidence(ctx.AppDB(), pid, Evidence{
			CandidateID: id, SourceKind: "web_extract", Title: page.Title, URL: defaultString(page.FinalURL, page.URL),
			Excerpt: qualificationExcerpt(page), ArtifactID: artifactID, RetrievedAt: nowUTC(),
		})
		links := selectQualificationLinks(page, candidate.CompanyDomain, maxPages*2)
		toAdd := make([]string, 0, len(links)+1)
		for _, link := range links {
			if !seen[link] && !queued[link] {
				queued[link] = true
				toAdd = append(toAdd, link)
			}
		}
		if len(pages) == 1 {
			if root := rootURL(defaultString(candidate.Website, startURL)); root != "" && !seen[root] && !queued[root] {
				queued[root] = true
				toAdd = append(toAdd, root)
			}
			// Contact/team/about links from the search result page are more
			// likely to contain identity and contact facts than its homepage.
			queue = append(toAdd, queue...)
		} else {
			queue = append(queue, toAdd...)
		}
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("web qualification could not extract a candidate page: %v", errorsByURL)
	}
	original := *candidate
	applyDeterministicQualification(profile, candidate, pages)
	preserveOperatorFields(candidate, &original)
	updated, err := saveCandidateQualification(ctx.AppDB(), pid, candidate)
	if err != nil {
		return nil, err
	}
	ctx.EmitWithProject("prospecting.candidate.qualified", pid, candidateEvent(updated))
	evidence, _ := listEvidence(ctx.AppDB(), pid, id)
	pageRefs := make([]map[string]any, 0, len(pages))
	for _, page := range pages {
		pageRefs = append(pageRefs, map[string]any{"url": defaultString(page.FinalURL, page.URL), "title": page.Title, "status": page.Status})
	}
	return map[string]any{
		"candidate": updated, "evidence": evidence, "pages_reviewed": pageRefs,
		"page_errors": errorsByURL, "rejected": updated.Status == "rejected",
	}, nil
}

func qualifyBatch(ctx *sdk.AppCtx, profileID int64, status string, limit, maxPages int, requalify bool) (map[string]any, error) {
	if ctx == nil || ctx.AppDB() == nil {
		return nil, errors.New("prospecting context unavailable")
	}
	if err := requireOptionalApp(ctx, "web"); err != nil {
		return nil, err
	}
	if status == "" {
		status = "ready"
	}
	limit = clamp(limit, 1, 25)
	maxPages = clamp(maxPages, 1, 5)
	allCandidates, _, err := listCandidates(ctx.AppDB(), ctx.CurrentProject(), candidateFilter{ProfileID: profileID, Status: status, Limit: 200})
	if err != nil {
		return nil, err
	}
	candidates := make([]Candidate, 0, limit)
	skippedEnriched := 0
	for _, candidate := range allCandidates {
		if !requalify && candidate.EnrichedAt != "" {
			skippedEnriched++
			continue
		}
		candidates = append(candidates, candidate)
		if len(candidates) >= limit {
			break
		}
	}
	results := make([]map[string]any, 0, len(candidates))
	qualified, rejected, failed := 0, 0, 0
	for _, candidate := range candidates {
		out, qualifyErr := qualifyCandidate(ctx, candidate.ID, maxPages)
		row := map[string]any{"id": candidate.ID, "company_name": candidate.CompanyName}
		if qualifyErr != nil {
			row["error"] = qualifyErr.Error()
			failed++
		} else {
			updated := out["candidate"].(*Candidate)
			row["candidate"] = updated
			if updated.Status == "rejected" {
				rejected++
			} else {
				qualified++
			}
		}
		results = append(results, row)
	}
	return map[string]any{"results": results, "processed": len(results), "qualified": qualified, "rejected": rejected, "failed": failed, "skipped_enriched": skippedEnriched}, nil
}

func qualificationExcerpt(page webExtractPage) string {
	text := compactWhitespace(defaultString(page.Description, page.Text))
	return truncate(text, 700)
}

func selectQualificationLinks(page webExtractPage, domain string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	type rankedLink struct {
		URL   string
		Rank  int
		Order int
	}
	ranked := []rankedLink{}
	for i, link := range page.Links {
		resolved := resolvePageLink(defaultString(page.FinalURL, page.URL), link.URL)
		website, linkDomain := normalizeWebsite(resolved)
		if website == "" || linkDomain == "" || (domain != "" && linkDomain != domain) {
			continue
		}
		haystack := strings.ToLower(link.Text + " " + website)
		rank := 0
		switch {
		case containsAny(haystack, []string{"contact", "contact us", "get in touch", "location"}):
			rank = 100
		case containsAny(haystack, []string{"team", "staff", "meet", "doctor", "dentist", "leadership", "owner"}):
			rank = 90
		case containsAny(haystack, []string{"about", "our practice", "our office"}):
			rank = 80
		case containsAny(haystack, []string{"appointment", "new patient", "patient form", "insurance"}):
			rank = 70
		default:
			continue
		}
		ranked = append(ranked, rankedLink{URL: website, Rank: rank, Order: i})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Rank == ranked[j].Rank {
			return ranked[i].Order < ranked[j].Order
		}
		return ranked[i].Rank > ranked[j].Rank
	})
	seen := map[string]bool{}
	out := make([]string, 0, limit)
	for _, item := range ranked {
		if seen[item.URL] {
			continue
		}
		seen[item.URL] = true
		out = append(out, item.URL)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func resolvePageLink(base, href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	parsed, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if parsed.IsAbs() {
		return parsed.String()
	}
	baseURL, err := url.Parse(base)
	if err != nil || baseURL.Hostname() == "" {
		return ""
	}
	return baseURL.ResolveReference(parsed).String()
}

func applyDeterministicQualification(profile *TargetProfile, candidate *Candidate, pages []webExtractPage) {
	corpusParts := make([]string, 0, len(pages)*4)
	for _, page := range pages {
		corpusParts = append(corpusParts, page.Title, page.Description, page.Text, metadataText(page.Metadata))
	}
	corpus := compactWhitespace(strings.Join(corpusParts, "\n"))
	lowerCorpus := strings.ToLower(corpus)
	if name := extractCompanyName(candidate, pages); name != "" {
		candidate.CompanyName = name
	}
	if _, domain := normalizeWebsite(defaultString(pages[0].FinalURL, pages[0].URL)); domain != "" {
		candidate.CompanyDomain = domain
		candidate.Website = "https://" + domain
	}
	// A second qualification pass is a refresh of website-derived contacts,
	// not a validation of stale extraction output. Rebuild both fields from
	// the current first-party pages while preserving manually supplied values
	// on the initial qualification pass.
	if candidate.EnrichedAt != "" {
		candidate.Email = ""
		candidate.Phone = ""
	} else {
		candidate.Email = normalizeQualifiedEmail(candidate.Email, candidate.CompanyDomain)
	}
	if email := extractBestEmail(pages, candidate.CompanyDomain); candidate.Email == "" && email != "" {
		candidate.Email = email
	}
	if !validQualifiedPhone(candidate.Phone, profile.Locations) {
		candidate.Phone = ""
	}
	if phone := extractBestPhone(pages, profile.Locations); candidate.Phone == "" && phone != "" {
		candidate.Phone = phone
	}
	if candidate.EnrichedAt != "" && candidate.PersonDisplayName != "" && !looksPersonName(candidate.PersonDisplayName) {
		// Remove the exact sentence generated by older qualification versions,
		// while retaining operator notes and unrelated business observations.
		claim := candidate.PersonDisplayName + " is listed as " + defaultString(candidate.JobTitle, "public team member")
		candidate.Summary = compactWhitespace(strings.ReplaceAll(strings.ReplaceAll(candidate.Summary, claim+".", ""), claim, ""))
		candidate.PersonFirstName, candidate.PersonLastName, candidate.PersonDisplayName, candidate.JobTitle = "", "", "", ""
	}
	if person := extractDecisionMaker(pages, profile.TargetTitles); candidate.PersonDisplayName == "" && person.DisplayName != "" {
		candidate.PersonFirstName = person.FirstName
		candidate.PersonLastName = person.LastName
		candidate.PersonDisplayName = person.DisplayName
		candidate.JobTitle = person.JobTitle
	}
	candidate.Location = extractLocation(pages, profile.Locations)
	if estimate := estimateEmployees(pages); estimate > 0 {
		candidate.EmployeeEstimate = &estimate
	}
	candidate.LocationCount = estimateLocationCount(pages, candidate.Location)
	candidate.AutomationSignals = detectAutomationSignals(pages)
	candidate.Eligibility, candidate.EligibilityReasons = determineEligibility(profile, candidate, lowerCorpus)
	candidate.Summary = deterministicSummary(profile, candidate)
}

func extractCompanyName(candidate *Candidate, pages []webExtractPage) string {
	// Search-result titles are usually a better company label than a person's
	// name or a stray heading found on a deep contact page. Only replace a
	// useful discovery name when the extracted site name clearly agrees with
	// the candidate's domain brand.
	current := strings.TrimSpace(candidate.CompanyName)
	currentUseful := validCompanyName(current) && !isGenericCompanyTitle(current)
	domainToken := strings.ToLower(strings.Split(strings.TrimPrefix(candidate.CompanyDomain, "www."), ".")[0])
	for _, page := range pages {
		for _, key := range []string{"og:site_name", "application-name", "apple-mobile-web-app-title"} {
			if value := metadataString(page.Metadata, key); validCompanyName(value) {
				cleaned := cleanCompanyTitle(value, candidate.CompanyDomain)
				if !currentUseful || companyNameMatchesDomain(cleaned, domainToken) {
					return cleaned
				}
			}
		}
	}
	if currentUseful {
		return current
	}
	for _, page := range pages {
		title := cleanCompanyTitle(page.Title, candidate.CompanyDomain)
		if validCompanyName(title) && !genericPageTitles[strings.ToLower(title)] {
			return title
		}
	}
	if genericPageTitles[strings.ToLower(strings.TrimSpace(candidate.CompanyName))] || len(candidate.CompanyName) > 100 {
		return domainBrand(candidate.CompanyDomain)
	}
	return candidate.CompanyName
}

func companyNameMatchesDomain(name, domainToken string) bool {
	name = strings.ToLower(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, name))
	domainToken = strings.ToLower(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, domainToken))
	return len(name) >= 4 && len(domainToken) >= 4 && (strings.Contains(domainToken, name) || strings.Contains(name, domainToken))
}

func validCompanyName(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 2 || len(value) > 100 {
		return false
	}
	lower := strings.ToLower(value)
	return lower != "wordpress" && lower != "home" && lower != "contact" && lower != "new patients"
}

func domainBrand(domain string) string {
	base := strings.Split(strings.TrimPrefix(domain, "www."), ".")[0]
	for _, token := range []string{"dentistry", "orthodontics", "orthodontic", "pediatric", "family", "dental", "clinic", "practice", "smiles", "dentures"} {
		base = strings.ReplaceAll(base, token, " "+token+" ")
	}
	return titleWords(compactWhitespace(base))
}

func extractBestEmail(pages []webExtractPage, domain string) string {
	type rankedEmail struct {
		Value string
		Score int
		Order int
	}
	candidates := []rankedEmail{}
	order := 0
	add := func(raw string, score int) {
		email := normalizeQualifiedEmail(raw, domain)
		if email == "" || containsAny(email, []string{"example.com", "sentry.io", "wixpress.com", "noreply@", "no-reply@", "privacy@", "abuse@"}) {
			return
		}
		if domain != "" && strings.HasSuffix(email, "@"+domain) {
			score += 100
		}
		local := strings.SplitN(email, "@", 2)[0]
		if containsAny(local, []string{"info", "contact", "office", "hello", "reception", "frontdesk", "schedule", "appointment", "admin"}) {
			score += 20
		}
		candidates = append(candidates, rankedEmail{Value: email, Score: score, Order: order})
		order++
	}
	for _, page := range pages {
		pageScore := 0
		if containsAny(strings.ToLower(page.URL+" "+page.FinalURL+" "+page.Title), []string{"contact", "location", "appointment"}) {
			pageScore = 20
		}
		for _, link := range page.Links {
			if strings.HasPrefix(strings.ToLower(link.URL), "mailto:") {
				add(strings.TrimPrefix(strings.Split(link.URL, "?")[0], "mailto:"), 80+pageScore)
			}
		}
		corpus := deobfuscateEmailText(strings.Join([]string{page.Text, page.Description, metadataText(page.Metadata), structuredDataText(page.StructuredData)}, " "))
		for _, raw := range emailPattern.FindAllString(corpus, -1) {
			add(raw, 40+pageScore)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].Order < candidates[j].Order
		}
		return candidates[i].Score > candidates[j].Score
	})
	if len(candidates) > 0 {
		return candidates[0].Value
	}
	return ""
}

var publicMailboxDomains = map[string]bool{
	"gmail.com": true, "googlemail.com": true, "yahoo.com": true, "outlook.com": true,
	"hotmail.com": true, "live.com": true, "aol.com": true, "icloud.com": true,
	"me.com": true, "proton.me": true, "protonmail.com": true,
}

func normalizeQualifiedEmail(raw, targetDomain string) string {
	email := normalizeEmail(raw)
	if email == "" {
		return ""
	}
	parts := strings.SplitN(email, "@", 2)
	local, emailDomain := parts[0], strings.TrimPrefix(parts[1], "www.")
	// Browser text can collapse a phone and an email into one token. Recover a
	// recognized mailbox suffix, but never retain the concatenated prefix.
	for _, mailbox := range []string{"appointments", "appointment", "reception", "frontdesk", "contact", "office", "hello", "schedule", "admin", "info"} {
		if len(local) > len(mailbox) && strings.HasSuffix(local, mailbox) {
			local = mailbox
			break
		}
	}
	if len(local) > 32 || regexp.MustCompile(`\d{5,}`).MatchString(local) {
		return ""
	}
	targetDomain = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(targetDomain), "www."))
	if targetDomain != "" && emailDomain != targetDomain && !strings.HasSuffix(targetDomain, "."+emailDomain) && !publicMailboxDomains[emailDomain] {
		return ""
	}
	return local + "@" + emailDomain
}

func extractBestPhone(pages []webExtractPage, locations []string) string {
	type rankedPhone struct {
		Value string
		Score int
		Order int
	}
	candidates := []rankedPhone{}
	order := 0
	add := func(raw string, score int, semantic bool) {
		if !semantic && !hasPhoneFormatting(raw) {
			return
		}
		if phone := normalizeQualifiedPhone(raw, locations); phone != "" {
			candidates = append(candidates, rankedPhone{Value: phone, Score: score, Order: order})
			order++
		}
	}
	for _, page := range pages {
		pageScore := 0
		if containsAny(strings.ToLower(page.URL+" "+page.FinalURL+" "+page.Title), []string{"contact", "location", "appointment"}) {
			pageScore = 20
		}
		for _, link := range page.Links {
			if strings.HasPrefix(strings.ToLower(link.URL), "tel:") {
				add(strings.TrimPrefix(link.URL, "tel:"), 100+pageScore, true)
			}
		}
		for _, value := range structuredPhoneValues(page.StructuredData) {
			for _, raw := range phonePattern.FindAllString(value, -1) {
				add(raw, 70+pageScore, true)
			}
		}
		for _, line := range nonEmptyLines(page.Text + "\n" + page.Description + "\n" + metadataText(page.Metadata)) {
			score := 30 + pageScore
			lower := strings.ToLower(line)
			if containsAny(lower, []string{"phone", "call", "tel", "contact", "appointment", "whatsapp", "schedule", "office"}) {
				score += 25
			}
			if containsAny(lower, []string{"fax"}) && !containsAny(lower, []string{"phone", "call", "tel"}) {
				score -= 25
			}
			for _, raw := range phonePattern.FindAllString(line, -1) {
				add(raw, score, hasNearbyPhoneContext(line, raw))
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].Order < candidates[j].Order
		}
		return candidates[i].Score > candidates[j].Score
	})
	if len(candidates) > 0 {
		return candidates[0].Value
	}
	return ""
}

func hasPhoneFormatting(raw string) bool {
	raw = strings.TrimSpace(raw)
	return strings.ContainsAny(raw, "()-. ")
}

func hasNearbyPhoneContext(line, raw string) bool {
	lower := strings.ToLower(line)
	index := strings.Index(line, raw)
	if index < 0 {
		return false
	}
	start := index - 48
	if start < 0 {
		start = 0
	}
	end := index + len(raw) + 48
	if end > len(line) {
		end = len(line)
	}
	return containsAny(lower[start:end], []string{"phone", "call", "tel", "contact", "appointment", "whatsapp", "schedule", "office"})
}

func deobfuscateEmailText(value string) string {
	plainObfuscated := regexp.MustCompile(`(?i)\b[a-z0-9._%+\-]+\s+at\s+[a-z0-9-]+(?:\s+dot\s+[a-z0-9-]+)+\b`)
	value = plainObfuscated.ReplaceAllStringFunc(value, func(match string) string {
		parts := regexp.MustCompile(`(?i)\s+at\s+`).Split(match, 2)
		if len(parts) != 2 {
			return match
		}
		domain := regexp.MustCompile(`(?i)\s+dot\s+`).ReplaceAllString(parts[1], ".")
		return parts[0] + "@" + domain
	})
	replacements := []struct {
		Pattern *regexp.Regexp
		Value   string
	}{
		{regexp.MustCompile(`(?i)\s*(?:\[at\]|\(at\)|\{at\})\s*`), "@"},
		{regexp.MustCompile(`(?i)\s*(?:\[dot\]|\(dot\)|\{dot\})\s*`), "."},
	}
	for _, replacement := range replacements {
		value = replacement.Pattern.ReplaceAllString(value, replacement.Value)
	}
	return value
}

func structuredDataText(value any) string {
	parts := []string{}
	var walk func(any)
	walk = func(item any) {
		switch typed := item.(type) {
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				parts = append(parts, key)
				walk(typed[key])
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		case string:
			parts = append(parts, typed)
		case fmt.Stringer:
			parts = append(parts, typed.String())
		case nil:
		default:
			parts = append(parts, fmt.Sprint(typed))
		}
	}
	walk(value)
	return strings.Join(parts, " ")
}

func structuredPhoneValues(value any) []string {
	values := []string{}
	var walk func(any)
	walk = func(item any) {
		switch typed := item.(type) {
		case map[string]any:
			for key, child := range typed {
				lowerKey := strings.ToLower(strings.TrimSpace(key))
				if lowerKey == "telephone" || lowerKey == "phone" || lowerKey == "phonenumber" || lowerKey == "contactphone" {
					switch contact := child.(type) {
					case string:
						values = append(values, contact)
					case []any:
						for _, entry := range contact {
							if text, ok := entry.(string); ok {
								values = append(values, text)
							}
						}
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return values
}

func normalizeQualifiedPhone(raw string, locations []string) string {
	phone := normalizePhone(raw)
	if !validQualifiedPhone(phone, locations) {
		return ""
	}
	return phone
}

func validQualifiedPhone(phone string, locations []string) bool {
	phone = normalizePhone(phone)
	digits := strings.TrimPrefix(phone, "+")
	if targetsUnitedStates(locations) {
		return len(digits) == 10 || (len(digits) == 11 && strings.HasPrefix(digits, "1"))
	}
	if len(digits) < 10 || len(digits) > 15 {
		return false
	}
	return true
}

func targetsUnitedStates(locations []string) bool {
	for _, location := range locations {
		normalized := strings.ToLower(strings.TrimSpace(location))
		if normalized == "united states" || normalized == "united states of america" || normalized == "usa" || normalized == "us" || normalized == "u.s." {
			return true
		}
	}
	return false
}

// Match role words, never substrings such as COO inside Cookie. The
// captured role offsets preserve the original spelling of a person's name.
func rolePhrasePattern(phrase string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}_])(` + regexp.QuoteMeta(strings.TrimSpace(phrase)) + `)([^\p{L}\p{N}_]|$)`)
}

func extractDecisionMaker(pages []webExtractPage, targetTitles []string) extractedPerson {
	phrases := []string{"practice owner", "owner dentist", "owner", "founder", "co-founder", "managing dentist", "practice manager", "office manager", "clinical director", "medical director", "director", "general manager", "operations manager", "administrative manager", "clinic manager", "gerencia administrativa", "directora general", "director general", "director médico", "gerente de clínica", "coordinadora de clínica"}
	phrases = append(append([]string{}, targetTitles...), phrases...)
	patterns := make([]*regexp.Regexp, len(phrases))
	for i, phrase := range phrases {
		if strings.TrimSpace(phrase) != "" {
			patterns[i] = rolePhrasePattern(phrase)
		}
	}
	best := extractedPerson{}
	for _, page := range pages {
		lines := nonEmptyLines(page.Text)
		for i, line := range lines {
			for rank, pattern := range patterns {
				if pattern == nil {
					continue
				}
				match := pattern.FindStringSubmatchIndex(line)
				if match == nil {
					continue
				}
				remainder := strings.Trim(compactWhitespace(line[:match[4]]+" "+line[match[5]:]), " -–—|:;,()")
				name := remainder
				// Only an isolated role heading may associate with a neighboring name.
				// Narrative sentences and vacancies do not establish a name/role pair.
				if remainder == "" {
					if i > 0 && looksPersonName(lines[i-1]) {
						name = lines[i-1]
					}
					if name == "" && i+1 < len(lines) && looksPersonName(lines[i+1]) {
						name = lines[i+1]
					}
				}
				if !looksPersonName(name) {
					continue
				}
				first, last, display := splitPersonName(name)
				candidate := extractedPerson{FirstName: first, LastName: last, DisplayName: display, JobTitle: strings.TrimSpace(phrases[rank]), Rank: len(phrases) - rank}
				if candidate.Rank > best.Rank {
					best = candidate
				}
			}
		}
	}
	if best.DisplayName != "" {
		return best
	}
	for _, page := range pages {
		match := doctorNamePattern.FindStringSubmatch(page.Title + "\n" + page.Text)
		if len(match) > 1 && looksPersonName(match[1]) {
			first, last, display := splitPersonName(match[1])
			return extractedPerson{FirstName: first, LastName: last, DisplayName: display, JobTitle: "Dentist", Rank: 1}
		}
	}
	return best
}

func looksPersonName(value string) bool {
	value = cleanPersonName(value)
	parts := strings.Fields(value)
	if len(parts) < 2 || len(parts) > 6 || len(value) > 80 {
		return false
	}
	blocked := []string{"our team", "our story", "about us", "meet the team", "contact us", "interview tips", "cookie policy", "privacy policy", "terms of", "job openings", "career opportunities", "professional placement", "technology talent", "staffing services", "dental practice", "office manager position", "general dentistry", "patient coordinator", "practice software", "read more", "learn more", "get started"}
	if containsAny(strings.ToLower(value), blocked) {
		return false
	}
	particles := map[string]bool{"de": true, "del": true, "da": true, "di": true, "van": true, "von": true, "der": true, "den": true, "la": true, "le": true, "dos": true, "du": true, "al": true, "bin": true, "ibn": true}
	navigationWords := map[string]bool{"cookie": true, "policy": true, "privacy": true, "terms": true, "interview": true, "tips": true, "story": true, "services": true, "solutions": true, "staffing": true, "recruiting": true, "careers": true, "contact": true, "about": true, "our": true, "team": true, "apply": true, "resume": true, "resources": true, "support": true, "placement": true, "talent": true}
	roleWords := map[string]bool{"owner": true, "founder": true, "manager": true, "director": true, "chief": true, "officer": true, "president": true, "ceo": true, "coo": true, "cfo": true, "cto": true}
	nameWords := 0
	for _, part := range parts {
		lower := strings.ToLower(strings.Trim(part, "."))
		if roleWords[lower] || navigationWords[lower] {
			return false
		}
		if particles[lower] {
			continue
		}
		first := true
		letters := 0
		for _, r := range part {
			if unicode.IsLetter(r) {
				if first && unicode.IsLower(r) {
					return false
				}
				letters++
				first = false
			} else if r != '-' && r != '\'' && r != '’' && r != '.' {
				return false
			}
		}
		if letters == 0 {
			return false
		}
		nameWords++
	}
	return nameWords >= 2
}

func cleanPersonName(value string) string {
	value = strings.TrimSpace(value)
	value = regexp.MustCompile(`(?i)^(dr\.?|dra\.?|mr\.?|mrs\.?|ms\.?)\s+`).ReplaceAllString(value, "")
	if i := strings.Index(value, ","); i >= 0 {
		value = value[:i]
	}
	return strings.Trim(strings.TrimSpace(value), "-–—|:;()")
}

func splitPersonName(value string) (string, string, string) {
	display := cleanPersonName(value)
	parts := strings.Fields(display)
	if len(parts) == 0 {
		return "", "", ""
	}
	first := parts[0]
	last := ""
	if len(parts) > 1 {
		last = strings.Join(parts[1:], " ")
	}
	return first, last, display
}

func extractLocation(pages []webExtractPage, profileLocations []string) string {
	for _, page := range pages {
		for _, line := range nonEmptyLines(page.Text) {
			if usAddressPattern.MatchString(line) {
				return truncate(line, 180)
			}
		}
	}
	for _, page := range pages {
		for _, location := range profileLocations {
			if location = strings.TrimSpace(location); location != "" && strings.Contains(strings.ToLower(page.Text+" "+metadataText(page.Metadata)), strings.ToLower(location)) {
				return location
			}
		}
	}
	return ""
}

func estimateEmployees(pages []webExtractPage) int {
	people := map[string]bool{}
	for _, page := range pages {
		for _, match := range doctorNamePattern.FindAllStringSubmatch(page.Text, -1) {
			if len(match) > 1 {
				people[strings.ToLower(cleanPersonName(match[1]))] = true
			}
		}
		lines := nonEmptyLines(page.Text)
		for i, line := range lines {
			if containsAny(line, []string{"owner", "founder", "manager", "director", "coordinator", "hygienist", "assistant", "reception"}) && i > 0 && looksPersonName(lines[i-1]) {
				people[strings.ToLower(cleanPersonName(lines[i-1]))] = true
			}
		}
	}
	if len(people) < 2 {
		return 0
	}
	return len(people)
}

func estimateLocationCount(pages []webExtractPage, location string) int {
	addresses := map[string]bool{}
	for _, page := range pages {
		for _, line := range nonEmptyLines(page.Text) {
			if usAddressPattern.MatchString(line) {
				addresses[strings.ToLower(compactWhitespace(line))] = true
			}
		}
	}
	if len(addresses) > 0 {
		return len(addresses)
	}
	if location != "" {
		return 1
	}
	return 0
}

func detectAutomationSignals(pages []webExtractPage) []AutomationSignal {
	type hit struct {
		Evidence string
		URL      string
	}
	hits := map[string]hit{}
	for _, page := range pages {
		for _, line := range nonEmptyLines(page.Text + "\n" + page.Description) {
			lower := strings.ToLower(line)
			for _, definition := range signalDefinitions {
				if _, exists := hits[definition.Key]; exists {
					continue
				}
				if containsAny(lower, definition.Patterns) {
					hits[definition.Key] = hit{Evidence: truncate(compactWhitespace(line), 260), URL: defaultString(page.FinalURL, page.URL)}
				}
			}
		}
	}
	out := make([]AutomationSignal, 0, len(hits))
	for _, definition := range signalDefinitions {
		if match, ok := hits[definition.Key]; ok {
			out = append(out, AutomationSignal{Key: definition.Key, Label: definition.Label, Weight: definition.Weight, Evidence: match.Evidence, URL: match.URL})
		}
	}
	return out
}

func determineEligibility(profile *TargetProfile, candidate *Candidate, lowerCorpus string) (string, []string) {
	if blocked, reason := isNoiseDomain(candidate.CompanyDomain); blocked {
		return "ineligible", []string{reason}
	}
	industryTokens := normalizedIndustryTokens(profile.Industries)
	industryMatch := len(industryTokens) == 0 || containsAny(lowerCorpus+" "+strings.ToLower(candidate.CompanyName+" "+candidate.CompanyDomain), industryTokens)
	businessSignals := 0
	if candidate.Phone != "" || candidate.Email != "" {
		businessSignals++
	}
	if candidate.Location != "" {
		businessSignals++
	}
	if containsAny(lowerCorpus, []string{"services", "our practice", "our office", "appointments", "patients", "contact us", "office hours"}) {
		businessSignals++
	}
	if containsAny(lowerCorpus, []string{"marketing agency", "software company", "directory of", "find a dentist near", "compare dentists"}) && !industryMatch {
		return "ineligible", []string{"site describes a provider or directory rather than a target operating company"}
	}
	reasons := []string{}
	if industryMatch {
		reasons = append(reasons, "industry evidence matches the target profile")
	} else {
		reasons = append(reasons, "target industry was not confirmed on reviewed pages")
	}
	if businessSignals > 0 {
		reasons = append(reasons, fmt.Sprintf("%d operating-business signal(s) confirmed", businessSignals))
	}
	if industryMatch && businessSignals >= 1 {
		return "eligible", reasons
	}
	return "review", reasons
}

func normalizedIndustryTokens(industries []string) []string {
	set := map[string]bool{}
	for _, industry := range industries {
		lower := strings.ToLower(industry)
		for _, token := range []string{"dental", "dentist", "dentistry", "orthodont", "pediatric", "clinic", "saas", "software", "accounting", "law firm", "legal", "real estate", "veterinary", "medical", "healthcare"} {
			if strings.Contains(lower, token) {
				set[token] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for token := range set {
		out = append(out, token)
	}
	sort.Strings(out)
	return out
}

func deterministicSummary(profile *TargetProfile, candidate *Candidate) string {
	parts := []string{candidate.CompanyName}
	if candidate.Location != "" {
		parts[0] += " operates in " + candidate.Location
	}
	if candidate.EmployeeEstimate != nil {
		parts = append(parts, fmt.Sprintf("At least %d named team members were found on public pages", *candidate.EmployeeEstimate))
	}
	if looksPersonName(candidate.PersonDisplayName) {
		role := defaultString(candidate.JobTitle, "public team member")
		parts = append(parts, candidate.PersonDisplayName+" is listed as "+role)
	}
	if len(candidate.AutomationSignals) > 0 {
		labels := make([]string, 0, len(candidate.AutomationSignals))
		for _, signal := range candidate.AutomationSignals {
			labels = append(labels, strings.ToLower(signal.Label))
		}
		parts = append(parts, "Public workflow signals include "+strings.Join(labels, ", "))
	}
	if len(parts) == 1 && strings.TrimSpace(candidate.Summary) != "" {
		return candidate.Summary
	}
	return truncate(strings.Join(parts, ". ")+".", 5000)
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if value, ok := metadata[key]; ok {
		return strings.TrimSpace(fmt.Sprint(value))
	}
	return ""
}

func metadataText(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+" "+fmt.Sprint(metadata[key]))
	}
	return strings.Join(parts, " ")
}

func nonEmptyLines(value string) []string {
	raw := strings.Split(strings.ReplaceAll(value, "\r", ""), "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if line = compactWhitespace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func compactWhitespace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func titleWords(value string) string {
	parts := strings.Fields(value)
	for i, part := range parts {
		if part == "" {
			continue
		}
		runes := []rune(strings.ToLower(part))
		runes[0] = unicode.ToUpper(runes[0])
		parts[i] = string(runes)
	}
	return strings.Join(parts, " ")
}

func rootURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func parsePositiveInt(value string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(value))
	if n < 0 {
		return 0
	}
	return n
}

func preserveOperatorFields(candidate, original *Candidate) {
	fields := map[string]*string{"company_name": &candidate.CompanyName, "company_domain": &candidate.CompanyDomain, "website": &candidate.Website, "person_first_name": &candidate.PersonFirstName, "person_last_name": &candidate.PersonLastName, "person_display_name": &candidate.PersonDisplayName, "job_title": &candidate.JobTitle, "email": &candidate.Email, "phone": &candidate.Phone, "summary": &candidate.Summary, "source_url": &candidate.SourceURL}
	old := map[string]string{"company_name": original.CompanyName, "company_domain": original.CompanyDomain, "website": original.Website, "person_first_name": original.PersonFirstName, "person_last_name": original.PersonLastName, "person_display_name": original.PersonDisplayName, "job_title": original.JobTitle, "email": original.Email, "phone": original.Phone, "summary": original.Summary, "source_url": original.SourceURL}
	for _, key := range original.OperatorFields {
		if field := fields[key]; field != nil {
			*field = old[key]
		}
	}
}
