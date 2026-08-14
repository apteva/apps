package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type TargetProfile struct {
	ID           int64    `json:"id"`
	ProjectID    string   `json:"project_id,omitempty"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Industries   []string `json:"industries"`
	Locations    []string `json:"locations"`
	EmployeeMin  *int     `json:"employee_min,omitempty"`
	EmployeeMax  *int     `json:"employee_max,omitempty"`
	TargetTitles []string `json:"target_titles"`
	Keywords     []string `json:"keywords"`
	Status       string   `json:"status"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	ArchivedAt   string   `json:"archived_at,omitempty"`
}

type SearchRun struct {
	ID             int64  `json:"id"`
	ProjectID      string `json:"project_id,omitempty"`
	ProfileID      int64  `json:"profile_id"`
	Query          string `json:"query"`
	Source         string `json:"source"`
	Status         string `json:"status"`
	RequestedLimit int    `json:"requested_limit"`
	ResultCount    int    `json:"result_count"`
	Error          string `json:"error,omitempty"`
	StartedAt      string `json:"started_at"`
	CompletedAt    string `json:"completed_at,omitempty"`
	CreatedAt      string `json:"created_at"`
}

type Candidate struct {
	ID                 int64              `json:"id"`
	ProjectID          string             `json:"project_id,omitempty"`
	ProfileID          int64              `json:"profile_id"`
	RunID              *int64             `json:"run_id,omitempty"`
	CanonicalKey       string             `json:"canonical_key"`
	CompanyName        string             `json:"company_name"`
	CompanyDomain      string             `json:"company_domain"`
	Website            string             `json:"website"`
	PersonFirstName    string             `json:"person_first_name"`
	PersonLastName     string             `json:"person_last_name"`
	PersonDisplayName  string             `json:"person_display_name"`
	JobTitle           string             `json:"job_title"`
	Email              string             `json:"email"`
	Phone              string             `json:"phone"`
	Summary            string             `json:"summary"`
	Location           string             `json:"location"`
	EmployeeEstimate   *int               `json:"employee_estimate,omitempty"`
	LocationCount      int                `json:"location_count"`
	Eligibility        string             `json:"eligibility"`
	EligibilityReasons []string           `json:"eligibility_reasons"`
	AutomationSignals  []AutomationSignal `json:"automation_signals"`
	FitScore           int                `json:"fit_score"`
	ConfidenceScore    int                `json:"confidence_score"`
	ScoreReasons       json.RawMessage    `json:"score_reasons"`
	Status             string             `json:"status"`
	Source             string             `json:"source"`
	SourceURL          string             `json:"source_url"`
	DecisionReason     string             `json:"decision_reason,omitempty"`
	CRMContactID       *int64             `json:"crm_contact_id,omitempty"`
	ResearchedAt       string             `json:"researched_at,omitempty"`
	AcceptedAt         string             `json:"accepted_at,omitempty"`
	RejectedAt         string             `json:"rejected_at,omitempty"`
	DeferredAt         string             `json:"deferred_at,omitempty"`
	EnrichedAt         string             `json:"enriched_at,omitempty"`
	CreatedAt          string             `json:"created_at"`
	UpdatedAt          string             `json:"updated_at"`
}

type AutomationSignal struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Weight   int    `json:"weight"`
	Evidence string `json:"evidence"`
	URL      string `json:"url,omitempty"`
}

type Evidence struct {
	ID          int64  `json:"id"`
	ProjectID   string `json:"project_id,omitempty"`
	CandidateID int64  `json:"candidate_id"`
	SourceKind  string `json:"source_kind"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Excerpt     string `json:"excerpt"`
	ArtifactID  *int64 `json:"artifact_id,omitempty"`
	RetrievedAt string `json:"retrieved_at"`
}

type Exclusion struct {
	ID        int64  `json:"id"`
	ProjectID string `json:"project_id,omitempty"`
	Kind      string `json:"kind"`
	Value     string `json:"value"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"created_at"`
}

type Handoff struct {
	ID              int64  `json:"id"`
	ProjectID       string `json:"project_id,omitempty"`
	CandidateID     int64  `json:"candidate_id"`
	CRMContactID    int64  `json:"crm_contact_id"`
	ChannelKind     string `json:"channel_kind"`
	ChannelValue    string `json:"channel_value"`
	WasCreated      bool   `json:"was_created"`
	ActivityWarning string `json:"activity_warning,omitempty"`
	CreatedAt       string `json:"created_at"`
}

type candidateFilter struct {
	ProfileID int64
	Status    string
	Q         string
	Limit     int
	Offset    int
}

type candidateInput struct {
	ProfileID         int64
	RunID             *int64
	CompanyName       string
	CompanyDomain     string
	Website           string
	PersonFirstName   string
	PersonLastName    string
	PersonDisplayName string
	JobTitle          string
	Email             string
	Phone             string
	Summary           string
	Source            string
	SourceURL         string
	CanonicalKey      string
}

type webSearchResult struct {
	Title      string `json:"title"`
	URL        string `json:"url"`
	Snippet    string `json:"snippet"`
	Source     string `json:"source"`
	Rank       int    `json:"rank"`
	FetchedAt  string `json:"fetched_at"`
	Confidence string `json:"confidence"`
}

type webSearchOutput struct {
	Results []webSearchResult `json:"results"`
	Count   int               `json:"count"`
	Blocked bool              `json:"blocked"`
	Error   string            `json:"error"`
	Engine  string            `json:"engine"`
	Page    struct {
		Text string `json:"text"`
	} `json:"page"`
}

type webLink struct {
	URL  string `json:"url"`
	Text string `json:"text"`
}

type webExtractPage struct {
	URL            string           `json:"url"`
	FinalURL       string           `json:"final_url"`
	Title          string           `json:"title"`
	Description    string           `json:"description"`
	Text           string           `json:"text"`
	Links          []webLink        `json:"links"`
	Metadata       map[string]any   `json:"metadata"`
	StructuredData any              `json:"structured_data"`
	Status         int              `json:"status"`
	Artifact       *webPageArtifact `json:"artifact"`
	Error          string           `json:"error"`
}

type webExtractOutput struct {
	Page webExtractPage `json:"page"`
}

type qualificationResult struct {
	Candidate *Candidate       `json:"candidate,omitempty"`
	Pages     []webExtractPage `json:"pages,omitempty"`
	Rejected  bool             `json:"rejected"`
	Error     string           `json:"error,omitempty"`
}

type webCitation struct {
	ID      int    `json:"id"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	Excerpt string `json:"excerpt"`
}

type webPageArtifact struct {
	ID int64 `json:"id"`
}

type webResearchSource struct {
	URL      string           `json:"url"`
	FinalURL string           `json:"final_url"`
	Title    string           `json:"title"`
	Text     string           `json:"text"`
	Artifact *webPageArtifact `json:"artifact"`
	Error    string           `json:"error"`
}

type webResearchOutput struct {
	Answer        string              `json:"answer"`
	Citations     []webCitation       `json:"citations"`
	Sources       []webResearchSource `json:"sources"`
	Confidence    string              `json:"confidence"`
	OpenQuestions []string            `json:"open_questions"`
}

type crmContact struct {
	ID           int64  `json:"id"`
	DisplayName  string `json:"display_name"`
	PrimaryEmail string `json:"primary_email"`
	PrimaryPhone string `json:"primary_phone"`
}

type crmUpsertOutput struct {
	Contact    crmContact `json:"contact"`
	WasCreated bool       `json:"was_created"`
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	switch v := args[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func int64Arg(args map[string]any, key string) int64 {
	if args == nil {
		return 0
	}
	switch v := args[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	default:
		return 0
	}
}

func intArg(args map[string]any, key string, fallback int) int {
	n := int(int64Arg(args, key))
	if n == 0 {
		return fallback
	}
	return n
}

func boolArg(args map[string]any, key string, fallback bool) bool {
	v, ok := args[key]
	if !ok {
		return fallback
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		b, err := strconv.ParseBool(x)
		if err == nil {
			return b
		}
	}
	return fallback
}

func stringSliceArg(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok || v == nil {
		return nil
	}
	var values []string
	switch x := v.(type) {
	case []string:
		values = append(values, x...)
	case []any:
		for _, item := range x {
			values = append(values, fmt.Sprint(item))
		}
	case string:
		values = strings.Split(x, ",")
	default:
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

func optionalIntArg(args map[string]any, key string) *int {
	if _, ok := args[key]; !ok || args[key] == nil {
		return nil
	}
	n := int(int64Arg(args, key))
	return &n
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func decodeStrings(raw string) []string {
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return []string{}
	}
	return out
}

func normalizeWebsite(raw string) (website, domain string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", ""
	}
	domain = strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	if domain == "" {
		return "", ""
	}
	u.Fragment = ""
	return u.String(), domain
}

func normalizeEmail(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	parsed, err := mail.ParseAddress(raw)
	if err != nil || !strings.Contains(parsed.Address, "@") {
		return ""
	}
	return strings.ToLower(parsed.Address)
}

var nonPhone = regexp.MustCompile(`[^0-9+]`)

func normalizePhone(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	clean := nonPhone.ReplaceAllString(raw, "")
	if len(strings.TrimPrefix(clean, "+")) < 7 {
		return ""
	}
	return clean
}

func canonicalCandidateKey(domain, website, companyName, sourceURL string) string {
	if domain != "" {
		return "domain:" + strings.ToLower(domain)
	}
	if _, sourceDomain := normalizeWebsite(sourceURL); sourceDomain != "" {
		return "source-domain:" + sourceDomain
	}
	seed := strings.ToLower(strings.TrimSpace(companyName + "|" + website + "|" + sourceURL))
	sum := sha256.Sum256([]byte(seed))
	return "hash:" + hex.EncodeToString(sum[:12])
}

func cleanCompanyTitle(title, domain string) string {
	title = strings.TrimSpace(title)
	for _, sep := range []string{" | ", " — ", " – ", " - "} {
		if parts := strings.SplitN(title, sep, 2); len(parts) == 2 && len(strings.TrimSpace(parts[0])) >= 2 {
			title = strings.TrimSpace(parts[0])
			break
		}
	}
	if title == "" {
		title = strings.Split(domain, ".")[0]
	}
	return title
}

func validateProfile(p *TargetProfile) error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("name required")
	}
	if p.EmployeeMin != nil && *p.EmployeeMin < 0 {
		return errors.New("employee_min must be non-negative")
	}
	if p.EmployeeMax != nil && *p.EmployeeMax < 0 {
		return errors.New("employee_max must be non-negative")
	}
	if p.EmployeeMin != nil && p.EmployeeMax != nil && *p.EmployeeMin > *p.EmployeeMax {
		return errors.New("employee_min must not exceed employee_max")
	}
	return nil
}

func clamp(n, min, max int) int {
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max]) + "…"
}
