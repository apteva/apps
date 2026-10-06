package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPAppContextPreservesMountedContext(t *testing.T) {
	app, mounted, _ := newTestEnv(t)
	request := httptest.NewRequest("GET", "/conversations", nil)
	if app.appCtx(request) != mounted {
		t.Fatal("service request changed mounted context")
	}
	request.AddCookie(&http.Cookie{Name: "session", Value: "signed-in-user"})
	scoped := app.appCtx(request)
	if scoped == mounted {
		t.Fatal("browser request must derive a separate context")
	}
	if scoped.AppDB() != mounted.AppDB() || scoped.CurrentProject() != mounted.CurrentProject() {
		t.Fatal("request context lost app state")
	}
	if mountedCtx != mounted {
		t.Fatal("browser session replaced mounted context")
	}
}
