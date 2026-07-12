package main

// Stripe integration (v0.8.0+).
//
// Stripe can be configured directly with `stripe_secret_key`. In that
// mode billing creates its own webhook endpoint in Stripe and stores
// the returned signing secret locally, so operators do not need to
// paste a whsec_ value. The older `payment_processor` integration
// remains a fallback for installs that already use it.
//
// When the integration isn't bound, the whole module degrades:
// invoices_send_payment_link returns a clean "bind the integration"
// error, the webhook endpoint returns 503, and everything else in
// billing keeps working as before (manual payment recording).

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

// ─── Helpers ────────────────────────────────────────────────────────

// requireProcessor returns the bound payment_processor integration
// or an error suitable for surfacing to agents/UI.
func requireProcessor(ctx *sdk.AppCtx) (*sdk.BoundIntegration, error) {
	bound := ctx.IntegrationFor("payment_processor")
	if bound == nil {
		return nil, errors.New(
			"no payment_processor bound — bind a Stripe connection in the billing app's settings",
		)
	}
	return bound, nil
}

// executeStripe runs a Stripe integration tool by its catalog name
// (create_checkout_session, create_customer, etc.) and decodes the
// upstream JSON response into `out`.
func executeStripe(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, tool string, input map[string]any, out any) error {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, input)
	if err != nil {
		return fmt.Errorf("stripe %s: %w", tool, err)
	}
	if res == nil || !res.Success {
		status := 0
		if res != nil {
			status = res.Status
		}
		return fmt.Errorf("stripe %s failed (HTTP %d): %s", tool, status, string(safeData(res)))
	}
	if out != nil {
		if err := json.Unmarshal(res.Data, out); err != nil {
			return fmt.Errorf("stripe %s: decode response: %w", tool, err)
		}
	}
	return nil
}

func safeData(r *sdk.ExecuteResult) []byte {
	if r == nil {
		return nil
	}
	return r.Data
}

func stripeSecretKey(ctx *sdk.AppCtx) string {
	return strings.TrimSpace(configString(ctx, "stripe_secret_key", ""))
}

func stripeDirectConfigured(ctx *sdk.AppCtx) bool {
	return stripeSecretKey(ctx) != ""
}

func stripeAPIBase() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("STRIPE_API_BASE")), "/"); v != "" {
		return v
	}
	return "https://api.stripe.com/v1"
}

func stripeFormValues(values neturl.Values, prefix string, v any) {
	switch x := v.(type) {
	case nil:
		return
	case map[string]any:
		for k, child := range x {
			key := k
			if prefix != "" {
				key = prefix + "[" + k + "]"
			}
			stripeFormValues(values, key, child)
		}
	case []map[string]any:
		for i, child := range x {
			stripeFormValues(values, fmt.Sprintf("%s[%d]", prefix, i), child)
		}
	case []any:
		for i, child := range x {
			stripeFormValues(values, fmt.Sprintf("%s[%d]", prefix, i), child)
		}
	case []string:
		for _, child := range x {
			values.Add(prefix+"[]", child)
		}
	case string:
		values.Set(prefix, x)
	case fmt.Stringer:
		values.Set(prefix, x.String())
	case int:
		values.Set(prefix, strconv.Itoa(x))
	case int64:
		values.Set(prefix, strconv.FormatInt(x, 10))
	case float64:
		values.Set(prefix, strconv.FormatFloat(x, 'f', -1, 64))
	case bool:
		values.Set(prefix, strconv.FormatBool(x))
	default:
		values.Set(prefix, fmt.Sprint(x))
	}
}

func executeStripeDirect(ctx *sdk.AppCtx, method, path string, input map[string]any, out any) error {
	secret := stripeSecretKey(ctx)
	if secret == "" {
		return errors.New("stripe_secret_key is not configured")
	}
	values := neturl.Values{}
	for k, v := range input {
		stripeFormValues(values, k, v)
	}
	req, err := http.NewRequest(method, stripeAPIBase()+path, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Stripe-Version", "2024-06-20")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("stripe %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("stripe %s %s failed (HTTP %d): %s", method, path, resp.StatusCode, string(data))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("stripe %s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}

type stripeSettings struct {
	WebhookEndpointID string
	WebhookSecret     string
	WebhookURL        string
	Mode              string
}

func loadStripeSettings(db *sql.DB) (stripeSettings, error) {
	var s stripeSettings
	err := db.QueryRow(
		`SELECT webhook_endpoint_id, webhook_secret, webhook_url, mode
		 FROM billing_stripe_settings
		 WHERE id = 1`,
	).Scan(&s.WebhookEndpointID, &s.WebhookSecret, &s.WebhookURL, &s.Mode)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	return s, err
}

func saveStripeSettings(db *sql.DB, s stripeSettings) error {
	_, err := db.Exec(
		`INSERT INTO billing_stripe_settings
		   (id, webhook_endpoint_id, webhook_secret, webhook_url, mode, updated_at)
		 VALUES (1, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(id) DO UPDATE SET
		   webhook_endpoint_id = excluded.webhook_endpoint_id,
		   webhook_secret = excluded.webhook_secret,
		   webhook_url = excluded.webhook_url,
		   mode = excluded.mode,
		   updated_at = CURRENT_TIMESTAMP`,
		s.WebhookEndpointID, s.WebhookSecret, s.WebhookURL, s.Mode)
	return err
}

func stripeWebhookURL(ctx *sdk.AppCtx) (string, error) {
	if override := strings.TrimSpace(configString(ctx, "stripe_webhook_url", "")); override != "" {
		return strings.TrimRight(override, "/"), nil
	}
	publicURL := stripePublicBaseURL(ctx)
	if publicURL == "" {
		return "", errors.New("platform public URL is not configured; set stripe_webhook_url to a public tunnel URL for local testing")
	}
	return strings.TrimRight(publicURL, "/") + "/webhooks/stripe", nil
}

func stripePublicBaseURL(ctx *sdk.AppCtx) string {
	if override := strings.TrimSpace(configString(ctx, "stripe_webhook_url", "")); override != "" {
		if u, err := neturl.Parse(override); err == nil && u.Scheme != "" && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
	}
	var publicURL string
	if info, err := ctx.PlatformInfo(); err == nil && info != nil {
		publicURL = strings.TrimSpace(info.PublicURL)
	}
	if publicURL == "" {
		publicURL = strings.TrimSpace(os.Getenv("APTEVA_PUBLIC_URL"))
	}
	return strings.TrimRight(publicURL, "/")
}

func ensureStripeWebhook(ctx *sdk.AppCtx) error {
	if !stripeDirectConfigured(ctx) {
		return nil
	}
	webhookURL, err := stripeWebhookURL(ctx)
	if err != nil {
		return err
	}
	settings, err := loadStripeSettings(ctx.AppDB())
	if err != nil {
		return err
	}
	if settings.WebhookSecret != "" && settings.WebhookURL == webhookURL {
		return nil
	}
	var endpoint struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		Secret string `json:"secret"`
	}
	input := map[string]any{
		"url": webhookURL,
		"enabled_events": []string{
			"checkout.session.completed",
			"payment_intent.succeeded",
			"setup_intent.succeeded",
			"setup_intent.setup_failed",
			"payment_method.detached",
			"charge.refunded",
		},
		"metadata": map[string]any{
			"apteva_app": "billing",
		},
	}
	if err := executeStripeDirect(ctx, http.MethodPost, "/webhook_endpoints", input, &endpoint); err != nil {
		return err
	}
	if endpoint.ID == "" || endpoint.Secret == "" {
		return errors.New("Stripe did not return a webhook endpoint id and signing secret")
	}
	return saveStripeSettings(ctx.AppDB(), stripeSettings{
		WebhookEndpointID: endpoint.ID,
		WebhookSecret:     endpoint.Secret,
		WebhookURL:        endpoint.URL,
		Mode:              "direct",
	})
}

type setupSessionRequest struct {
	PaymentMethodTypes []string
	SuccessURL         string
	CancelURL          string
	SetDefault         bool
	Metadata           map[string]any
}

func (a *App) createStripeSetupSession(ctx *sdk.AppCtx, pid string, cust *Customer, req setupSessionRequest) (*SetupSession, error) {
	var bound *sdk.BoundIntegration
	if stripeDirectConfigured(ctx) {
		if err := ensureStripeWebhook(ctx); err != nil {
			return nil, fmt.Errorf("stripe webhook setup: %w", err)
		}
	} else {
		var err error
		bound, err = requireProcessor(ctx)
		if err != nil {
			return nil, err
		}
	}
	stripeCustomerID, err := ensureStripeCustomer(ctx, pid, cust, bound)
	if err != nil {
		return nil, err
	}
	successURL := strings.TrimSpace(req.SuccessURL)
	if successURL == "" {
		successURL = configString(ctx, "stripe_setup_success_url", "")
	}
	if successURL == "" {
		successURL = stripePublicBaseURL(ctx) + "/dashboard?app=billing&setup_session={CHECKOUT_SESSION_ID}"
	}
	cancelURL := strings.TrimSpace(req.CancelURL)
	if cancelURL == "" {
		cancelURL = configString(ctx, "stripe_setup_cancel_url", "")
	}
	if cancelURL == "" {
		cancelURL = stripePublicBaseURL(ctx) + "/dashboard?app=billing"
	}
	metadata := mergeMaps(req.Metadata, map[string]any{
		"apteva_project_id":  pid,
		"apteva_customer_id": fmt.Sprintf("%d", cust.ID),
		"apteva_set_default": strconv.FormatBool(req.SetDefault),
	})
	input := map[string]any{
		"mode":                 "setup",
		"customer":             stripeCustomerID,
		"payment_method_types": req.PaymentMethodTypes,
		"success_url":          successURL,
		"cancel_url":           cancelURL,
		"client_reference_id":  fmt.Sprintf("cust_%d_setup", cust.ID),
		"metadata":             metadata,
		"setup_intent_data": map[string]any{
			"metadata": metadata,
		},
	}
	var sess struct {
		ID          string `json:"id"`
		URL         string `json:"url"`
		Customer    string `json:"customer"`
		SetupIntent string `json:"setup_intent"`
		ExpiresAt   int64  `json:"expires_at"`
	}
	if stripeDirectConfigured(ctx) {
		if err := executeStripeDirect(ctx, http.MethodPost, "/checkout/sessions", input, &sess); err != nil {
			return nil, err
		}
	} else {
		if err := executeStripe(ctx, bound, "create_checkout_session", input, &sess); err != nil {
			return nil, err
		}
	}
	if sess.ID == "" || sess.URL == "" {
		return nil, errors.New("Stripe returned no setup session URL")
	}
	rawTypes, _ := json.Marshal(req.PaymentMethodTypes)
	rawMeta, _ := json.Marshal(metadata)
	return dbSetupSessionCreate(ctx.AppDB(), &SetupSession{
		ProjectID:             pid,
		CustomerID:            cust.ID,
		Provider:              "stripe",
		ProviderCustomerID:    stripeCustomerID,
		ProviderSessionID:     sess.ID,
		ProviderSetupIntentID: sess.SetupIntent,
		Status:                "pending",
		SuccessURL:            successURL,
		CancelURL:             cancelURL,
		URL:                   sess.URL,
		PaymentMethodTypes:    rawTypes,
		Metadata:              rawMeta,
		ExpiresAt:             timestampFromUnix(sess.ExpiresAt),
	})
}

func ensureStripeCustomer(ctx *sdk.AppCtx, pid string, cust *Customer, bound *sdk.BoundIntegration) (string, error) {
	if cust == nil {
		return "", errors.New("customer required")
	}
	if strings.TrimSpace(cust.ExternalID) != "" {
		return cust.ExternalID, nil
	}
	input := map[string]any{
		"email": cust.Email,
		"name":  cust.Name,
		"metadata": map[string]any{
			"apteva_project_id":  pid,
			"apteva_customer_id": fmt.Sprintf("%d", cust.ID),
		},
	}
	var out struct {
		ID string `json:"id"`
	}
	if stripeDirectConfigured(ctx) {
		if err := executeStripeDirect(ctx, http.MethodPost, "/customers", input, &out); err != nil {
			return "", err
		}
	} else {
		if bound == nil {
			return "", errors.New("payment_processor integration required")
		}
		if err := executeStripe(ctx, bound, "create_customer", input, &out); err != nil {
			return "", err
		}
	}
	if out.ID == "" {
		return "", errors.New("Stripe returned no customer id")
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE customers SET external_id = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ?`, out.ID, cust.ID, pid); err != nil {
		ctx.Logger().Warn("stripe customer id persistence failed", "customer_id", cust.ID, "err", err.Error())
	}
	return out.ID, nil
}

func parseStripeSignature(header string) (int64, []string) {
	var ts int64
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = atoi64(v)
		case "v1":
			sigs = append(sigs, v)
		}
	}
	return ts, sigs
}

func verifyStripeWebhookSignature(payload []byte, header, secret string, now time.Time) error {
	if secret == "" {
		return errors.New("stripe webhook signing secret is not configured")
	}
	ts, sigs := parseStripeSignature(header)
	if ts == 0 || len(sigs) == 0 {
		return errors.New("malformed Stripe-Signature header")
	}
	eventTime := time.Unix(ts, 0)
	if now.Sub(eventTime) > 5*time.Minute || eventTime.Sub(now) > 5*time.Minute {
		return errors.New("Stripe-Signature timestamp outside tolerance")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strconv.FormatInt(ts, 10)))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(payload)
	expected := mac.Sum(nil)
	for _, sig := range sigs {
		got, err := hex.DecodeString(sig)
		if err == nil && hmac.Equal(got, expected) {
			return nil
		}
	}
	return errors.New("Stripe-Signature HMAC mismatch")
}

// ─── Send payment link ──────────────────────────────────────────────

// toolInvoicesSendPaymentLink implements the v0.8.0 "agent shares a
// Stripe URL the customer pays at" flow. Creates a Stripe Checkout
// Session whose line items mirror our invoice, returns the hosted
// payment URL. The customer's eventual payment fires a
// checkout.session.completed webhook → handleStripeWebhook records
// the payment + transitions our invoice to 'paid' idempotently.
//
// Stripe Checkout Sessions have a 24h max lifetime. Calling this
// tool again on the same invoice creates a fresh session; the
// previous URL keeps working until its own expiry.
func (a *App) toolInvoicesSendPaymentLink(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "invoice_id")
	if id == 0 {
		return nil, errors.New("invoice_id required")
	}
	var bound *sdk.BoundIntegration
	if !stripeDirectConfigured(ctx) {
		var err error
		bound, err = requireProcessor(ctx)
		if err != nil {
			return nil, err
		}
	}

	inv, cust, err := loadInvoiceForRender(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if inv == nil {
		return nil, fmt.Errorf("invoice %d not found", id)
	}
	if inv.Status != "open" && inv.Status != "uncollectible" {
		return nil, fmt.Errorf("cannot send payment link for %s invoice — only 'open' or 'uncollectible' qualify", inv.Status)
	}
	if cust == nil || strings.TrimSpace(cust.Email) == "" {
		return nil, errors.New("invoice's customer has no email on file — set one via customers_update before sending a payment link")
	}
	if len(inv.LineItems) == 0 {
		return nil, errors.New("invoice has no line items")
	}

	amountDue := inv.TotalCents - inv.AmountPaidCents
	if amountDue <= 0 {
		return nil, errors.New("invoice has no outstanding balance")
	}
	// Checkout requires integer quantities, while Billing deliberately allows
	// fractional quantities and per-line tax. Charge one reconciled balance
	// line so Checkout's amount is exactly the invoice's outstanding amount.
	label := inv.Number
	if label == "" {
		label = fmt.Sprintf("Invoice #%d", inv.ID)
	} else {
		label = "Invoice " + label
	}
	lineItems := []map[string]any{{
		"price_data": map[string]any{
			"currency":     strings.ToLower(inv.Currency),
			"unit_amount":  amountDue,
			"product_data": map[string]any{"name": label},
		},
		"quantity": 1,
	}}

	successURL := strArg(args, "success_url")
	if successURL == "" {
		successURL = configString(ctx, "stripe_success_url", "")
	}
	if successURL == "" {
		// Sensible default — the dashboard's billing panel deep link
		// for this invoice. {CHECKOUT_SESSION_ID} is replaced by
		// Stripe at redirect time.
		successURL = stripePublicBaseURL(ctx) + fmt.Sprintf(
			"/dashboard?app=billing&invoice_id=%d&stripe_session={CHECKOUT_SESSION_ID}", inv.ID)
	}
	cancelURL := strArg(args, "cancel_url")
	if cancelURL == "" {
		cancelURL = configString(ctx, "stripe_cancel_url", "")
	}
	if cancelURL == "" {
		cancelURL = stripePublicBaseURL(ctx) + fmt.Sprintf("/dashboard?app=billing&invoice_id=%d", inv.ID)
	}
	for name, raw := range map[string]string{"success_url": successURL, "cancel_url": cancelURL} {
		u, err := neturl.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, fmt.Errorf("%s must be an absolute http(s) URL", name)
		}
	}

	// metadata.invoice_id is THE join key — the webhook handler reads
	// it back to find our invoice. project_id mirrors the integration
	// global-call convention so cross-project routing works.
	input := map[string]any{
		"mode":                "payment",
		"line_items":          lineItems,
		"customer_email":      cust.Email,
		"success_url":         successURL,
		"cancel_url":          cancelURL,
		"client_reference_id": fmt.Sprintf("inv_%d", inv.ID),
		"metadata": map[string]any{
			"apteva_invoice_id":     fmt.Sprintf("%d", inv.ID),
			"apteva_customer_id":    fmt.Sprintf("%d", inv.CustomerID),
			"apteva_project_id":     pid,
			"apteva_invoice_number": inv.Number,
		},
	}

	var sess struct {
		ID        string `json:"id"`
		URL       string `json:"url"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if stripeDirectConfigured(ctx) {
		if err := ensureStripeWebhook(ctx); err != nil {
			return nil, fmt.Errorf("stripe webhook setup: %w", err)
		}
		if err := executeStripeDirect(ctx, http.MethodPost, "/checkout/sessions", input, &sess); err != nil {
			return nil, err
		}
	} else {
		if err := executeStripe(ctx, bound, "create_checkout_session", input, &sess); err != nil {
			return nil, err
		}
	}
	if sess.URL == "" {
		return nil, errors.New("Stripe returned no payment URL")
	}

	// Persist the exact expected amount/currency before exposing the URL. The
	// webhook resolves this row by the signed session id rather than trusting
	// caller-controlled metadata from an arbitrary event in the Stripe account.
	now := nowRFC3339()
	expiresAt := ""
	if sess.ExpiresAt > 0 {
		expiresAt = time.Unix(sess.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO billing_checkout_sessions
		   (project_id, invoice_id, provider, provider_session_id,
		    amount_cents, currency, status, expires_at, created_at)
		 VALUES (?, ?, 'stripe', ?, ?, ?, 'pending', ?, ?)`,
		pid, inv.ID, sess.ID, amountDue, inv.Currency,
		nullStr(expiresAt), now); err != nil {
		return nil, fmt.Errorf("persist checkout session: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE invoices
		 SET external_id = ?, external_url = ?, last_synced_at = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ?`,
		sess.ID, sess.URL, now, inv.ID, pid); err != nil {
		return nil, fmt.Errorf("persist payment link: %w", err)
	}
	if err := writeAuditTx(tx, inv.ID, callerActor(args), "payment_link_sent", map[string]any{
		"stripe_session_id": sess.ID,
		"stripe_url":        sess.URL,
		"amount_cents":      amountDue,
		"currency":          inv.Currency,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return map[string]any{
		"url":               sess.URL,
		"stripe_session_id": sess.ID,
		"expires_at":        sess.ExpiresAt,
	}, nil
}

// ─── Webhook handler ────────────────────────────────────────────────

// handleStripeWebhook is the public POST /webhooks/stripe endpoint.
// Stripe POSTs here after a payment event. We forward the raw body
// + the Stripe-Signature header to the integration's process_webhook
// tool, which verifies the signature using the webhookSecret in the
// connection credentials. On success, dispatches on event type.
//
// Idempotency is handled by dbPaymentRecord — the (method,
// external_id) unique index on payments rejects duplicates cleanly,
// and dbPaymentRecord pre-checks for the existing row so re-deliveries
// return 200 OK without side effects.
func (a *App) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	signature := r.Header.Get("Stripe-Signature")
	if signature == "" {
		httpErr(w, http.StatusBadRequest, "missing Stripe-Signature header")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpErr(w, http.StatusRequestEntityTooLarge, "webhook body exceeds 1 MiB")
		return
	}

	// Verify + parse via the integration's process_webhook tool.
	// process_webhook returns the canonical Stripe Event payload
	// on success; signature failure returns success=false.
	var event struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if stripeDirectConfigured(ctx) {
		settings, err := loadStripeSettings(ctx.AppDB())
		if err != nil {
			httpErr(w, http.StatusInternalServerError, "load stripe settings: "+err.Error())
			return
		}
		if settings.WebhookSecret == "" {
			if err := ensureStripeWebhook(ctx); err != nil {
				httpErr(w, http.StatusServiceUnavailable, "stripe webhook is not configured: "+err.Error())
				return
			}
			settings, _ = loadStripeSettings(ctx.AppDB())
		}
		if err := verifyStripeWebhookSignature(body, signature, settings.WebhookSecret, time.Now().UTC()); err != nil {
			httpErr(w, http.StatusBadRequest, "webhook verification failed: "+err.Error())
			return
		}
		if err := json.Unmarshal(body, &event); err != nil {
			httpErr(w, http.StatusBadRequest, "decode webhook payload: "+err.Error())
			return
		}
	} else {
		bound := ctx.IntegrationFor("payment_processor")
		if bound == nil {
			httpErr(w, http.StatusServiceUnavailable, "no payment_processor integration bound")
			return
		}
		if err := executeStripe(ctx, bound, "process_webhook", map[string]any{
			"payload":   string(body),
			"signature": signature,
		}, &event); err != nil {
			httpErr(w, http.StatusBadRequest, "webhook verification failed: "+err.Error())
			return
		}
	}

	if err := a.dispatchStripeEvent(ctx, event.ID, event.Type, event.Data.Object); err != nil {
		ctx.Logger().Warn("stripe webhook dispatch failed",
			"event_id", event.ID, "type", event.Type, "err", err.Error())
		httpErr(w, http.StatusInternalServerError, "webhook processing failed")
		return
	}

	httpJSON(w, map[string]any{"received": true, "event_id": event.ID})
}

// dispatchStripeEvent routes a verified Stripe event to the right
// handler. Unhandled event types log a notice and return nil — we
// only care about a small set of money-movement events.
func (a *App) dispatchStripeEvent(ctx *sdk.AppCtx, eventID, eventType string, obj json.RawMessage) error {
	switch eventType {
	case "checkout.session.completed":
		return a.handleCheckoutCompleted(ctx, obj)
	case "payment_intent.succeeded":
		// Reserved for non-Checkout flows (direct PaymentIntent
		// creation). Not used by v0.8.0's send-payment-link path
		// but the catalog declares the event, so handle gracefully.
		return nil
	case "setup_intent.succeeded":
		return a.handleSetupIntentSucceeded(ctx, obj)
	case "setup_intent.setup_failed":
		return a.handleSetupIntentFailed(ctx, obj)
	case "payment_method.detached":
		return a.handlePaymentMethodDetached(ctx, obj)
	case "charge.refunded":
		return a.handleChargeRefunded(ctx, obj)
	case "invoice.paid":
		// Stripe-mirrored invoice flow (future v0.9.0). Not used
		// by v0.8.0 — our invoices live in our DB, not Stripe.
		return nil
	default:
		ctx.Logger().Info("stripe event not handled by billing", "type", eventType, "event_id", eventID)
		return nil
	}
}

// handleCheckoutCompleted is the payment-confirmation path. The
// Stripe checkout.session.completed event fires when the customer
// finishes paying. We look up our invoice by the metadata key we
// set when creating the session, then record a method='stripe'
// payment with external_id=payment_intent_id. dbPaymentRecord is
// idempotent on (method, external_id), so webhook re-delivery is a
// no-op.
func (a *App) handleCheckoutCompleted(ctx *sdk.AppCtx, obj json.RawMessage) error {
	var sess struct {
		ID                string            `json:"id"`
		AmountTotal       int64             `json:"amount_total"`
		Currency          string            `json:"currency"`
		Mode              string            `json:"mode"`
		PaymentIntent     string            `json:"payment_intent"`
		PaymentStatus     string            `json:"payment_status"`
		SetupIntent       string            `json:"setup_intent"`
		ClientReferenceID string            `json:"client_reference_id"`
		Customer          string            `json:"customer"`
		CustomerEmail     string            `json:"customer_email"`
		Metadata          map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(obj, &sess); err != nil {
		return fmt.Errorf("decode session: %w", err)
	}
	if sess.Mode == "setup" {
		if err := dbSetupSessionCompleteByProviderSession(ctx.AppDB(), "stripe", sess.ID, sess.SetupIntent); err != nil {
			return fmt.Errorf("complete setup session: %w", err)
		}
		return nil
	}
	if sess.PaymentStatus != "paid" {
		// async_payment_succeeded would be the followup for async
		// methods (SEPA, etc.). Skip until that lands.
		ctx.Logger().Info("checkout.session.completed but payment_status not 'paid'",
			"session", sess.ID, "status", sess.PaymentStatus)
		return nil
	}

	var pid, expectedCurrency, sessionStatus string
	var invoiceID, expectedAmount int64
	if err := ctx.AppDB().QueryRow(
		`SELECT project_id, invoice_id, amount_cents, currency, status
		 FROM billing_checkout_sessions
		 WHERE provider = 'stripe' AND provider_session_id = ?`, sess.ID).
		Scan(&pid, &invoiceID, &expectedAmount, &expectedCurrency, &sessionStatus); err != nil {
		return fmt.Errorf("unrecognized checkout session %s: %w", sess.ID, err)
	}
	if sess.AmountTotal != expectedAmount {
		return fmt.Errorf("checkout session %s amount mismatch: got %d want %d", sess.ID, sess.AmountTotal, expectedAmount)
	}
	if !strings.EqualFold(sess.Currency, expectedCurrency) {
		return fmt.Errorf("checkout session %s currency mismatch: got %s want %s", sess.ID, sess.Currency, expectedCurrency)
	}

	externalID := sess.PaymentIntent
	if externalID == "" {
		externalID = sess.ID // fall back to checkout session id
	}

	now := time.Now().UTC().Format(time.RFC3339)
	pay, inv, err := dbPaymentRecord(ctx.AppDB(), pid, invoiceID, sess.AmountTotal,
		"stripe", externalID, now,
		fmt.Sprintf("Stripe Checkout Session %s", sess.ID),
		"system:stripe-webhook")
	if err != nil {
		return fmt.Errorf("record payment: %w", err)
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE billing_checkout_sessions
		 SET status = 'completed', completed_at = COALESCE(completed_at, ?)
		 WHERE provider = 'stripe' AND provider_session_id = ?`, now, sess.ID); err != nil {
		return fmt.Errorf("complete checkout session: %w", err)
	}
	emitInvoice(ctx, "invoice.paid", inv)
	ctx.Logger().Info("stripe payment recorded",
		"invoice_id", invoiceID, "payment_id", pay.ID, "amount", sess.AmountTotal, "session", sess.ID)
	return nil
}

func (a *App) handleSetupIntentSucceeded(ctx *sdk.AppCtx, obj json.RawMessage) error {
	var si struct {
		ID                 string            `json:"id"`
		Status             string            `json:"status"`
		Customer           string            `json:"customer"`
		PaymentMethod      string            `json:"payment_method"`
		Mandate            string            `json:"mandate"`
		PaymentMethodTypes []string          `json:"payment_method_types"`
		Metadata           map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(obj, &si); err != nil {
		return fmt.Errorf("decode setup_intent: %w", err)
	}
	if si.Status != "" && si.Status != "succeeded" {
		return nil
	}
	pid := si.Metadata["apteva_project_id"]
	customerID := atoi64(si.Metadata["apteva_customer_id"])
	if pid == "" || customerID == 0 {
		return fmt.Errorf("setup_intent %s missing apteva project/customer metadata", si.ID)
	}
	if si.PaymentMethod == "" {
		return fmt.Errorf("setup_intent %s has no payment_method", si.ID)
	}
	pm := &PaymentMethod{
		ProjectID:               pid,
		CustomerID:              customerID,
		Provider:                "stripe",
		ProviderCustomerID:      si.Customer,
		ProviderPaymentMethodID: si.PaymentMethod,
		ProviderMandateID:       si.Mandate,
		Type:                    firstNonEmpty(si.PaymentMethodTypes, "unknown"),
		Status:                  "active",
		IsDefault:               si.Metadata["apteva_set_default"] != "false",
		Reusable:                true,
		Metadata:                json.RawMessage(`{}`),
	}
	if stripeDirectConfigured(ctx) {
		if fetched, err := fetchStripePaymentMethod(ctx, si.PaymentMethod); err == nil && fetched != nil {
			pm.ProviderCustomerID = firstString(pm.ProviderCustomerID, fetched.ProviderCustomerID)
			pm.Type = firstString(fetched.Type, pm.Type)
			pm.DisplayBrand = fetched.DisplayBrand
			pm.DisplayLast4 = fetched.DisplayLast4
			pm.ExpMonth = fetched.ExpMonth
			pm.ExpYear = fetched.ExpYear
			pm.Country = fetched.Country
			pm.Currency = fetched.Currency
			pm.DelayedNotification = fetched.DelayedNotification
		} else if err != nil {
			ctx.Logger().Warn("stripe payment method fetch failed", "payment_method", si.PaymentMethod, "err", err.Error())
		}
	}
	if pm.Type == "sepa_debit" {
		pm.DelayedNotification = true
	}
	rawMeta, _ := json.Marshal(mergeMaps(mapFromStringMap(si.Metadata), map[string]any{
		"stripe_setup_intent_id": si.ID,
	}))
	pm.Metadata = rawMeta
	created, err := dbPaymentMethodUpsert(ctx.AppDB(), pm)
	if err != nil {
		return fmt.Errorf("save payment method: %w", err)
	}
	emitPaymentMethod(ctx, "payment_method.attached", created)
	return nil
}

func (a *App) handleSetupIntentFailed(ctx *sdk.AppCtx, obj json.RawMessage) error {
	var si struct {
		ID       string            `json:"id"`
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(obj, &si); err != nil {
		return fmt.Errorf("decode setup_intent: %w", err)
	}
	if si.ID != "" {
		return dbSetupSessionFailBySetupIntent(ctx.AppDB(), "stripe", si.ID)
	}
	return nil
}

func (a *App) handlePaymentMethodDetached(ctx *sdk.AppCtx, obj json.RawMessage) error {
	var raw struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(obj, &raw); err != nil {
		return fmt.Errorf("decode payment_method: %w", err)
	}
	if raw.ID == "" {
		return nil
	}
	pm, err := dbPaymentMethodByProviderID(ctx.AppDB(), "stripe", raw.ID)
	if err != nil || pm == nil {
		return err
	}
	detached, err := dbPaymentMethodDetach(ctx.AppDB(), pm.ProjectID, pm.ID)
	if err != nil {
		return err
	}
	emitPaymentMethod(ctx, "payment_method.detached", detached)
	return nil
}

func fetchStripePaymentMethod(ctx *sdk.AppCtx, providerPaymentMethodID string) (*PaymentMethod, error) {
	var raw struct {
		ID       string            `json:"id"`
		Type     string            `json:"type"`
		Customer string            `json:"customer"`
		Metadata map[string]string `json:"metadata"`
		Card     struct {
			Brand    string `json:"brand"`
			Last4    string `json:"last4"`
			ExpMonth int    `json:"exp_month"`
			ExpYear  int    `json:"exp_year"`
			Country  string `json:"country"`
		} `json:"card"`
		SepaDebit struct {
			Last4   string `json:"last4"`
			Country string `json:"country"`
		} `json:"sepa_debit"`
		USBankAccount struct {
			BankName string `json:"bank_name"`
			Last4    string `json:"last4"`
		} `json:"us_bank_account"`
	}
	if err := executeStripeDirect(ctx, http.MethodGet, "/payment_methods/"+providerPaymentMethodID, map[string]any{}, &raw); err != nil {
		return nil, err
	}
	pm := &PaymentMethod{
		Provider:                "stripe",
		ProviderCustomerID:      raw.Customer,
		ProviderPaymentMethodID: raw.ID,
		Type:                    firstString(raw.Type, "unknown"),
		Reusable:                true,
	}
	switch raw.Type {
	case "card":
		pm.DisplayBrand = raw.Card.Brand
		pm.DisplayLast4 = raw.Card.Last4
		pm.ExpMonth = raw.Card.ExpMonth
		pm.ExpYear = raw.Card.ExpYear
		pm.Country = raw.Card.Country
	case "sepa_debit":
		pm.DisplayBrand = "SEPA Direct Debit"
		pm.DisplayLast4 = raw.SepaDebit.Last4
		pm.Country = raw.SepaDebit.Country
		pm.DelayedNotification = true
	case "us_bank_account":
		pm.DisplayBrand = raw.USBankAccount.BankName
		pm.DisplayLast4 = raw.USBankAccount.Last4
		pm.DelayedNotification = true
	}
	return pm, nil
}

// handleChargeRefunded records a negative-amount payment row for a
// refund. Stripe's charge.refunded event fires with the refund
// amount in `amount_refunded` (cumulative). We record the DELTA
// from the previous refund, but for v0.8.0 simplicity we just record
// the full amount_refunded once with idempotency on the charge id.
func (a *App) handleChargeRefunded(ctx *sdk.AppCtx, obj json.RawMessage) error {
	var charge struct {
		ID             string            `json:"id"`
		PaymentIntent  string            `json:"payment_intent"`
		Amount         int64             `json:"amount"`
		AmountRefunded int64             `json:"amount_refunded"`
		Currency       string            `json:"currency"`
		Metadata       map[string]string `json:"metadata"`
		Refunds        struct {
			Data []struct {
				ID     string `json:"id"`
				Amount int64  `json:"amount"`
				Status string `json:"status"`
			} `json:"data"`
		} `json:"refunds"`
	}
	if err := json.Unmarshal(obj, &charge); err != nil {
		return fmt.Errorf("decode charge: %w", err)
	}
	if charge.AmountRefunded <= 0 {
		return nil
	}
	// Find invoice via payment_intent — we recorded the PI as the
	// payment's external_id, so look it up.
	if charge.PaymentIntent == "" {
		return fmt.Errorf("charge %s has no payment_intent", charge.ID)
	}
	var invoiceID int64
	var pid, paymentCurrency string
	if err := ctx.AppDB().QueryRow(
		`SELECT invoice_id, project_id, currency FROM payments
		 WHERE method = 'stripe' AND external_id = ?
		 LIMIT 1`,
		charge.PaymentIntent).Scan(&invoiceID, &pid, &paymentCurrency); err != nil {
		return fmt.Errorf("no payment found for payment_intent %s: %w", charge.PaymentIntent, err)
	}
	if charge.Currency != "" && !strings.EqualFold(charge.Currency, paymentCurrency) {
		return fmt.Errorf("refund currency mismatch: got %s want %s", charge.Currency, paymentCurrency)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var inv *Invoice
	recordedIndividual := false
	for _, refund := range charge.Refunds.Data {
		if refund.ID == "" || refund.Amount <= 0 || (refund.Status != "" && refund.Status != "succeeded") {
			continue
		}
		recordedIndividual = true
		refundExternalID := charge.ID + ":refund:" + refund.ID
		_, current, err := dbPaymentRecord(ctx.AppDB(), pid, invoiceID, -refund.Amount,
			"stripe", refundExternalID, now,
			fmt.Sprintf("Stripe refund %s on %s", refund.ID, charge.ID),
			"system:stripe-webhook")
		if err != nil {
			return fmt.Errorf("record refund %s: %w", refund.ID, err)
		}
		inv = current
	}
	if !recordedIndividual {
		// Some API versions omit the expanded refunds list. Reconcile the
		// cumulative amount_refunded against refund rows already stored for
		// this charge and record only the delta.
		var already int64
		if err := ctx.AppDB().QueryRow(
			`SELECT COALESCE(-SUM(amount_cents), 0) FROM payments
			 WHERE method = 'stripe' AND project_id = ? AND invoice_id = ?
			   AND (external_id = ? OR external_id LIKE ?)`,
			pid, invoiceID, charge.ID+":refund", charge.ID+":refund:%").Scan(&already); err != nil {
			return err
		}
		delta := charge.AmountRefunded - already
		if delta <= 0 {
			return nil
		}
		extID := fmt.Sprintf("%s:refund:%d", charge.ID, charge.AmountRefunded)
		_, current, err := dbPaymentRecord(ctx.AppDB(), pid, invoiceID, -delta,
			"stripe", extID, now,
			fmt.Sprintf("Stripe cumulative refund on %s", charge.ID),
			"system:stripe-webhook")
		if err != nil {
			return fmt.Errorf("record refund: %w", err)
		}
		inv = current
	}
	emitInvoice(ctx, "invoice.refunded", inv)
	return nil
}
