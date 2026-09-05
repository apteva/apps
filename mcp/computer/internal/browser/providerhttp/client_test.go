package providerhttp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRedirectStripsProviderCredentials(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, key := range []string{"X-BB-API-Key", "Steel-Api-Key", "X-Api-Key", "Authorization"} {
			if r.Header.Get(key) != "" {
				t.Errorf("credential forwarded: %s", key)
			}
		}
		io.WriteString(w, "ok")
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 302) }))
	defer source.Close()
	request, _ := http.NewRequest("GET", source.URL, nil)
	for _, key := range []string{"X-BB-API-Key", "Steel-Api-Key", "X-Api-Key", "Authorization"} {
		request.Header.Set(key, "fixture")
	}
	response, err := New(time.Second).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}
func TestOversizedBodyRejected(t *testing.T) {
	if body, err := ReadAll(strings.NewReader("12345"), 4); err == nil || body != nil {
		t.Fatal("truncated body silently accepted")
	}
	if body, err := ReadAll(strings.NewReader("1234"), 4); err != nil || string(body) != "1234" {
		t.Fatal(err)
	}
}
