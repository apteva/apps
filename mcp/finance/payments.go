package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
)

// Payment requests contain no bank credentials. They are immutable once prepared.
// No bank write uses the read-side retry helper, and no response books a ledger
// entry: only bank sync establishes that money actually moved.
type bankPaymentRequest struct {
	ConnectionID      int64  `json:"connection_id"`
	Provider          string `json:"provider"`
	Mode              string `json:"mode"`
	AccountID         int64  `json:"account_id,omitempty"`
	ExternalAccountID string `json:"external_account_id,omitempty"`
	Amount            int64  `json:"amount"`
	Currency          string `json:"currency"`
	RecipientName     string `json:"recipient_name"`
	RecipientAddress  string `json:"recipient_address,omitempty"`
	RecipientType     string `json:"recipient_type,omitempty"`
	RecipientID       string `json:"recipient_id,omitempty"`
	UserID            string `json:"user_id,omitempty"`
	Reference         string `json:"reference"`
	Direction         string `json:"direction,omitempty"`
	Network           string `json:"network,omitempty"`
	ACHClass          string `json:"ach_class,omitempty"`
	LegalName         string `json:"legal_name,omitempty"`
	Country           string `json:"country,omitempty"`
	BankName          string `json:"bank_name,omitempty"`
	PaymentType       string `json:"payment_type,omitempty"`
	PSUType           string `json:"psu_type,omitempty"`
	RedirectURL       string `json:"redirect_url,omitempty"`
	Deferred          bool   `json:"deferred,omitempty"`
	DebtorIBAN        string `json:"debtor_iban,omitempty"`
	CustomerID        string `json:"customer_id,omitempty"`
	CustomerIP        string `json:"customer_ip,omitempty"`
	Email             string `json:"email,omitempty"`
	SortCode          string `json:"sort_code,omitempty"`
	AccountNumber     string `json:"account_number,omitempty"`
}

type bankPayment struct {
	ID               string             `json:"id"`
	Revision         int64              `json:"-"`
	CallbackState    string             `json:"-"`
	CallbackVerified bool               `json:"-"`
	Continued        bool               `json:"continued"`
	Request          bankPaymentRequest `json:"request"`
	State            string             `json:"state"`
	ProviderID       string             `json:"provider_id"`
	ProviderStatus   string             `json:"provider_status"`
	Response         map[string]any     `json:"response"`
	Error            string             `json:"error,omitempty"`
	CreatedAt        string             `json:"created_at"`
}

func bankingPaymentCapabilities() map[string]any {
	return map[string]any{
		"plaid":              map[string]any{"modes": []string{"payment", "ach"}, "note": "UK/EU payment initiation requires a Plaid recipient/user and bank authorization in Hosted Link. US ACH transfers require the Transfer product and a linked USD account; debit pulls from that account, credit pays it from your Plaid funding account."},
		"teller":             map[string]any{"modes": []string{"payment"}, "note": "US Zelle payments where the institution supports Teller's beta payments API. Teller Connect may require MFA."},
		"enable-banking":     map[string]any{"modes": []string{"payment"}, "note": "Bank payments with bank authorization; deferred execution is available where the chosen bank supports it."},
		"truelayer-payments": map[string]any{"modes": []string{"payment"}, "note": "EUR/GBP bank payments with TrueLayer's hosted bank selection and consent page."},
		"saltedge-payments":  map[string]any{"modes": []string{"payment"}, "note": "SEPA payments through Salt Edge Widget; requires a PIS customer and supported bank/template."},
		"nordigen":           map[string]any{"modes": []string{}, "note": "The configured GoCardless Bank Account Data API provides account data, not payment initiation. A separate payment service is required."},
		"truelayer":          map[string]any{"modes": []string{}, "note": "This connection uses the Data API. Payments v3 needs separate payments credentials and Tl-Signature request signing."},
		"saltedge":           map[string]any{"modes": []string{}, "note": "This connection uses the Account Information API. Salt Edge Payment Initiation needs a separate integration and payment consent flow."},
	}
}

func (a *App) bankingPaymentTools() []sdk.Tool {
	text := func() any { return map[string]any{"type": "string"} }
	fields := map[string]any{
		"connection_id": map[string]any{"type": "integer"}, "request_key": text(),
		"mode":       map[string]any{"type": "string", "enum": []string{"payment", "ach"}},
		"account_id": map[string]any{"type": "integer"},
		"amount":     map[string]any{"type": "integer", "minimum": 1, "description": "Exact minor units (1234 = 12.34)."},
		"currency":   text(), "recipient_name": text(), "recipient_address": text(), "recipient_type": text(),
		"recipient_id": text(), "user_id": text(), "reference": text(), "direction": text(),
		"network": text(), "ach_class": text(), "legal_name": text(), "country": text(),
		"bank_name": text(), "payment_type": text(), "psu_type": text(), "redirect_url": text(), "deferred": map[string]any{"type": "boolean"},
		"debtor_iban": text(), "customer_id": text(), "customer_ip": text(), "email": text(), "sort_code": text(), "account_number": text(),
	}
	idSchema := schemaObject(map[string]any{"id": text()}, []string{"id"})
	return []sdk.Tool{
		{Name: "banking_payment_events", Description: "Sync Plaid Transfer events from a durable cursor and refresh matching payment statuses. Read-only provider calls; never moves money.", InputSchema: schemaObject(map[string]any{"connection_id": map[string]any{"type": "integer"}}, []string{"connection_id"}), Handler: a.toolBankingPaymentEvents},
		{Name: "banking_payment_options", Description: "Read bank payment capabilities, payment templates or existing Plaid recipients. No payment is created.", InputSchema: schemaObject(map[string]any{"connection_id": map[string]any{"type": "integer"}, "account_id": map[string]any{"type": "integer"}, "country": text(), "psu_type": text(), "cursor": text(), "template_identifier": text()}, []string{"connection_id"}), Handler: a.toolBankingPaymentOptions},
		{Name: "banking_payment_continue", Description: "Explicitly continue the reviewed payment after bank authorization, with confirmed=true. Enable Banking additionally requires a verified callback URL. Never replay uncertain execution.", InputSchema: schemaObject(map[string]any{"id": text(), "confirmed": map[string]any{"type": "boolean"}}, []string{"id", "confirmed"}), Handler: a.toolBankingPaymentContinue},
		{Name: "banking_payment_callback", Description: "Verify the complete bank return URL state for this payment. This does not establish settlement or execute a payment.", InputSchema: schemaObject(map[string]any{"id": text(), "return_url": text()}, []string{"id", "return_url"}), Handler: a.toolBankingPaymentCallback},
		{Name: "banking_payment_prepare", Description: "Prepare an immutable real bank payment for review, without issuing provider writes. Use a stable request_key to prevent duplicate drafts. Enable Banking: bank_name/country, SEPA or INST_SEPA payment_type, recipient_name/IBAN in recipient_address, redirect_url, optional debtor_iban/deferred. TrueLayer Payments: EUR IBAN or GBP sort_code/account_number, payer legal_name/email, redirect_url. Salt Edge Payments: bank_name (provider code), customer_id, actual customer_ip, debtor_iban, recipient IBAN, redirect_url. Teller: linked USD account_id, recipient_name/address (Zelle email or phone). Plaid payment: existing recipient_id, recipient_name, user_id, country GB or supported EUR country. Plaid ACH: linked USD account_id, direction debit (pull) or credit (push), network ach/same-day-ach, ach_class, legal_name. Review the returned exact amount, recipient and direction before submitting.", InputSchema: schemaObject(fields, []string{"connection_id", "request_key", "amount", "currency", "reference"}), Handler: a.toolBankingPaymentPrepare},
		{Name: "banking_payment_submit", Description: "Submit the reviewed payment ID to the bank provider. This can move real money. Requires confirmed=true after authorization for these exact payment details. Duplicate submission never replays a bank write; an unknown/submitting result requires provider reconciliation. Completion of bank MFA may still be required.", InputSchema: schemaObject(map[string]any{"id": text(), "confirmed": map[string]any{"type": "boolean"}}, []string{"id", "confirmed"}), Handler: a.toolBankingPaymentSubmit},
		{Name: "banking_payment_get", Description: "Read a payment request; refresh=true fetches its provider status without resubmitting it. Provider status does not itself book a ledger transaction. If an uncertain/MFA submission has no provider ID, supply provider_id from provider records to reconcile; its details must match the reviewed request.", InputSchema: schemaObject(map[string]any{"id": text(), "refresh": map[string]any{"type": "boolean"}, "provider_id": text()}, []string{"id"}), Handler: a.toolBankingPaymentGet},
		{Name: "banking_payments_list", Description: "List the latest 100 payment requests in this project, including incomplete and uncertain submissions.", InputSchema: schemaObject(map[string]any{}, nil), Handler: a.toolBankingPaymentsList},
		{Name: "banking_payment_authorize", Description: "Get the bank authorization handoff for an already submitted payment. European providers and Plaid return hosted URLs; Teller returns connect_token. Complete bank authorization, then refresh status or explicitly continue deferred/ACH requests. Never create another payment for the same authorization.", InputSchema: idSchema, Handler: a.toolBankingPaymentAuthorize},
		{Name: "banking_payment_cancel", Description: "Cancel a local draft, or request cancellation of an eligible Plaid ACH transfer with confirmed=true. The bank decides eligibility.", InputSchema: schemaObject(map[string]any{"id": text(), "confirmed": map[string]any{"type": "boolean"}}, []string{"id"}), Handler: a.toolBankingPaymentCancel},
	}
}

func exactPositiveInteger(v any) (int64, error) {
	if f, ok := v.(float64); ok {
		if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || f <= 0 || f > 9007199254740991 {
			return 0, errors.New("expected a positive exact integer")
		}
		return int64(f), nil
	}
	n, err := strconv.ParseInt(fmt.Sprint(v), 10, 64)
	if err != nil || n <= 0 || n > 9007199254740991 {
		return 0, errors.New("expected a positive exact integer")
	}
	return n, nil
}

func paymentConnection(ctx *sdk.AppCtx, id int64) (sdk.PlatformConnection, string, error) {
	c, p, err := bankingConnection(ctx, "", id)
	if err != nil {
		return c, p, err
	}
	if c.ProjectID != "" && c.ProjectID != projectID(ctx) {
		return c, p, errors.New("connection belongs to another project")
	}
	if c.Status != "" && c.Status != "active" && c.Status != "connected" {
		return c, p, errors.New("connection is not active")
	}
	return c, p, nil
}

func (a *App) toolBankingPaymentPrepare(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	key := strings.TrimSpace(strArg(args, "request_key", ""))
	if key == "" || len(key) > 128 {
		return nil, errors.New("request_key required (maximum 128 characters)")
	}
	amount, err := exactPositiveInteger(args["amount"])
	if err != nil {
		return nil, fmt.Errorf("amount: %w", err)
	}
	cid, err := exactPositiveInteger(args["connection_id"])
	if err != nil {
		return nil, fmt.Errorf("connection_id: %w", err)
	}
	_, provider, err := paymentConnection(ctx, cid)
	if err != nil {
		return nil, err
	}
	if !isPaymentProvider(provider) {
		return nil, fmt.Errorf("%s payment writes are unavailable with the configured data integration", provider)
	}
	r := bankPaymentRequest{ConnectionID: cid, Provider: provider, Mode: strArg(args, "mode", "payment"), Amount: amount,
		Currency: strings.ToUpper(strArg(args, "currency", "")), RecipientName: strings.TrimSpace(strArg(args, "recipient_name", "")),
		RecipientAddress: strings.TrimSpace(strArg(args, "recipient_address", "")), RecipientType: strArg(args, "recipient_type", "person"),
		RecipientID: strArg(args, "recipient_id", ""), UserID: strArg(args, "user_id", ""), Reference: strings.TrimSpace(strArg(args, "reference", "")),
		Direction: strArg(args, "direction", ""), Network: strArg(args, "network", "ach"), ACHClass: strArg(args, "ach_class", ""), LegalName: strings.TrimSpace(strArg(args, "legal_name", "")), Country: strings.ToUpper(strArg(args, "country", ""))}
	if r.Reference == "" {
		return nil, errors.New("reference required")
	}
	if r.Mode != "payment" && r.Mode != "ach" {
		return nil, errors.New("mode must be payment or ach")
	}
	if provider == "teller" || r.Mode == "ach" {
		r.AccountID, err = exactPositiveInteger(args["account_id"])
		if err != nil {
			return nil, fmt.Errorf("account_id: %w", err)
		}
		links, e := bankingLinks(ctx, provider, cid, r.AccountID)
		if e != nil {
			return nil, e
		}
		if len(links) != 1 || links[0].Account.Archived || links[0].Account.Kind != "cash" {
			return nil, errors.New("exactly one active linked cash account required")
		}
		if r.Currency != "USD" || links[0].Account.Currency != "USD" {
			return nil, errors.New("Teller payments and Plaid ACH require a linked USD account")
		}
		r.ExternalAccountID = links[0].ExternalID
	}
	if isEuropeanPaymentProvider(provider) {
		if err := prepareEuropeanPayment(ctx, args, &r); err != nil {
			return nil, err
		}
	} else if provider == "teller" {
		if r.Mode != "payment" {
			return nil, errors.New("Teller supports payment mode only")
		}
		if r.RecipientName == "" || r.RecipientAddress == "" {
			return nil, errors.New("recipient_name and Zelle recipient_address required")
		}
		if r.RecipientType != "person" && r.RecipientType != "business" {
			return nil, errors.New("recipient_type must be person or business")
		}
	} else if r.Mode == "payment" {
		if r.Currency != "EUR" && r.Currency != "GBP" {
			return nil, errors.New("Plaid payment mode currently supports EUR and GBP")
		}
		if r.RecipientID == "" || r.UserID == "" || r.RecipientName == "" {
			return nil, errors.New("Plaid recipient_id, recipient_name and user_id required; create the recipient/user with the Plaid integration first")
		}
		if len(r.Reference) > 18 {
			return nil, errors.New("payment reference maximum 18 characters")
		}
		countries := " AT BE CY DE EE ES FI FR GR HR IE IT LT LU LV MT NL PT SI SK "
		if (r.Currency == "GBP" && r.Country != "GB") || (r.Currency == "EUR" && !strings.Contains(countries, " "+r.Country+" ")) {
			return nil, errors.New("country must match a supported payment market: GB for GBP, a supported euro-area country for EUR")
		}
		var recipient map[string]any
		if err := executeIntegrationJSON(ctx, cid, "get_payment_recipient", map[string]any{"recipient_id": r.RecipientID}, &recipient); err != nil {
			return nil, err
		}
		if firstString(recipient, "recipient_id") != r.RecipientID || firstString(recipient, "name") != r.RecipientName {
			return nil, errors.New("recipient ID/name does not match Plaid's registered recipient")
		}
		r.RecipientAddress = firstString(recipient, "iban")
		if r.RecipientAddress == "" {
			r.SortCode = firstString(recipient, "bacs.sort_code")
			r.AccountNumber = firstString(recipient, "bacs.account")
		}
	} else {
		if r.Direction != "debit" && r.Direction != "credit" {
			return nil, errors.New("direction must be debit (pull from bank) or credit (pay bank from Plaid funding account)")
		}
		if r.Network != "ach" && r.Network != "same-day-ach" {
			return nil, errors.New("network must be ach or same-day-ach")
		}
		if r.ACHClass != "ppd" && r.ACHClass != "ccd" && r.ACHClass != "web" {
			return nil, errors.New("ach_class must be ppd, ccd or web")
		}
		if r.LegalName == "" || len(r.Reference) > 10 {
			return nil, errors.New("legal_name required; ACH reference maximum 10 characters")
		}
	}
	body, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	_, err = ctx.AppDB().Exec(`INSERT INTO bank_payments(id,project_id,request_key,request_json,callback_state) VALUES(?,?,?,?,?) ON CONFLICT(project_id,request_key) DO NOTHING`, id, projectID(ctx), key, string(body), uuid.NewString())
	if err != nil {
		return nil, err
	}
	var stored string
	err = ctx.AppDB().QueryRow(`SELECT id,request_json FROM bank_payments WHERE project_id=? AND request_key=?`, projectID(ctx), key).Scan(&id, &stored)
	if err != nil {
		return nil, err
	}
	if stored != string(body) {
		return nil, errors.New("request_key already used for different payment details")
	}
	return readBankPayment(ctx, id)
}

func readBankPayment(ctx *sdk.AppCtx, id string) (bankPayment, error) {
	var p bankPayment
	var req, resp string
	err := ctx.AppDB().QueryRow(`SELECT id,request_json,state,provider_id,provider_status,response_json,error,created_at,revision,callback_state,callback_verified,continued FROM bank_payments WHERE id=? AND project_id=?`, id, projectID(ctx)).Scan(&p.ID, &req, &p.State, &p.ProviderID, &p.ProviderStatus, &resp, &p.Error, &p.CreatedAt, &p.Revision, &p.CallbackState, &p.CallbackVerified, &p.Continued)
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(req), &p.Request); err != nil {
		return p, err
	}
	err = json.Unmarshal([]byte(resp), &p.Response)
	return p, err
}

func saveBankPayment(ctx *sdk.AppCtx, p bankPayment) (bankPayment, error) {
	body, err := json.Marshal(p.Response)
	if err != nil {
		return p, err
	}
	res, err := ctx.AppDB().Exec(`UPDATE bank_payments SET state=?,provider_id=?,provider_status=?,response_json=?,error=?,callback_verified=?,continued=?,revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND revision=?`, p.State, p.ProviderID, p.ProviderStatus, string(body), p.Error, p.CallbackVerified, p.Continued, p.ID, projectID(ctx), p.Revision)
	if err != nil {
		return p, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return p, err
	}
	if n != 1 {
		return readBankPayment(ctx, p.ID)
	}
	p.Revision++
	return p, nil
}

func (a *App) toolBankingPaymentSubmit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if !boolArg(args, "confirmed", false) {
		return nil, errors.New("review the exact payment details and set confirmed=true to submit")
	}
	p, err := readBankPayment(ctx, strArg(args, "id", ""))
	if err != nil {
		return nil, err
	}
	if p.State != "draft" {
		return p, nil
	}
	created, e := time.Parse("2006-01-02 15:04:05", p.CreatedAt)
	if e != nil || time.Since(created) > 30*time.Minute {
		return nil, errors.New("payment draft expired; cancel it and prepare a new request for review")
	}
	_, provider, err := paymentConnection(ctx, p.Request.ConnectionID)
	if err != nil {
		return nil, err
	}
	if provider != p.Request.Provider {
		return nil, errors.New("connection provider changed; prepare a new request")
	}
	// Validate the source again before reserving submission. Never follow a relink.
	var token string
	if p.Request.AccountID != 0 {
		links, e := bankingLinks(ctx, provider, p.Request.ConnectionID, p.Request.AccountID)
		if e != nil {
			return nil, e
		}
		if len(links) != 1 || links[0].ExternalID != p.Request.ExternalAccountID || links[0].Account.Archived || links[0].Account.Currency != p.Request.Currency {
			return nil, errors.New("source account link changed or was archived; prepare a new request")
		}
		if p.Request.Mode == "ach" {
			token, err = bankingLinkAccessToken(links[0])
			if err != nil {
				return nil, err
			}
		}
	}
	if provider == "plaid" && p.Request.Mode == "payment" {
		if err := validatePlaidRecipient(ctx, p.Request); err != nil {
			return nil, err
		}
	}
	if provider == "enable-banking" {
		if err := validateEnableBank(ctx, p.Request); err != nil {
			return nil, err
		}
	}
	if provider == "teller" {
		if err := validateTellerPayment(ctx, p.Request); err != nil {
			return nil, err
		}
	}
	result, err := ctx.AppDB().Exec(`UPDATE bank_payments SET state='submitting',revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND state='draft'`, p.ID, projectID(ctx))
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return readBankPayment(ctx, p.ID)
	}
	p.State = "submitting"
	p.Revision++
	if isEuropeanPaymentProvider(provider) {
		return submitEuropeanPayment(ctx, p)
	}
	r := p.Request
	var raw map[string]any
	input := map[string]any{}
	tool := "create_payment"
	if provider == "teller" {
		input = map[string]any{"account_id": r.ExternalAccountID, "amount": paymentDecimal(r.Amount), "memo": r.Reference, "idempotency_key": p.ID,
			"payee": map[string]any{"scheme": "zelle", "address": r.RecipientAddress, "name": r.RecipientName, "type": r.RecipientType}}
	} else if r.Mode == "payment" {
		input = map[string]any{"recipient_id": r.RecipientID, "user_id": r.UserID, "reference": r.Reference, "amount": map[string]any{"currency": r.Currency, "value": json.Number(paymentDecimal(r.Amount))}}
	} else {
		authorization := map[string]any{}
		err = executeIntegrationJSON(ctx, r.ConnectionID, "create_transfer_authorization", map[string]any{"access_token": token, "account_id": r.ExternalAccountID, "type": r.Direction, "network": r.Network, "amount": paymentDecimal(r.Amount), "ach_class": r.ACHClass, "idempotency_key": p.ID, "user": map[string]any{"legal_name": r.LegalName}}, &authorization)
		if err != nil {
			return uncertainPayment(ctx, p, "Transfer authorization result is uncertain; inspect provider records before creating another request")
		}
		auth := childMap(authorization, "authorization")
		p.Response = map[string]any{"authorization": auth}
		if firstString(auth, "decision") != "approved" || firstString(auth, "id") == "" || firstString(auth, "decision_rationale.code") != "" {
			p.State = "authorization_blocked"
			if firstString(auth, "decision") == "user_action_required" {
				p.State = "authorization_required"
			}
			p.Error = "No transfer was created. Resolve the Plaid authorization before continuing this request."
			p.ProviderStatus = firstString(auth, "decision")
			if p.ProviderStatus == "declined" {
				p.State = "declined"
			}
			return saveBankPayment(ctx, p)
		}
		// Persist the authorization before the money-moving step, for crash recovery.
		if p, err = saveBankPayment(ctx, p); err != nil {
			return nil, err
		}
		if p.State != "submitting" {
			return p, nil
		}
		tool = "create_transfer"
		input = map[string]any{"access_token": token, "account_id": r.ExternalAccountID, "authorization_id": firstString(auth, "id"), "description": r.Reference, "idempotency_key": p.ID}
	}
	err = executeIntegrationJSON(ctx, r.ConnectionID, tool, input, &raw)
	if err != nil {
		return uncertainPayment(ctx, p, "Provider submission result is uncertain; refresh/reconcile before creating another payment. This request will not be resubmitted.")
	}
	if r.Mode == "ach" {
		raw = childMap(raw, "transfer")
	}
	p.Response = raw
	p.ProviderID = firstString(raw, "payment_id", "id")
	p.ProviderStatus = firstString(raw, "status")
	if p.ProviderStatus == "" {
		p.ProviderStatus = "recorded"
	}
	p.State = "submitted"
	if provider == "plaid" && r.Mode == "payment" {
		p.State = "authorization_required"
	}
	if firstString(raw, "connect_token") != "" {
		p.State = "authorization_required"
	}
	if p.ProviderID == "" && firstString(raw, "connect_token") == "" {
		return uncertainPayment(ctx, p, "Provider returned no payment identifier; reconcile with provider before another submission")
	}
	return saveBankPayment(ctx, p)
}

func uncertainPayment(ctx *sdk.AppCtx, p bankPayment, message string) (bankPayment, error) {
	p.State = "unknown"
	p.Error = message
	return saveBankPayment(ctx, p)
}
func paymentDecimal(n int64) string { return fmt.Sprintf("%d.%02d", n/100, n%100) }

func (a *App) toolBankingPaymentGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	p, err := readBankPayment(ctx, strArg(args, "id", ""))
	if err != nil {
		return nil, err
	}
	if !boolArg(args, "refresh", false) {
		return p, nil
	}
	_, provider, err := paymentConnection(ctx, p.Request.ConnectionID)
	if err != nil {
		return nil, err
	}
	if provider != p.Request.Provider {
		return nil, errors.New("connection provider changed")
	}
	if (p.State == "continuing" && p.Request.Provider != "enable-banking" && strArg(args, "provider_id", "") == "") || p.State == "cancelling" {
		return p, nil
	}
	if isEuropeanPaymentProvider(provider) {
		return refreshEuropeanPayment(ctx, p, strArg(args, "provider_id", ""))
	}
	adopting := false
	if p.ProviderID == "" {
		candidate := strArg(args, "provider_id", "")
		if candidate == "" {
			if p.Request.Provider == "teller" && (p.State == "authorization_required" || p.State == "unknown" || p.State == "submitting") {
				var raw any
				if err := executeIntegrationJSON(ctx, p.Request.ConnectionID, "list_payments", map[string]any{"account_id": p.Request.ExternalAccountID}, &raw); err != nil {
					return nil, errors.New("could not list Teller payments for reconciliation")
				}
				candidates := []map[string]any{}
				for _, m := range flattenItems(raw) {
					if paymentResponseMatches(p.Request, m) {
						candidates = append(candidates, map[string]any{"id": m["id"], "date": m["date"], "reference": m["reference"]})
					}
				}
				if p.Response == nil {
					p.Response = map[string]any{}
				}
				p.Response["candidates"] = candidates
			}
			return p, nil
		}
		if p.State != "unknown" && p.State != "authorization_required" && p.State != "submitting" && p.State != "continuing" {
			return nil, errors.New("provider ID can only reconcile an attempted submission")
		}
		p.ProviderID = candidate
		adopting = true
	}
	input := map[string]any{"payment_id": p.ProviderID}
	tool := "get_payment"
	if p.Request.Provider == "teller" {
		input["account_id"] = p.Request.ExternalAccountID
	}
	if p.Request.Mode == "ach" {
		tool = "get_transfer"
		input = map[string]any{"transfer_id": p.ProviderID}
	}
	var raw map[string]any
	if err = executeIntegrationJSON(ctx, p.Request.ConnectionID, tool, input, &raw); err != nil {
		return nil, errors.New("could not refresh payment status; retry the status check")
	}
	if p.Request.Mode == "ach" {
		raw = childMap(raw, "transfer")
	}
	// Ignore malformed and mismatched responses rather than losing a known result.
	if firstString(raw, "payment_id", "id") != p.ProviderID {
		return nil, errors.New("provider returned an invalid payment status response")
	}
	if adopting && !paymentResponseMatches(p.Request, raw) {
		return nil, errors.New("provider payment details do not match this request; no changes saved")
	}
	p.Response = raw
	p.ProviderStatus = firstString(raw, "status")
	if p.ProviderStatus == "" {
		p.ProviderStatus = "recorded"
	}
	p.Error = ""
	// Keep provider vocabulary intact. 'Executed', 'pending', etc. are not claims
	// of settlement. Authorization failures can still require user action.
	p.State = "submitted"
	if p.Request.Mode == "payment" && p.Request.Provider == "plaid" && p.ProviderStatus == "PAYMENT_STATUS_INPUT_NEEDED" {
		p.State = "authorization_required"
	}
	return saveBankPayment(ctx, p)
}

func (a *App) toolBankingPaymentAuthorize(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	p, err := readBankPayment(ctx, strArg(args, "id", ""))
	if err != nil {
		return nil, err
	}
	if p.State != "authorization_required" {
		return nil, errors.New("payment does not require bank authorization")
	}
	if _, provider, err := paymentConnection(ctx, p.Request.ConnectionID); err != nil {
		return nil, err
	} else if provider != p.Request.Provider {
		return nil, errors.New("connection provider changed")
	}
	if isEuropeanPaymentProvider(p.Request.Provider) {
		return europeanHandoff(p)
	}
	if p.Request.Provider == "teller" {
		token := firstString(p.Response, "connect_token")
		if token == "" {
			return nil, errors.New("no Teller Connect token available")
		}
		return map[string]any{"provider": "teller", "connect_token": token}, nil
	}
	if p.Request.Mode == "ach" {
		return plaidACHHandoff(ctx, p)
	}
	if p.ProviderID == "" {
		return nil, errors.New("payment ID missing")
	}
	_, provider, err := paymentConnection(ctx, p.Request.ConnectionID)
	if err != nil {
		return nil, err
	}
	if provider != p.Request.Provider {
		return nil, errors.New("connection provider changed")
	}
	var out map[string]any
	err = executeIntegrationJSON(ctx, p.Request.ConnectionID, "create_link_token", map[string]any{"client_name": "Apteva Finance", "language": "en", "country_codes": []string{p.Request.Country}, "user_id": p.Request.UserID, "products": []string{"payment_initiation"}, "payment_initiation": map[string]any{"payment_id": p.ProviderID}, "hosted_link": map[string]any{}}, &out)
	if err != nil {
		return nil, errors.New("could not create Plaid bank authorization link")
	}
	return map[string]any{"provider": "plaid", "hosted_link_url": out["hosted_link_url"], "link_token": out["link_token"], "expiration": out["expiration"]}, nil
}

func (a *App) toolBankingPaymentsList(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	rows, err := ctx.AppDB().Query(`SELECT id FROM bank_payments WHERE project_id=? ORDER BY created_at DESC,id DESC LIMIT 100`, projectID(ctx))
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []bankPayment{}
	for _, id := range ids {
		p, e := readBankPayment(ctx, id)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return map[string]any{"payments": out, "capabilities": bankingPaymentCapabilities()}, nil
}

func (a *App) toolBankingPaymentCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := strArg(args, "id", "")
	p, err := readBankPayment(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.State != "draft" {
		return cancelPlaidTransfer(ctx, p, args)
	}
	res, err := ctx.AppDB().Exec(`UPDATE bank_payments SET state='cancelled',revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=? AND state='draft'`, projectID(ctx), id)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return nil, errors.New("only an existing draft can be cancelled")
	}
	return readBankPayment(ctx, id)
}

func (a *App) handleBankingPayments(w http.ResponseWriter, r *http.Request) {
	switch strings.TrimPrefix(r.URL.Path, "/banking/payments/") {
	case "events":
		postBody(w, r, a.toolBankingPaymentEvents)
	case "options":
		postBody(w, r, a.toolBankingPaymentOptions)
	case "continue":
		postBody(w, r, a.toolBankingPaymentContinue)
	case "callback":
		postBody(w, r, a.toolBankingPaymentCallback)
	case "prepare":
		postBody(w, r, a.toolBankingPaymentPrepare)
	case "submit":
		postBody(w, r, a.toolBankingPaymentSubmit)
	case "get":
		postBody(w, r, a.toolBankingPaymentGet)
	case "authorize":
		postBody(w, r, a.toolBankingPaymentAuthorize)
	case "cancel":
		postBody(w, r, a.toolBankingPaymentCancel)
	case "list":
		postBody(w, r, a.toolBankingPaymentsList)
	default:
		http.NotFound(w, r)
	}
}

func paymentResponseMatches(r bankPaymentRequest, raw map[string]any) bool {
	if r.Provider == "teller" {
		n, err := enableMinor(firstString(raw, "amount"), "USD")
		return err == nil && n == r.Amount && strings.EqualFold(firstString(raw, "payee.address"), r.RecipientAddress) && firstString(raw, "payee.scheme") == "zelle" && firstString(raw, "memo") == r.Reference
	}
	if r.Mode == "ach" {
		n, err := enableMinor(firstString(raw, "amount"), "USD")
		return err == nil && n == r.Amount && firstString(raw, "account_id") == r.ExternalAccountID && firstString(raw, "type") == r.Direction && firstString(raw, "description") == r.Reference
	}
	n, err := enableMinor(firstString(raw, "amount.value"), r.Currency)
	return err == nil && n == r.Amount && firstString(raw, "amount.currency") == r.Currency && firstString(raw, "recipient_id") == r.RecipientID && firstString(raw, "reference") == r.Reference
}
