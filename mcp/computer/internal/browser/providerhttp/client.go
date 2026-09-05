// Package providerhttp applies common bounds to credentialed provider requests.
package providerhttp

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func New(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("provider redirect limit exceeded")
		}
		if len(via) > 0 && (!strings.EqualFold(req.URL.Scheme, via[0].URL.Scheme) || !strings.EqualFold(req.URL.Host, via[0].URL.Host)) {
			for _, name := range []string{"Authorization", "Cookie", "X-BB-API-Key", "Steel-Api-Key", "X-Api-Key"} {
				req.Header.Del(name)
			}
		}
		return nil
	}}
}
func ReadAll(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("provider response exceeds %d bytes", limit)
	}
	return body, nil
}
