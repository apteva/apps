package main

import (
	"context"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Only the number association is removed. The trunk remains available to
// established dialogs until the durable retirement worker observes drain.
func detachTwilioSIPNumber(ctx *sdk.AppCtx, connection int64, trunk, number string) error {
	credentials, err := ctx.PlatformAPI().GetConnectionCredentials(connection)
	if err != nil {
		return err
	}
	if credentials == nil || credentials.Slug != "twilio" {
		return errors.New("Twilio credentials unavailable")
	}
	account, password := credentials.Fields["account_sid"], credentials.Fields["auth_token"]
	if account == "" || password == "" || !strings.HasPrefix(trunk, "TK") || !strings.HasPrefix(number, "PN") {
		return errors.New("Twilio association or credentials incomplete")
	}
	request, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(request, http.MethodDelete, "https://trunking.twilio.com/v1/Trunks/"+url.PathEscape(trunk)+"/PhoneNumbers/"+url.PathEscape(number), nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(account, password)
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("Twilio number detach request failed")
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusNotFound {
		return errors.New("Twilio number detach failed")
	}
	return nil
}
