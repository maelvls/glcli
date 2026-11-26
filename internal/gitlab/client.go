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
	"time"

	"github.com/maelvls/glcli/internal/config"
)

// Client talks to one GitLab instance.
type Client struct {
	Host config.Host
	HTTP *http.Client
	// Trace, when set, is called once per request with a one-line summary.
	Trace func(format string, args ...any)
	// BaseURL overrides Host.BaseURL(), for tests.
	BaseURL string
}

// New returns a client for h.
func New(h config.Host) *Client {
	return &Client{Host: h, HTTP: &http.Client{Timeout: 60 * time.Second, CheckRedirect: keepTokenOnHost}}
}

// keepTokenOnHost drops the token when a redirect leaves the host: Go
// only strips Authorization and cookies, not PRIVATE-TOKEN.
func keepTokenOnHost(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if req.URL.Host != via[0].URL.Host {
		req.Header.Del("PRIVATE-TOKEN")
	}
	return nil
}

// APIError is a non-2xx answer from GitLab.
type APIError struct {
	Method  string
	Path    string
	Status  int
	Message string
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

func (c *Client) do(ctx context.Context, path string, q url.Values) (*http.Response, error) {
	u := c.base() + "/api/v4/" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.Host.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "glcli")
	start := time.Now()
	resp, err := c.HTTP.Do(req)
	if c.Trace != nil {
		status := "error"
		if resp != nil {
			status = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		c.Trace("GET %s %s: %s, %s", c.Host.Name, path, time.Since(start).Round(time.Millisecond), status)
	}
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &APIError{Method: http.MethodGet, Path: path, Status: resp.StatusCode, Message: errorMessage(body)}
	}
	return resp, nil
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
