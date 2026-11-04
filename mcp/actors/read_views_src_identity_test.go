package main

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestReadViewNativeImageIdentity(t *testing.T) {
	c := fixtureReadViews()
	c.IdentityURL = "https://imagecdn.example.com/accounts99/572201/clip_images/"
	c.Identity = actorField{Selector: "img#identity", Attribute: "src", Required: true, Pattern: `^(https://imagecdn\.example\.com/accounts99/572201)/clip_images/`}
	if err := validateReadViews(&c); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"first page", `<img id="identity" src="https://imagecdn.example.com/accounts99/572201/clip_images/rect_ex_lrg_34753567.jpg">`, true},
		{"another page clip", `<img id="identity" src="https://imagecdn.example.com/accounts99/572201/clip_images/rect_ex_lrg_34818925.jpg">`, true},
		{"foreign store", `<img id="identity" src="https://imagecdn.example.com/accounts99/575985/clip_images/rect_ex_lrg_34753567.jpg">`, false},
		{"foreign host", `<img id="identity" src="https://evil.example/accounts99/572201/clip_images/rect_ex_lrg_34753567.jpg">`, false},
		{"non-image URL", `<iframe id="identity" src="https://imagecdn.example.com/accounts99/572201/clip_images/rect_ex_lrg_34753567.jpg"></iframe>`, false},
		{"absent attribute", `<img id="identity">`, false},
		{"empty attribute", `<img id="identity" src="">`, false},
		{"ambiguous", `<img id="identity" src="https://imagecdn.example.com/accounts99/572201/clip_images/a.jpg"><img id="identity" src="https://imagecdn.example.com/accounts99/572201/clip_images/b.jpg">`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := html.Parse(strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			_, err = verifyReadIdentity(c, root, "https://example.com/report")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestReadViewIdentityKeepsHrefAndRejectsMissingURL(t *testing.T) {
	c := fixtureReadViews()
	for _, body := range []string{`<a id="identity" href="https://example.com/creator">Creator</a>`, `<a id="identity" href="/creator">Creator</a>`} {
		root, _ := html.Parse(strings.NewReader(body))
		if _, err := verifyReadIdentity(c, root, "https://example.com/report"); err != nil {
			t.Fatal(err)
		}
	}
	root, _ := html.Parse(strings.NewReader(`<a id="identity">Missing href</a>`))
	if _, err := verifyReadIdentity(c, root, c.IdentityURL); err == nil {
		t.Fatal("a missing href must not resolve to the current URL as false identity evidence")
	}
	c.Identity.Attribute = "class"
	if err := validateReadViews(&c); err == nil {
		t.Fatal("identity attributes remain limited to explicit native URLs")
	}
}
