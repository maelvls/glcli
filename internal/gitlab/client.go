// Package gitlab is a small read-mostly client for the GitLab REST API v4.
package gitlab

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maelvls/glcli/internal/config"
)

// Client talks to one GitLab instance.
type Client struct {
	// Host is the host as it was when the client was made, and is never
	// written to afterwards. Once Renewer gave another token, requests
	// carry that one, not Host.Token.
	Host config.Host
	HTTP *http.Client
	// Trace, when set, is called once per request with a one-line summary.
	Trace func(format string, args ...any)
	// BaseURL overrides Host.BaseURL(), for tests.
	BaseURL string
	// Renewer, when set, is asked for another token when GitLab answers
	// 401: the request is then made once more with it. The client itself
	// never renews a token.
	Renewer Renewer

	mu      sync.Mutex
	renewed *config.Host // Host with the last token Renewer gave
}

// A Renewer knows whether the token of a host was replaced since a client
// read it: glab renews the token of an OAuth login when it expires, and
// writes the new one to its config. cli.Runtime is one, by reading that
// config again.
type Renewer interface {
	// Generation is read before each request and handed back to Renew
	// with the answer to it: it tells Renew which requests left before it
	// last looked for new tokens.
	Generation() int
	// Renew is called after GitLab answered 401 to a request made with
	// the token of rejected. It returns the host with the token to use
	// instead and true, or false when there is no other token. It can be
	// called by several goroutines at once.
	Renew(rejected config.Host, generation int) (config.Host, bool)
}

// New returns a client for h.
func New(h config.Host) *Client {
	return &Client{Host: h, HTTP: &http.Client{Timeout: 60 * time.Second, CheckRedirect: keepTokenOnHost}}
}

// keepTokenOnHost drops the token, whichever header it is in, when a
// redirect leaves the host or goes from https to http on it. Go only
// strips Authorization and cookies, not PRIVATE-TOKEN, keeps them for a
// subdomain and another port, and does not look at the scheme.
func keepTokenOnHost(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if leavesHost(via[0].URL, req.URL) {
		req.Header.Del("PRIVATE-TOKEN")
		req.Header.Del("Authorization")
	}
	return nil
}

// leavesHost says whether a request first sent to from must go to to
// without the token: to is another host (or another port), or the same
// one over http when the token was first sent over https, where it would
// travel in clear.
func leavesHost(from, to *url.URL) bool {
	return to.Host != from.Host || from.Scheme == "https" && to.Scheme != "https"
}

// APIError is a non-2xx answer from GitLab.
type APIError struct {
	// Host is the name of the host as glab knows it (config.Host.Name),
	// for hints that tell which host to log in to.
	Host    string
	Method  string
	Path    string
	Status  int
	Message string

	// TokenSource is where the token that was sent came from
	// (config.Host.TokenSource): glab's config, or "$GLCLI_TOKEN" or
	// "$GITLAB_TOKEN".
	TokenSource string
	// RedirectedTo is the host that answered, when a redirect left the
	// host of the request: it was not sent the token (see keepTokenOnHost).
	// When the token was not sent to the host of the request itself (a
	// redirect from https to http on it), it is that host with the scheme,
	// such as "http://gitlab.example.com".
	RedirectedTo string
	// OAuth says that the token that was sent is the OAuth access token of
	// a login of glab (config.Host.OAuth), and TokenExpiry when glab's
	// config says it expires (config.Host.Expiry; zero when it does not).
	OAuth       bool
	TokenExpiry time.Time
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("GitLab %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, msg)
}

// IsTransient reports whether err is worth retrying later: the network is
// down, the request timed out, GitLab is rate limiting or failing.
// A cancelled context is not transient.
func IsTransient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusTooManyRequests || apiErr.Status >= 500
	}
	// A certificate GitLab's host is not trusted with will not fix itself.
	var (
		unknownAuthority x509.UnknownAuthorityError
		hostname         x509.HostnameError
		invalid          x509.CertificateInvalidError
		verification     *tls.CertificateVerificationError
	)
	if errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &invalid) || errors.As(err, &verification) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded)
}

// IsStatus reports whether err is an APIError with this HTTP status.
func IsStatus(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == status
}

// Project returns the API path of a project: "projects/" followed by its
// numeric ID or its URL-encoded full path.
func Project(pathOrID string) string {
	return "projects/" + url.PathEscape(pathOrID)
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return strings.TrimSuffix(c.BaseURL, "/")
	}
	return c.Host.BaseURL()
}

// host is the host with the token to send now: the last one Renewer gave,
// or else the one the client was made with.
func (c *Client) host() config.Host {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.renewed != nil {
		return *c.renewed
	}
	return c.Host
}

// do sends GET path. When GitLab answers 401 and Renewer has another token
// for the host, it sends the request once more with that token, and the
// requests that follow use it too: a command that waits for hours goes on
// after glab renewed the token it started with.
func (c *Client) do(ctx context.Context, path string, q url.Values) (*http.Response, error) {
	h, generation := c.host(), 0
	if c.Renewer != nil {
		generation = c.Renewer.Generation()
	}
	resp, err := c.send(ctx, h, path, q)
	var apiErr *APIError
	// A 401 from where the token was not sent says nothing about the token.
	if c.Renewer == nil || !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized || apiErr.RedirectedTo != "" {
		return resp, err
	}
	other, ok := c.Renewer.Renew(h, generation)
	if !ok || other.Token == h.Token {
		return nil, err
	}
	// The same host, with the other token and what goes with it.
	h.Token, h.OAuth, h.Expiry = other.Token, other.OAuth, other.Expiry
	c.mu.Lock()
	c.renewed = &h
	c.mu.Unlock()
	if c.Trace != nil {
		c.Trace("GET %s %s: the token was rejected and there is another one for this host: trying again with it", h.Name, path)
	}
	return c.send(ctx, h, path, q)
}

// send sends GET path with the token of h.
func (c *Client) send(ctx context.Context, h config.Host, path string, q url.Values) (*http.Response, error) {
	u := c.base() + "/api/v4/" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	authorize(req, h)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "glcli")
	start := time.Now()
	resp, err := c.HTTP.Do(req)
	if c.Trace != nil {
		status := "error"
		if resp != nil {
			status = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		c.Trace("GET %s %s: %s, %s", h.Name, path, time.Since(start).Round(time.Millisecond), status)
	}
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		apiErr := &APIError{Host: h.Name, TokenSource: h.TokenSource, OAuth: h.OAuth, TokenExpiry: h.Expiry, Method: http.MethodGet, Path: path, Status: resp.StatusCode, Message: errorMessage(body)}
		// On the host of the request, the request that was answered tells
		// whether the token went with it: not after a redirect to http,
		// and Go does not put Authorization back on a redirect that comes
		// back to the host after leaving it.
		if final := resp.Request; final.URL.Host != req.URL.Host {
			apiErr.RedirectedTo = final.URL.Host
		} else if h.Token != "" && final.Header.Get("PRIVATE-TOKEN") == "" && final.Header.Get("Authorization") == "" {
			apiErr.RedirectedTo = final.URL.Scheme + "://" + final.URL.Host
		}
		return nil, apiErr
	}
	return resp, nil
}

// authorize puts the token of h in the header GitLab reads it from: an
// OAuth 2.0 access token (a host glab logged in to with OAuth) goes in
// "Authorization: Bearer", since GitLab looks for a personal, project or
// group access token in PRIVATE-TOKEN and answers 401 to anything else
// there. Every other token goes in PRIVATE-TOKEN, as it always did.
func authorize(req *http.Request, h config.Host) {
	if h.OAuth {
		req.Header.Set("Authorization", "Bearer "+h.Token)
		return
	}
	req.Header.Set("PRIVATE-TOKEN", h.Token)
}

func errorMessage(body []byte) string {
	var v struct {
		Message any    `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &v) == nil {
		switch m := v.Message.(type) {
		case string:
			return m
		case nil:
		default:
			b, _ := json.Marshal(m)
			return string(b)
		}
		if v.Error != "" {
			return v.Error
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// Get decodes the JSON answer of GET path into out.
func (c *Client) Get(ctx context.Context, path string, q url.Values, out any) error {
	_, err := c.get(ctx, path, q, out)
	return err
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) (http.Header, error) {
	resp, err := c.do(ctx, path, q)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return nil, fmt.Errorf("GitLab GET %s: decoding the answer: %w", path, err)
	}
	return resp.Header, nil
}

// GetText returns the body of GET path, at most limit bytes from its end
// when limit > 0 (job logs can be very long).
func (c *Client) GetText(ctx context.Context, path string, limit int64) (string, error) {
	resp, err := c.do(ctx, path, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if limit <= 0 {
		b, err := io.ReadAll(resp.Body)
		return string(b), err
	}
	// Keep a sliding window of the last limit bytes.
	buf := make([]byte, 0, limit)
	chunk := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if int64(len(buf)) > limit {
			buf = buf[int64(len(buf))-limit:]
		}
		if err == io.EOF {
			return string(buf), nil
		}
		if err != nil {
			return string(buf), err
		}
	}
}

// GetAll follows GitLab's pagination (100 per page) and returns every
// item, or the first maxPages pages when maxPages > 0, in which case
// truncated says whether more were left.
func GetAll[T any](ctx context.Context, c *Client, path string, q url.Values, maxPages int) (items []T, truncated bool, err error) {
	q = cloneValues(q)
	q.Set("per_page", "100")
	page := 1
	for {
		q.Set("page", strconv.Itoa(page))
		var batch []T
		h, err := c.get(ctx, path, q, &batch)
		if err != nil {
			return nil, false, err
		}
		items = append(items, batch...)
		next := h.Get("X-Next-Page")
		if next == "" || len(batch) == 0 {
			return items, false, nil
		}
		if maxPages > 0 && page >= maxPages {
			return items, true, nil
		}
		n, err := strconv.Atoi(next)
		if err != nil || n <= page {
			return items, false, nil
		}
		page = n
	}
}

func cloneValues(q url.Values) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}
