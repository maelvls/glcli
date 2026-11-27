package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/maelvls/glcli/internal/config"
)

// The two kinds of host: the token of one goes in PRIVATE-TOKEN, the
// token of the other in Authorization.
var redirectHosts = []struct {
	kind string
	host config.Host
}{
	{"access token", config.Host{Name: "test", Token: "secret", TokenSource: "config.yml"}},
	{"OAuth token", config.Host{Name: "test", Token: "secret", TokenSource: "config.yml", OAuth: true}},
}

// tokenHeaders is what a request carries of the token, in either header.
func tokenHeaders(r *http.Request) string {
	return strings.Join(append(r.Header.Values("PRIVATE-TOKEN"), r.Header.Values("Authorization")...), ",")
}

func mustParse(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// keepTokenOnHost on its own, with both headers on the redirected
// request, as Go leaves them there.
func TestKeepTokenOnHost(t *testing.T) {
	for _, tc := range []struct {
		name string
		via  []string
		to   string
		keep bool
	}{
		{"same host", []string{"https://gitlab.com/api/v4/a"}, "https://gitlab.com/api/v4/b", true},
		{"https to http on the same host", []string{"https://gitlab.com/api/v4/a"}, "http://gitlab.com/api/v4/a", false},
		{"https to another scheme on the same host", []string{"https://gitlab.com/api/v4/a"}, "ftp://gitlab.com/a", false},
		{"another host", []string{"https://gitlab.com/api/v4/a"}, "https://example.com/api/v4/a", false},
		{"a subdomain", []string{"https://gitlab.com/api/v4/a"}, "https://sub.gitlab.com/api/v4/a", false},
		{"the parent domain", []string{"https://sub.gitlab.com/api/v4/a"}, "https://gitlab.com/api/v4/a", false},
		{"another port", []string{"https://gitlab.com/api/v4/a"}, "https://gitlab.com:8443/api/v4/a", false},
		{"another host over http", []string{"https://gitlab.com/api/v4/a"}, "http://example.com/a", false},
		{"http on the same host, for a host glab reaches over http", []string{"http://gitlab.internal/api/v4/a"}, "http://gitlab.internal/api/v4/b", true},
		{"http to https on the same host", []string{"http://gitlab.internal/api/v4/a"}, "https://gitlab.internal/api/v4/a", true},
		{"http to another host", []string{"http://gitlab.internal/api/v4/a"}, "http://example.com/a", false},
		{"to http after another host", []string{"https://gitlab.com/a", "https://example.com/a"}, "http://gitlab.com/a", false},
		{"to http after a redirect on the host", []string{"https://gitlab.com/a", "https://gitlab.com/b"}, "http://gitlab.com/c", false},
		{"back to the host over https, after http", []string{"https://gitlab.com/a", "http://gitlab.com/a"}, "https://gitlab.com/b", true},
		{"back to the host, after another host", []string{"https://gitlab.com/a", "https://example.com/a"}, "https://gitlab.com/b", true},
		{"from another host to a third one", []string{"https://gitlab.com/a", "https://example.com/a"}, "https://example.org/a", false},
	} {
		var via []*http.Request
		for _, u := range tc.via {
			via = append(via, &http.Request{URL: mustParse(t, u), Header: http.Header{}})
		}
		req := &http.Request{URL: mustParse(t, tc.to), Header: http.Header{}}
		req.Header.Set("PRIVATE-TOKEN", "secret")
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("Accept", "application/json")
		if err := keepTokenOnHost(req, via); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		got := tokenHeaders(req)
		if tc.keep && got != "secret,Bearer secret" {
			t.Errorf("%s: the token must be kept, got %q", tc.name, got)
		}
		if !tc.keep && got != "" {
			t.Errorf("%s: the token must be dropped, got %q", tc.name, got)
		}
		if req.Header.Get("Accept") != "application/json" {
			t.Errorf("%s: the other headers must be kept, got %v", tc.name, req.Header)
		}
	}
	// Ten redirects is the end, as with Go's own policy.
	via := make([]*http.Request, 10)
	for i := range via {
		via[i] = &http.Request{URL: mustParse(t, "https://gitlab.com/a"), Header: http.Header{}}
	}
	if err := keepTokenOnHost(&http.Request{URL: mustParse(t, "https://gitlab.com/b"), Header: http.Header{}}, via); err == nil {
		t.Error("expected an error after 10 redirects")
	}
}

// Neither header follows a redirect to another host, and both follow a
// redirect on the host.
func TestRedirectToAnotherHostDropsBothHeaders(t *testing.T) {
	for _, tc := range redirectHosts {
		t.Run(tc.kind, func(t *testing.T) {
			var leaked, same string
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				leaked = tokenHeaders(r)
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"message":"401 Unauthorized"}`)
			}))
			defer other.Close()
			gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v4/same":
					same = tokenHeaders(r)
					fmt.Fprint(w, `{}`)
				case "/api/v4/redirect-same":
					http.Redirect(w, r, "/api/v4/same", http.StatusFound)
				default:
					http.Redirect(w, r, other.URL+"/users/sign_in", http.StatusFound)
				}
			}))
			defer gl.Close()
			c := New(tc.host)
			c.BaseURL = gl.URL
			var out map[string]any
			err := c.Get(context.Background(), "elsewhere", nil, &out)
			if leaked != "" {
				t.Errorf("the token was sent to another host: %q", leaked)
			}
			// What PR #6 reports of such an answer still holds.
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != 401 || apiErr.RedirectedTo != strings.TrimPrefix(other.URL, "http://") || apiErr.Host != "test" || apiErr.TokenSource != "config.yml" {
				t.Errorf("want a 401 redirected to %s, got %#v", other.URL, err)
			}
			if err := c.Get(context.Background(), "redirect-same", nil, &out); err != nil {
				t.Fatal(err)
			}
			if want := map[bool]string{false: "secret", true: "Bearer secret"}[tc.host.OAuth]; same != want {
				t.Errorf("a redirect on the host must keep the token: got %q, want %q", same, want)
			}
		})
	}
}

// twoSchemes makes one host name, gitlab.test, of two test servers: one
// for https://gitlab.test and one for http://gitlab.test.
type twoSchemes struct{ https, http *httptest.Server }

func (s twoSchemes) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "gitlab.test" {
		return nil, fmt.Errorf("unexpected host %s", req.URL.Host)
	}
	srv := s.http
	if req.URL.Scheme == "https" {
		srv = s.https
	}
	out := req.Clone(req.Context())
	out.URL.Host = strings.TrimPrefix(strings.TrimPrefix(srv.URL, "https://"), "http://")
	resp, err := srv.Client().Transport.RoundTrip(out)
	if resp != nil {
		resp.Request = req
	}
	return resp, err
}

// A redirect from https to http on the same host would send the token in
// clear: it is dropped, and a 401 that follows is reported as one that
// says nothing about the token.
func TestRedirectFromHTTPSToHTTPDropsBothHeaders(t *testing.T) {
	for _, tc := range redirectHosts {
		t.Run(tc.kind, func(t *testing.T) {
			var clear []string
			plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				clear = append(clear, r.URL.Path+" "+tokenHeaders(r))
				if r.URL.Path == "/api/v4/back" {
					http.Redirect(w, r, "https://gitlab.test/api/v4/secure", http.StatusFound)
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"message":"401 Unauthorized"}`)
			}))
			defer plain.Close()
			var secure []string
			tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				secure = append(secure, r.URL.Path+" "+tokenHeaders(r))
				switch r.URL.Path {
				case "/api/v4/down":
					http.Redirect(w, r, "http://gitlab.test/api/v4/clear", http.StatusFound)
				case "/api/v4/down-and-back":
					http.Redirect(w, r, "http://gitlab.test/api/v4/back", http.StatusFound)
				case "/api/v4/stay":
					http.Redirect(w, r, "https://gitlab.test/api/v4/secure", http.StatusFound)
				default:
					fmt.Fprint(w, `{}`)
				}
			}))
			defer tlsServer.Close()

			sent := map[bool]string{false: "secret", true: "Bearer secret"}[tc.host.OAuth]
			c := New(tc.host)
			c.BaseURL = "https://gitlab.test"
			c.HTTP.Transport = twoSchemes{https: tlsServer, http: plain}
			var out map[string]any

			// https, then http: no token over http.
			err := c.Get(context.Background(), "down", nil, &out)
			if want := []string{"/api/v4/down " + sent}; fmt.Sprint(secure) != fmt.Sprint(want) {
				t.Errorf("over https: got %q, want %q", secure, want)
			}
			if want := []string{"/api/v4/clear "}; fmt.Sprint(clear) != fmt.Sprint(want) {
				t.Errorf("the token was sent in clear: got %q, want %q", clear, want)
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != 401 || apiErr.RedirectedTo != "http://gitlab.test" {
				t.Errorf("want a 401 redirected to http://gitlab.test, got %#v", err)
			}

			// https to https on the host: the token stays.
			secure, clear = nil, nil
			if err := c.Get(context.Background(), "stay", nil, &out); err != nil {
				t.Fatal(err)
			}
			if want := []string{"/api/v4/stay " + sent, "/api/v4/secure " + sent}; fmt.Sprint(secure) != fmt.Sprint(want) || len(clear) != 0 {
				t.Errorf("got %q over https and %q over http, want %q and nothing", secure, clear, want)
			}

			// https, http, then https again: never in clear. Back on https,
			// PRIVATE-TOKEN is sent again; Go itself does not put
			// Authorization back once it has dropped it.
			secure, clear = nil, nil
			err = c.Get(context.Background(), "down-and-back", nil, &out)
			if want := []string{"/api/v4/back "}; fmt.Sprint(clear) != fmt.Sprint(want) {
				t.Errorf("the token was sent in clear: got %q, want %q", clear, want)
			}
			if len(secure) != 2 || secure[0] != "/api/v4/down-and-back "+sent || (secure[1] != "/api/v4/secure "+sent && secure[1] != "/api/v4/secure ") {
				t.Errorf("over https: got %q", secure)
			}
			if err != nil {
				t.Errorf("the last answer is a 200: %v", err)
			}
		})
	}
}

// The clients New makes follow redirects with keepTokenOnHost.
func TestNewChecksRedirects(t *testing.T) {
	c := New(config.Host{Name: "test", Token: "secret"})
	req := &http.Request{URL: mustParse(t, "http://gitlab.com/a"), Header: http.Header{"Private-Token": {"secret"}, "Authorization": {"Bearer secret"}}}
	via := []*http.Request{{URL: mustParse(t, "https://gitlab.com/a"), Header: http.Header{}}}
	if c.HTTP.CheckRedirect == nil {
		t.Fatal("no redirect policy")
	}
	if err := c.HTTP.CheckRedirect(req, via); err != nil || tokenHeaders(req) != "" {
		t.Errorf("got %q, %v", tokenHeaders(req), err)
	}
}
