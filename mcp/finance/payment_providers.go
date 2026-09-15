package main

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func isEuropeanPaymentProvider(p string) bool {
	return p == "enable-banking" || p == "truelayer-payments" || p == "saltedge-payments"
}
func isPaymentProvider(p string) bool {
	return p == "plaid" || p == "teller" || isEuropeanPaymentProvider(p)
}

func validIBAN(value string) bool {
	if !regexp.MustCompile(`^[A-Z]{2}[0-9]{2}[A-Z0-9]{11,30}$`).MatchString(value) {
		return false
	}
	rem := 0
	for _, c := range value[4:] + value[:4] {
		if c >= 'A' && c <= 'Z' {
			rem = (rem*100 + int(c-'A') + 10) % 97
		} else {
			rem = (rem*10 + int(c-'0')) % 97
		}
	}
	return rem == 1
}
func normalizeIBAN(s string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
}
func httpsURL(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("a valid registered HTTPS return URL is required")
	}
	return u, nil
}
func prepareEuropeanPayment(ctx *sdk.AppCtx, args map[string]any, r *bankPaymentRequest) error {
	if r.Mode != "payment" {
		return errors.New("this provider supports payment mode only")
	}
	r.RedirectURL = strings.TrimSpace(strArg(args, "redirect_url", ""))
	if _, err := httpsURL(r.RedirectURL); err != nil {
		return err
	}
	r.BankName = strings.TrimSpace(strArg(args, "bank_name", ""))
	r.PaymentType = strArg(args, "payment_type", "SEPA")
	r.PSUType = strArg(args, "psu_type", "personal")
	r.Deferred = boolArg(args, "deferred", false)
	r.DebtorIBAN = normalizeIBAN(strArg(args, "debtor_iban", ""))
	r.RecipientAddress = normalizeIBAN(r.RecipientAddress)
	r.CustomerID = strings.TrimSpace(strArg(args, "customer_id", ""))
	r.CustomerIP = strings.TrimSpace(strArg(args, "customer_ip", ""))
	r.Email = strings.TrimSpace(strArg(args, "email", ""))
	r.SortCode = strArg(args, "sort_code", "")
	r.AccountNumber = strArg(args, "account_number", "")
	if r.RecipientName == "" {
		return errors.New("recipient_name required")
	}
	if !regexp.MustCompile(`^[A-Z]{2}$`).MatchString(r.Country) {
		return errors.New("two-letter payer bank country required")
	}
	if r.Provider == "truelayer-payments" {
		if r.Currency != "EUR" && r.Currency != "GBP" {
			return errors.New("TrueLayer payments require EUR or GBP")
		}
		if r.Currency == "GBP" {
			if !regexp.MustCompile(`^[0-9]{6}$`).MatchString(r.SortCode) || !regexp.MustCompile(`^[0-9]{8}$`).MatchString(r.AccountNumber) {
				return errors.New("GBP payments require a six-digit sort code and eight-digit account number")
			}
			r.RecipientAddress = ""
		} else if !validIBAN(r.RecipientAddress) {
			return errors.New("valid recipient IBAN required")
		}
		if _, err := mail.ParseAddress(r.Email); err != nil || r.LegalName == "" {
			return errors.New("payer legal_name and valid email required")
		}
		if len(r.Reference) > 18 || len(r.RecipientName) > 40 {
			return errors.New("reference maximum 18 characters; recipient name maximum 40")
		}
		if r.Deferred {
			return errors.New("deferred submission is available only with Enable Banking")
		}
		return nil
	}
	if r.Currency != "EUR" || !validIBAN(r.RecipientAddress) {
		return errors.New("this flow requires EUR and a valid recipient IBAN")
	}
	if r.DebtorIBAN != "" && !validIBAN(r.DebtorIBAN) {
		return errors.New("invalid debtor IBAN")
	}
	if r.Provider == "enable-banking" {
		if r.PaymentType != "SEPA" && r.PaymentType != "INST_SEPA" {
			return errors.New("choose SEPA or INST_SEPA from the bank's supported payment types")
		}
		if r.PSUType != "personal" && r.PSUType != "business" {
			return errors.New("psu_type must be personal or business")
		}
		return validateEnableBank(ctx, *r)
	}
	if r.PaymentType != "SEPA" {
		return errors.New("Finance currently supports Salt Edge's SEPA template")
	}
	if r.Deferred {
		return errors.New("deferred submission is available only with Enable Banking")
	}
	if r.BankName == "" || r.CustomerID == "" || net.ParseIP(r.CustomerIP) == nil || r.DebtorIBAN == "" {
		return errors.New("Salt Edge requires bank provider code, customer_id, actual customer_ip and debtor_iban")
	}
	// Read the actual template before preparing. Extra required bank fields are
	// reported by the provider rather than invented or populated with defaults.
	var template map[string]any
	if err := executeIntegrationJSON(ctx, r.ConnectionID, "get_payment_template", map[string]any{"template_identifier": r.PaymentType}, &template); err != nil {
		return fmt.Errorf("could not load Salt Edge payment template: %w", err)
	}
	return nil
}

func validateEnableBank(ctx *sdk.AppCtx, r bankPaymentRequest) error {
	var raw map[string]any
	if err := executeIntegrationJSON(ctx, r.ConnectionID, "list_banks", map[string]any{"country": r.Country, "psu_type": r.PSUType, "service": "PIS"}, &raw); err != nil {
		return err
	}
	for _, b := range flattenItems(raw["aspsps"]) {
		if firstString(b, "name") != r.BankName || firstString(b, "country") != r.Country {
			continue
		}
		for _, m := range flattenItems(b["payments"]) {
			if firstString(m, "payment_type") != r.PaymentType || (firstString(m, "psu_type") != "" && firstString(m, "psu_type") != r.PSUType) {
				continue
			}
			for key, want := range map[string]string{"currencies": r.Currency, "creditor_account_schemas": "IBAN"} {
				if values, ok := m[key].([]any); ok && len(values) > 0 {
					found := false
					for _, value := range values {
						if value == want {
							found = true
						}
					}
					if !found {
						return fmt.Errorf("bank payment type does not support %s %s", key, want)
					}
				}
			}
			if r.Deferred && !boolArg(m, "deferred_submission_supported", false) {
				return errors.New("bank does not support deferred submission for this payment type")
			}
			if boolArg(m, "debtor_account_required", false) && r.DebtorIBAN == "" {
				return errors.New("this bank requires debtor_iban")
			}
			// These fields are not available in the simple SEPA form. Fail before a write.
			for k, v := range m {
				if strings.HasSuffix(k, "_required") && v == true && k != "debtor_account_required" && k != "creditor_name_required" && k != "remittance_information_required" {
					return fmt.Errorf("bank requires %s; this payment type needs additional fields not yet supported by Finance", k)
				}
			}
			return nil
		}
	}
	return errors.New("bank/payment type is not available for this country and payer type")
}

func validateTellerPayment(ctx *sdk.AppCtx, r bankPaymentRequest) error {
	var account, capabilities map[string]any
	input := map[string]any{"account_id": r.ExternalAccountID}
	if err := executeIntegrationJSON(ctx, r.ConnectionID, "get_account", input, &account); err != nil {
		return err
	}
	if firstString(account, "links.payments") == "" {
		return errors.New("this Teller account does not support payments")
	}
	if err := executeIntegrationJSON(ctx, r.ConnectionID, "get_payment_capabilities", input, &capabilities); err != nil {
		return err
	}
	for _, scheme := range flattenItems(capabilities["schemes"]) {
		if firstString(scheme, "name") == "zelle" {
			return nil
		}
	}
	return errors.New("this Teller account does not support Zelle")
}

func (a *App) toolBankingPaymentOptions(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := exactPositiveInteger(args["connection_id"])
	if err != nil {
		return nil, err
	}
	_, provider, err := paymentConnection(ctx, id)
	if err != nil {
		return nil, err
	}
	var raw any
	tool := ""
	input := map[string]any{}
	switch provider {
	case "enable-banking":
		tool = "list_banks"
		input = map[string]any{"country": strArg(args, "country", "ES"), "psu_type": strArg(args, "psu_type", "personal"), "service": "PIS"}
	case "plaid":
		tool = "list_payment_recipients"
		input = map[string]any{"count": 100}
		if c := strArg(args, "cursor", ""); c != "" {
			input["cursor"] = c
		}
	case "teller":
		aid, e := exactPositiveInteger(args["account_id"])
		if e != nil {
			return nil, e
		}
		links, e := bankingLinks(ctx, provider, id, aid)
		if e != nil {
			return nil, e
		}
		if len(links) != 1 {
			return nil, errors.New("linked account required")
		}
		tool = "get_payment_capabilities"
		input["account_id"] = links[0].ExternalID
	case "saltedge-payments":
		if t := strArg(args, "template_identifier", ""); t != "" {
			tool = "get_payment_template"
			input["template_identifier"] = t
		} else {
			tool = "list_payment_banks"
			input["country_code"] = strArg(args, "country", "ES")
			if c := strArg(args, "cursor", ""); c != "" {
				input["from_id"] = c
			}
		}
	case "truelayer-payments":
		return map[string]any{"hosted_selection": true, "note": "Choose the bank on TrueLayer's hosted payment page."}, nil
	default:
		return nil, errors.New("this connection provides account data only")
	}
	err = executeIntegrationJSON(ctx, id, tool, input, &raw)
	return raw, err
}

func europeanPaymentInput(p bankPayment) map[string]any {
	r := p.Request
	switch r.Provider {
	case "enable-banking":
		request := map[string]any{"credit_transfer_transaction": []any{map[string]any{"beneficiary": map[string]any{"creditor": map[string]any{"name": r.RecipientName}, "creditor_account": map[string]any{"identification": r.RecipientAddress, "scheme_name": "IBAN"}}, "instructed_amount": map[string]any{"amount": paymentDecimal(r.Amount), "currency": r.Currency}, "remittance_information": []string{r.Reference}, "payment_id": map[string]any{"end_to_end_id": strings.ReplaceAll(p.ID, "-", "")}}}}
		if r.DebtorIBAN != "" {
			request["debtor_account"] = map[string]any{"identification": r.DebtorIBAN, "scheme_name": "IBAN"}
		}
		return map[string]any{"payment_type": r.PaymentType, "payment_request": request, "aspsp": map[string]any{"name": r.BankName, "country": r.Country}, "psu_type": r.PSUType, "redirect_url": r.RedirectURL, "state": p.CallbackState, "defer_submission": r.Deferred}
	case "truelayer-payments":
		identifier := map[string]any{"type": "iban", "iban": r.RecipientAddress}
		if r.Currency == "GBP" {
			identifier = map[string]any{"type": "sort_code_account_number", "sort_code": r.SortCode, "account_number": r.AccountNumber}
		}
		return map[string]any{"idempotency_key": p.ID, "amount_in_minor": r.Amount, "currency": r.Currency, "payment_method": map[string]any{"type": "bank_transfer", "provider_selection": map[string]any{"type": "user_selected"}, "beneficiary": map[string]any{"type": "external_account", "account_holder_name": r.RecipientName, "account_identifier": identifier, "reference": r.Reference}}, "user": map[string]any{"name": r.LegalName, "email": r.Email}, "hosted_page": map[string]any{"return_uri": r.RedirectURL, "country_code": r.Country}, "metadata": map[string]any{"finance_payment_id": p.ID}}
	default:
		return map[string]any{"template_identifier": r.PaymentType, "customer_id": r.CustomerID, "provider": map[string]any{"code": r.BankName}, "return_payment_id": true, "attempt": map[string]any{"return_to": r.RedirectURL, "custom_fields": map[string]any{"state": p.CallbackState}}, "payment_attributes": map[string]any{"creditor_name": r.RecipientName, "creditor_iban": r.RecipientAddress, "debtor_iban": r.DebtorIBAN, "amount": paymentDecimal(r.Amount), "currency_code": r.Currency, "reference": r.Reference, "description": r.Reference, "end_to_end_id": strings.ReplaceAll(p.ID, "-", ""), "customer_ip_address": r.CustomerIP}}
	}
}
func europeanResponse(provider string, raw map[string]any) map[string]any {
	if provider == "saltedge-payments" {
		return childMap(raw, "data")
	}
	return raw
}
func europeanURL(raw map[string]any) string {
	return firstString(raw, "url", "payment_url", "hosted_page.uri", "hosted_page.url", "hosted_page_uri", "_authorization_url")
}
func submitEuropeanPayment(ctx *sdk.AppCtx, p bankPayment) (bankPayment, error) {
	var raw map[string]any
	if err := executeIntegrationJSON(ctx, p.Request.ConnectionID, "create_payment", europeanPaymentInput(p), &raw); err != nil {
		return uncertainPayment(ctx, p, "Payment creation outcome is uncertain. Reconcile the provider record before another payment; this request will not be replayed.")
	}
	raw = europeanResponse(p.Request.Provider, raw)
	p.Response = raw
	p.ProviderID = firstString(raw, "payment_id", "id")
	p.ProviderStatus = firstString(raw, "status")
	p.State = "authorization_required"
	if p.ProviderID == "" {
		return uncertainPayment(ctx, p, "Provider returned no payment ID; verify provider records before another payment.")
	}
	return saveBankPayment(ctx, p)
}
func europeanHandoff(p bankPayment) (any, error) {
	rawURL := europeanURL(p.Response)
	u, err := httpsURL(rawURL)
	if err != nil {
		return nil, errors.New("provider returned no valid authorization URL; refresh status or inspect the provider record")
	}
	return map[string]any{"provider": p.Request.Provider, "hosted_link_url": u.String()}, nil
}
func refreshEuropeanPayment(ctx *sdk.AppCtx, p bankPayment, candidate string) (bankPayment, error) {
	adopting := p.ProviderID == ""
	if adopting {
		if candidate == "" {
			return p, nil
		}
		if p.State != "unknown" && p.State != "submitting" {
			return p, errors.New("only an attempted payment can be reconciled")
		}
		p.ProviderID = candidate
	}
	input := map[string]any{"payment_id": p.ProviderID}
	if p.Request.Provider == "truelayer-payments" {
		input = map[string]any{"id": p.ProviderID}
	}
	var raw map[string]any
	if err := executeIntegrationJSON(ctx, p.Request.ConnectionID, "get_payment", input, &raw); err != nil {
		return p, err
	}
	raw = europeanResponse(p.Request.Provider, raw)
	if firstString(raw, "id", "payment_id") != p.ProviderID {
		return p, errors.New("provider payment ID mismatch")
	}
	if adopting && !europeanResponseMatches(p, raw) {
		return p, errors.New("provider payment does not match the reviewed amount, recipient and reference")
	}
	handoff := europeanURL(p.Response)
	p.Response = raw
	if handoff != "" {
		p.Response["_authorization_url"] = handoff
	}
	p.ProviderStatus = firstString(raw, "status")
	p.Error = ""
	p.State = "submitted"
	status := strings.ToLower(p.ProviderStatus)
	if status == "authorization_required" || status == "authorizing" || status == "rcvd" {
		p.State = "authorization_required"
	}
	if p.Request.Provider == "enable-banking" && p.Request.Deferred && !p.Continued {
		p.State = "authorization_required"
		if p.CallbackVerified {
			p.State = "ready_to_execute"
		}
	}
	if status == "failed" || status == "rejected" || status == "rjct" || status == "cancelled" || status == "cncl" {
		p.State = "failed"
	}
	return saveBankPayment(ctx, p)
}
func europeanResponseMatches(p bankPayment, raw map[string]any) bool {
	r := p.Request
	switch r.Provider {
	case "enable-banking":
		txs := flattenItems(childMap(raw, "payment_details")["credit_transfer_transaction"])
		if len(txs) != 1 {
			return false
		}
		t := txs[0]
		n, e := enableMinor(firstString(t, "instructed_amount.amount"), r.Currency)
		return e == nil && n == r.Amount && firstString(t, "instructed_amount.currency") == r.Currency && firstString(t, "beneficiary.creditor_account.identification") == r.RecipientAddress && firstString(t, "beneficiary.creditor.name") == r.RecipientName && firstString(t, "payment_id.end_to_end_id") == strings.ReplaceAll(p.ID, "-", "")
	case "truelayer-payments":
		n, e := exactPositiveInteger(raw["amount_in_minor"])
		b := childMap(childMap(raw, "payment_method"), "beneficiary")
		accountMatches := firstString(b, "account_identifier.iban") == r.RecipientAddress
		if r.Currency == "GBP" {
			accountMatches = firstString(b, "account_identifier.sort_code") == r.SortCode && firstString(b, "account_identifier.account_number") == r.AccountNumber
		}
		return e == nil && n == r.Amount && firstString(raw, "currency") == r.Currency && accountMatches && firstString(b, "reference") == r.Reference && firstString(b, "account_holder_name") == r.RecipientName && firstString(raw, "metadata.finance_payment_id") == p.ID
	default:
		attrs := childMap(raw, "payment_attributes")
		n, e := enableMinor(firstString(attrs, "amount"), r.Currency)
		return e == nil && n == r.Amount && firstString(attrs, "currency_code") == r.Currency && firstString(attrs, "creditor_iban") == r.RecipientAddress && firstString(attrs, "debtor_iban") == r.DebtorIBAN && firstString(attrs, "end_to_end_id") == strings.ReplaceAll(p.ID, "-", "") && firstString(raw, "customer_id") == r.CustomerID
	}
}

func (a *App) toolBankingPaymentCallback(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	p, err := readBankPayment(ctx, strArg(args, "id", ""))
	if err != nil {
		return nil, err
	}
	if p.Request.Provider != "enable-banking" && p.Request.Provider != "saltedge-payments" {
		return nil, errors.New("this provider uses hosted authorization followed by a status refresh")
	}
	if p.State != "authorization_required" && p.State != "ready_to_execute" {
		return nil, errors.New("payment is not awaiting authorization")
	}
	expected, e := httpsURL(p.Request.RedirectURL)
	if e != nil {
		return nil, e
	}
	returned, e := httpsURL(strArg(args, "return_url", ""))
	if e != nil {
		return nil, e
	}
	if returned.Host != expected.Host || returned.Path != expected.Path || p.CallbackState == "" || subtle.ConstantTimeCompare([]byte(returned.Query().Get("state")), []byte(p.CallbackState)) != 1 {
		return nil, errors.New("bank return URL or callback state does not match this payment")
	}
	for k, values := range expected.Query() {
		if returned.Query().Get(k) != values[0] {
			return nil, errors.New("return URL query mismatch")
		}
	}
	if returned.Query().Get("error") != "" {
		return nil, errors.New("bank authorization returned an error; refresh provider status")
	}
	p.CallbackVerified = true
	if p.Request.Deferred && !p.Continued {
		p.State = "ready_to_execute"
	}
	return saveBankPayment(ctx, p)
}

func validatePlaidRecipient(ctx *sdk.AppCtx, r bankPaymentRequest) error {
	var raw map[string]any
	if err := executeIntegrationJSON(ctx, r.ConnectionID, "get_payment_recipient", map[string]any{"recipient_id": r.RecipientID}, &raw); err != nil {
		return err
	}
	if firstString(raw, "recipient_id") != r.RecipientID || firstString(raw, "name") != r.RecipientName || firstString(raw, "iban") != r.RecipientAddress || firstString(raw, "bacs.sort_code") != r.SortCode || firstString(raw, "bacs.account") != r.AccountNumber {
		return errors.New("registered Plaid recipient changed; prepare a new payment for review")
	}
	return nil
}
