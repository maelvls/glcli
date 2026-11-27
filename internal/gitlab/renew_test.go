package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/maelvls/glcli/internal/config"
)

// fakeRenewer hands out the tokens of a list, one per call.
type fakeRenewer struct {
	mu    sync.Mutex
	next  []config.Host
	calls []string // the token each call was made for
	gens  []int
	gen   int
	why   error // what Renew says when it has no token left
}

func (f *fakeRenewer) Generation() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gen
}

func (f *fakeRenewer) Renew(rejected config.Host, generation int) (config.Host, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, rejected.Token)
	f.gens = append(f.gens, generation)
	f.gen++
	if len(f.next) == 0 {
		return rejected, false, f.why
	}
	h := f.next[0]
	f.next = f.next[1:]
	return h, true, nil
}

// acceptOnly is a fake GitLab that takes one token, in one header.
func acceptOnly(t *testing.T, accept *string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := "PRIVATE-TOKEN: " + r.Header.Get("PRIVATE-TOKEN")
		if a := r.Header.Get("Authorization"); a != "" {
			got = "Authorization: " + a
		}
		mu.Lock()
		seen = append(seen, got)
		ok := got == *accept
		mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"401 Unauthorized"}`)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(gl.Close)
	return gl, &seen
}

// After a 401, the client asks its Renewer once, makes the request again
// with the token it gave, and keeps that token for the next requests.
func TestClientTriesTheRenewedTokenOnce(t *testing.T) {
	accept := "Authorization: Bearer new"
	gl, seen := acceptOnly(t, &accept)
	expiry := time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC)
	renewer := &fakeRenewer{next: []config.Host{{Name: "elsewhere", APIHost: "elsewhere.example.com", Protocol: "http", Token: "new", OAuth: true, Expiry: expiry, TokenSource: "other.yml"}}}
	first := config.Host{Name: "test", Token: "old", TokenSource: "config.yml"}
	c := New(first)
	c.BaseURL = gl.URL
	c.Renewer = renewer
	var out map[string]any
	for range 3 {
		if err := c.Get(context.Background(), "user", nil, &out); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"PRIVATE-TOKEN: old", "Authorization: Bearer new", "Authorization: Bearer new", "Authorization: Bearer new"}
	if !slices.Equal(*seen, want) {
		t.Errorf("requests:\n got %q\nwant %q", *seen, want)
	}
	if !slices.Equal(renewer.calls, []string{"old"}) || !slices.Equal(renewer.gens, []int{0}) {
		t.Errorf("the renewer was asked for %q at generations %v, want once for the old token at 0", renewer.calls, renewer.gens)
	}
	// Host is what the client was made with; of the host the renewer gave,
	// only the token and what goes with it are used.
	if c.Host != first {
		t.Errorf("Host changed: %+v", c.Host)
	}
	if h := c.host(); h.Name != "test" || h.TokenSource != "config.yml" || h.Token != "new" || !h.OAuth || !h.Expiry.Equal(expiry) || h.APIHost != "" {
		t.Errorf("the host of the next requests: %+v", h)
	}
}

// The token the Renewer gave is rejected too: the error is the second
// 401, with what is known of the second token, and nothing is tried a
// third time.
func TestClientDoesNotLoopOnRejectedTokens(t *testing.T) {
	accept := "nothing"
	gl, seen := acceptOnly(t, &accept)
	expiry := time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC)
	renewer := &fakeRenewer{next: []config.Host{{Token: "new", OAuth: true, Expiry: expiry}, {Token: "newer"}}}
	c := New(config.Host{Name: "test", Token: "old", TokenSource: "config.yml"})
	c.BaseURL = gl.URL
	c.Renewer = renewer
	var out map[string]any
	err := c.Get(context.Background(), "user", nil, &out)
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != 401 || !apiErr.OAuth || !apiErr.TokenExpiry.Equal(expiry) || apiErr.Host != "test" || apiErr.TokenSource != "config.yml" {
		t.Errorf("got %#v", err)
	}
	if want := []string{"PRIVATE-TOKEN: old", "Authorization: Bearer new"}; !slices.Equal(*seen, want) {
		t.Errorf("requests:\n got %q\nwant %q", *seen, want)
	}
	if len(renewer.calls) != 1 {
		t.Errorf("the renewer was asked %d times, want 1", len(renewer.calls))
	}
}

// Without another token, or without a Renewer, a 401 is the error it
// always was, after one request.
func TestClientWithoutAnotherToken(t *testing.T) {
	for name, renewer := range map[string]Renewer{
		"no renewer":     nil,
		"no other token": &fakeRenewer{},
		"the same token": &fakeRenewer{next: []config.Host{{Token: "old"}}},
		"the same token, said to be an OAuth one": &fakeRenewer{next: []config.Host{{Token: "old", OAuth: true}}},
	} {
		accept := "nothing"
		gl, seen := acceptOnly(t, &accept)
		c := New(config.Host{Name: "test", Token: "old", TokenSource: "config.yml"})
		c.BaseURL = gl.URL
		c.Renewer = renewer
		var out map[string]any
		err := c.Get(context.Background(), "user", nil, &out)
		apiErr, ok := err.(*APIError)
		if !ok || apiErr.Status != 401 || apiErr.OAuth || !apiErr.TokenExpiry.IsZero() || apiErr.Error() != "GitLab GET user: HTTP 401: 401 Unauthorized" {
			t.Errorf("%s: got %#v", name, err)
		}
		if want := []string{"PRIVATE-TOKEN: old"}; !slices.Equal(*seen, want) {
			t.Errorf("%s: requests: got %q, want %q", name, *seen, want)
		}
	}
}

// The Renewer is only asked about a 401 that GitLab answered to the
// token: not about another status, and not about the 401 of another host
// after a redirect, which was not sent the token.
func TestClientAsksTheRenewerOnlyAboutItsToken(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer other.Close()
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/forbidden":
			w.WriteHeader(http.StatusForbidden)
		case "/api/v4/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/api/v4/failing":
			w.WriteHeader(http.StatusBadGateway)
		default:
			http.Redirect(w, r, other.URL+"/users/sign_in", http.StatusFound)
		}
	}))
	defer gl.Close()
	renewer := &fakeRenewer{next: []config.Host{{Token: "new"}}}
	c := New(config.Host{Name: "test", Token: "old", TokenSource: "config.yml"})
	c.BaseURL = gl.URL
	c.Renewer = renewer
	var out map[string]any
	for path, status := range map[string]int{"forbidden": 403, "missing": 404, "failing": 502, "elsewhere": 401} {
		if err := c.Get(context.Background(), path, nil, &out); !IsStatus(err, status) {
			t.Errorf("%s: got %v, want HTTP %d", path, err, status)
		}
	}
	if len(renewer.calls) != 0 {
		t.Errorf("the renewer was asked %d times, want 0", len(renewer.calls))
	}
}

// What the Renewer says of a token it did not give is kept in the error,
// for the hint; the error itself reads as before.
func TestClientKeepsWhyNoTokenWasTried(t *testing.T) {
	accept := "nothing"
	gl, seen := acceptOnly(t, &accept)
	why := fmt.Errorf("%w: something else", ErrLoginChanged)
	c := New(config.Host{Name: "test", Token: "old", TokenSource: "config.yml"})
	c.BaseURL = gl.URL
	c.Renewer = &fakeRenewer{why: why}
	var out map[string]any
	err := c.Get(context.Background(), "user", nil, &out)
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != 401 || apiErr.Renewal != why || apiErr.Error() != "GitLab GET user: HTTP 401: 401 Unauthorized" {
		t.Errorf("got %#v", err)
	}
	if len(*seen) != 1 {
		t.Errorf("requests: %q, want one", *seen)
	}
}
