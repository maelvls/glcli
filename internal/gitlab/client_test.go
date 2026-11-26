package gitlab

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/maelvls/glcli/internal/config"
)

func TestTokenNotSentToAnotherHost(t *testing.T) {
	var leaked string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("PRIVATE-TOKEN")
		fmt.Fprint(w, `{}`)
	}))
	defer other.Close()
	var sameHost string
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/same" {
			sameHost = r.Header.Get("PRIVATE-TOKEN")
			fmt.Fprint(w, `{}`)
			return
		}
		if r.URL.Path == "/api/v4/redirect-same" {
			http.Redirect(w, r, "/api/v4/same", http.StatusFound)
			return
		}
		http.Redirect(w, r, other.URL+"/x", http.StatusFound)
	}))
	defer gl.Close()
	c := New(config.Host{Name: "test", Token: "secret"})
	c.BaseURL = gl.URL
	var out map[string]any
	if err := c.Get(context.Background(), "elsewhere", nil, &out); err != nil {
		t.Fatal(err)
	}
	if leaked != "" {
		t.Fatalf("the token was sent to another host: %q", leaked)
	}
	if err := c.Get(context.Background(), "redirect-same", nil, &out); err != nil {
		t.Fatal(err)
	}
	if sameHost != "secret" {
		t.Fatalf("a redirect on the same host must keep the token, got %q", sameHost)
	}
}

func TestIsTransient(t *testing.T) {
	if !IsTransient(&APIError{Status: 502}) || !IsTransient(&APIError{Status: 429}) {
		t.Error("5xx and 429 are transient")
	}
	if IsTransient(&APIError{Status: 404}) || IsTransient(context.Canceled) {
		t.Error("404 and cancellation are not transient")
	}
	if !IsTransient(&url.Error{Op: "Get", URL: "x", Err: fmt.Errorf("connection refused")}) {
		t.Error("a network error is transient")
	}
	if IsTransient(&url.Error{Op: "Get", URL: "x", Err: x509.UnknownAuthorityError{}}) {
		t.Error("an untrusted certificate is not transient")
	}
}
