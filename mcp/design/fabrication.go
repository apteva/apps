package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Fabrication connects validated Design revisions to external print
// services. Each bound connection in the fabrication_provider role is
// driven by an adapter selected by the connection's catalog slug. Adapters
// only talk to their service through the platform's integration executor,
// so credentials never enter this app.

const fabricationRole = "fabrication_provider"

// Order statuses. Only submit moves money; everything before it is free.
const (
	orderDraft            = "draft"
	orderAwaitingApproval = "awaiting_approval"
	orderApproved         = "approved"
	orderSubmitting       = "submitting"
	orderAwaitingPayment  = "awaiting_payment"
	orderSubmitted        = "submitted"
	orderInProduction     = "in_production"
	orderShipped          = "shipped"
	orderDelivered        = "delivered"
	orderDeclined         = "declined"
	orderCancelled        = "cancelled"
	orderFailed           = "failed"
	orderUnknown          = "submit_unknown"
)

// priceDriftTolerance is how much a provider's draft total may exceed the
// quoted total before the order needs a person's approval again.
const priceDriftTolerance = 0.05

var errNotSupported = errors.New("not supported by this provider")

// ProviderCaps describes what an adapter can do.
type ProviderCaps struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Processes   []string `json:"processes"`
	AutoPay     bool     `json:"auto_pay"`
	Cancel      bool     `json:"cancel"`
	Settings    []string `json:"settings,omitempty"`
	PaymentNote string   `json:"payment_note"`
}

// PartFile is one exported printable part of the pinned revision.
type PartFile struct {
	PartID   string `json:"part_id"`
	Name     string `json:"name"`
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	Quantity int    `json:"quantity"`
	Body     []byte `json:"-"`
}

type QuoteRequest struct {
	DesignID   int64          `json:"design_id"`
	RevisionID int64          `json:"revision_id"`
	Parts      []PartFile     `json:"parts"`
	Currency   string         `json:"currency"`
	Country    string         `json:"country"`
	Material   string         `json:"material,omitempty"`
	MaxOptions int            `json:"max_options"`
	Settings   map[string]any `json:"-"`
}

type QuoteLine struct {
	PartID    string  `json:"part_id"`
	SHA256    string  `json:"sha256"`
	Quantity  int     `json:"quantity"`
	UnitPrice float64 `json:"unit_price"`
	LineTotal float64 `json:"line_total"`
	Ref       string  `json:"ref,omitempty"`
}

type QuoteOption struct {
	ID           string          `json:"id"`
	Provider     string          `json:"provider"`
	ConnectionID int64           `json:"connection_id"`
	Vendor       string          `json:"vendor,omitempty"`
	Process      string          `json:"process,omitempty"`
	Material     string          `json:"material"`
	MaterialRef  string          `json:"material_ref,omitempty"`
	Lines        []QuoteLine     `json:"lines"`
	Subtotal     float64         `json:"subtotal"`
	Shipping     float64         `json:"shipping"`
	ShippingName string          `json:"shipping_name,omitempty"`
	Total        float64         `json:"total"`
	Currency     string          `json:"currency"`
	LeadDays     int             `json:"lead_time_days,omitempty"`
	ExpiresAt    string          `json:"expires_at,omitempty"`
	CheckoutURL  string          `json:"checkout_url,omitempty"`
	Ref          json.RawMessage `json:"provider_ref,omitempty"`
}

// Address is a saved ship-to address. Orders can only ship to these.
type Address struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Company   string `json:"company,omitempty"`
	Line1     string `json:"line1"`
	Line2     string `json:"line2,omitempty"`
	City      string `json:"city"`
	State     string `json:"state,omitempty"`
	Zip       string `json:"zip"`
	Country   string `json:"country"`
	Phone     string `json:"phone"`
	Email     string `json:"email"`
}

// Draft is a prepared, not yet charged order.
type Draft struct {
	Total    float64         `json:"total"`
	Currency string          `json:"currency"`
	Ref      json.RawMessage `json:"provider_ref,omitempty"`
	Note     string          `json:"note,omitempty"`
}

type OrderRef struct {
	ProviderOrderID string `json:"provider_order_id"`
	Status          string `json:"status"`
	PaymentURL      string `json:"payment_url,omitempty"`
	Note            string `json:"note,omitempty"`
}

type OrderStatus struct {
	Status         string `json:"status"`
	ProviderStatus string `json:"provider_status"`
	TrackingNumber string `json:"tracking_number,omitempty"`
	TrackingURL    string `json:"tracking_url,omitempty"`
	Carrier        string `json:"carrier,omitempty"`
}

// FabricationProvider is implemented once per print service.
type FabricationProvider interface {
	Caps() ProviderCaps
	Quote(ctx context.Context, call ToolCaller, req QuoteRequest) ([]QuoteOption, error)
	PrepareOrder(ctx context.Context, call ToolCaller, opt QuoteOption, addr Address, settings map[string]any) (Draft, error)
	Submit(ctx context.Context, call ToolCaller, opt QuoteOption, draft Draft, addr Address, settings map[string]any, idempotencyKey string) (OrderRef, error)
	Status(ctx context.Context, call ToolCaller, providerOrderID string) (OrderStatus, error)
	Cancel(ctx context.Context, call ToolCaller, providerOrderID string) error
}

// ToolCaller executes one connector tool on one bound connection and
// returns the provider's JSON body plus its HTTP status. A non-2xx status
// is returned as an error.
type ToolCaller func(ctx context.Context, tool string, input map[string]any) (json.RawMessage, int, error)

var fabricationProviders = map[string]FabricationProvider{
	"craftcloud": craftcloudProvider{},
	"slant3d":    slant3dProvider{},
	"shapeways":  shapewaysProvider{},
	"treatstock": treatstockProvider{},
	"sculpteo":   sculpteoProvider{},
}

func providerFor(slug string) (FabricationProvider, bool) {
	p, ok := fabricationProviders[strings.ToLower(strings.TrimSpace(slug))]
	return p, ok
}

// FabricationSettings is the per-project order policy and address book.
// It is edited only through the panel's HTTP routes, never by MCP tools.
type FabricationSettings struct {
	AutoOrder        bool                      `json:"auto_order"`
	MaxOrderTotal    float64                   `json:"max_order_total"`
	MonthlyBudget    float64                   `json:"monthly_budget"`
	Currency         string                    `json:"currency"`
	Country          string                    `json:"country"`
	AllowedProviders []string                  `json:"allowed_providers"`
	Addresses        []Address                 `json:"addresses"`
	DefaultAddressID string                    `json:"default_address_id"`
	ProviderSettings map[string]map[string]any `json:"provider_settings"`
}

func defaultFabricationSettings() FabricationSettings {
	return FabricationSettings{Currency: "EUR", Country: "FR", ProviderSettings: map[string]map[string]any{}}
}

var supportedCurrencies = map[string]bool{"USD": true, "EUR": true, "GBP": true, "CAD": true, "AUD": true, "CHF": true}

func (s *FabricationSettings) normalize() error {
	s.Currency = strings.ToUpper(strings.TrimSpace(s.Currency))
	if s.Currency == "" {
		s.Currency = "EUR"
	}
	if !supportedCurrencies[s.Currency] {
		return fmt.Errorf("currency %s is not supported", s.Currency)
	}
	s.Country = strings.ToUpper(strings.TrimSpace(s.Country))
	if len(s.Country) != 2 {
		return errors.New("country must be an ISO 3166-1 alpha-2 code")
	}
	if s.MaxOrderTotal < 0 || s.MonthlyBudget < 0 || math.IsNaN(s.MaxOrderTotal) || math.IsNaN(s.MonthlyBudget) {
		return errors.New("limits must be zero or positive")
	}
	if s.AutoOrder && (s.MaxOrderTotal <= 0 || s.MonthlyBudget <= 0) {
		return errors.New("auto_order needs both max_order_total and monthly_budget above zero")
	}
	seen := map[string]bool{}
	for i := range s.Addresses {
		a := &s.Addresses[i]
		a.ID = strings.TrimSpace(a.ID)
		if a.ID == "" {
			a.ID = fmt.Sprintf("addr-%d", i+1)
		}
		if seen[a.ID] {
			return fmt.Errorf("duplicate address id %s", a.ID)
		}
		seen[a.ID] = true
		a.Country = strings.ToUpper(strings.TrimSpace(a.Country))
		if a.FirstName == "" || a.LastName == "" || a.Line1 == "" || a.City == "" || a.Zip == "" || len(a.Country) != 2 {
			return fmt.Errorf("address %s needs first and last name, line1, city, zip and a 2-letter country", a.ID)
		}
	}
	if s.DefaultAddressID != "" && !seen[s.DefaultAddressID] {
		return errors.New("default_address_id is not in addresses")
	}
	for i, p := range s.AllowedProviders {
		s.AllowedProviders[i] = strings.ToLower(strings.TrimSpace(p))
	}
	if s.ProviderSettings == nil {
		s.ProviderSettings = map[string]map[string]any{}
	}
	return nil
}

func (s FabricationSettings) address(id string) (Address, bool) {
	if id == "" {
		id = s.DefaultAddressID
	}
	for _, a := range s.Addresses {
		if a.ID == id {
			return a, true
		}
	}
	return Address{}, false
}

func (s FabricationSettings) providerAllowed(slug string) bool {
	if len(s.AllowedProviders) == 0 {
		return true
	}
	for _, p := range s.AllowedProviders {
		if p == slug {
			return true
		}
	}
	return false
}

// PolicyVerdict explains whether a draft may be submitted without a person.
type PolicyVerdict struct {
	AutoAllowed  bool     `json:"auto_allowed"`
	Reasons      []string `json:"reasons"`
	MonthSpent   float64  `json:"month_spent"`
	MonthBudget  float64  `json:"month_budget"`
	MaxOrder     float64  `json:"max_order_total"`
	DraftTotal   float64  `json:"draft_total"`
	QuotedTotal  float64  `json:"quoted_total"`
	PriceDriftUp float64  `json:"price_drift_up"`
}

func evaluatePolicy(settings FabricationSettings, provider string, currency string, quoted, draft, monthSpent float64) PolicyVerdict {
	v := PolicyVerdict{MonthSpent: round2(monthSpent), MonthBudget: settings.MonthlyBudget, MaxOrder: settings.MaxOrderTotal, DraftTotal: round2(draft), QuotedTotal: round2(quoted)}
	if quoted > 0 {
		v.PriceDriftUp = round2((draft - quoted) / quoted)
	}
	if !settings.AutoOrder {
		v.Reasons = append(v.Reasons, "auto-order is off")
	}
	if !settings.providerAllowed(provider) {
		v.Reasons = append(v.Reasons, provider+" is not an allowed provider")
	}
	if !strings.EqualFold(currency, settings.Currency) {
		v.Reasons = append(v.Reasons, fmt.Sprintf("order currency %s differs from policy currency %s", currency, settings.Currency))
	}
	if settings.MaxOrderTotal <= 0 || draft > settings.MaxOrderTotal {
		v.Reasons = append(v.Reasons, fmt.Sprintf("total %.2f exceeds the per-order limit %.2f", draft, settings.MaxOrderTotal))
	}
	if settings.MonthlyBudget <= 0 || monthSpent+draft > settings.MonthlyBudget {
		v.Reasons = append(v.Reasons, fmt.Sprintf("this order would bring the month to %.2f of a %.2f budget", monthSpent+draft, settings.MonthlyBudget))
	}
	if quoted > 0 && draft > quoted*(1+priceDriftTolerance) {
		v.Reasons = append(v.Reasons, fmt.Sprintf("price rose from %.2f to %.2f since the quote", quoted, draft))
	}
	v.AutoAllowed = len(v.Reasons) == 0
	return v
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// rankOptions sorts by total then lead time and keeps at most max.
func rankOptions(options []QuoteOption, max int) []QuoteOption {
	sort.SliceStable(options, func(i, j int) bool {
		if options[i].Total != options[j].Total {
			return options[i].Total < options[j].Total
		}
		return options[i].LeadDays < options[j].LeadDays
	})
	if max > 0 && len(options) > max {
		options = options[:max]
	}
	return options
}

func materialMatches(filter, name, ref string) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return true
	}
	return strings.Contains(strings.ToLower(name), filter) || strings.Contains(strings.ToLower(ref), filter)
}

func monthStart(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
}
