package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maelvls/glcli/internal/cli"
	"github.com/maelvls/glcli/internal/config"
)

// These tests play glab's part by hand: no glab was run, and "glab renewed
// the token" means that the test wrote a new config.yml, in one step, with
// the keys glab writes (see internal/config/oauth_test.go).

// gate is a fake GitLab that accepts one token, in one header, and
// answers 401 to anything else, as GitLab does to an expired token.
type gate struct {
	mu     sync.Mutex
	accept string   // such as "Authorization: Bearer t" or "PRIVATE-TOKEN: t"
	seen   []string // what each request carried, in the same form
	// rejecting, when set, is called before each 401 is answered.
	rejecting func(seen int)
}

// sentToken is how a request carries its token.
func sentToken(r *http.Request) string {
	var out []string
	for _, v := range r.Header.Values("PRIVATE-TOKEN") {
		out = append(out, "PRIVATE-TOKEN: "+v)
	}
	for _, v := range r.Header.Values("Authorization") {
		out = append(out, "Authorization: "+v)
	}
	return strings.Join(out, " + ")
}

func (g *gate) handler(ok http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := sentToken(r)
		g.mu.Lock()
		g.seen = append(g.seen, got)
		n, accepted, rejecting := len(g.seen), got == g.accept, g.rejecting
		g.mu.Unlock()
		if accepted {
			ok(w, r)
			return
		}
		if rejecting != nil {
			rejecting(n)
		}
		unauthorized(w, r)
	}
}

func (g *gate) requests() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.seen)
}

func (g *gate) setAccept(accept string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.accept = accept
}

func currentUser(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/v4/user" {
		http.NotFound(w, r)
		return
	}
	fmt.Fprint(w, `{"username":"me","name":"Me"}`)
}

// countLoads counts how many times glab's config is read.
func countLoads(rt *cli.Runtime) *atomic.Int32 {
	var loads atomic.Int32
	load := rt.LoadConfig
	rt.LoadConfig = func() (*config.Config, error) {
		loads.Add(1)
		return load()
	}
	return &loads
}

func patConfig(token string) string {
	return `
host: gitlab.com
hosts:
    gitlab.com:
        api_protocol: https
        user: me
        token: ` + token + `
    code.pan.run:
        token: glpat-other-SECRET
        user: me
`
}

const (
	past   = "2020-01-02T03:04:05Z"
	future = "2099-01-02T03:04:05Z"
)

// GitLab answers 401, glab's config has another token by then: the request
// is made again with it, and nothing of this shows.
func TestRejectedTokenIsReadAgainFromTheConfig(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after string
		want          []string
	}{
		{"OAuth token renewed by glab",
			oauthConfig("oauth-OLD-SECRET", past), oauthConfig("oauth-NEW-SECRET", future),
			[]string{"Authorization: Bearer oauth-OLD-SECRET", "Authorization: Bearer oauth-NEW-SECRET"}},
		{"personal access token replaced by a new login",
			patConfig("glpat-OLD-SECRET"), patConfig("glpat-NEW-SECRET"),
			[]string{"PRIVATE-TOKEN: glpat-OLD-SECRET", "PRIVATE-TOKEN: glpat-NEW-SECRET"}},
		{"OAuth login replaced by a personal access token",
			oauthConfig("oauth-OLD-SECRET", past), patConfig("glpat-NEW-SECRET"),
			[]string{"Authorization: Bearer oauth-OLD-SECRET", "PRIVATE-TOKEN: glpat-NEW-SECRET"}},
		{"personal access token replaced by an OAuth login",
			patConfig("glpat-OLD-SECRET"), oauthConfig("oauth-NEW-SECRET", future),
			[]string{"PRIVATE-TOKEN: glpat-OLD-SECRET", "Authorization: Bearer oauth-NEW-SECRET"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &gate{accept: tc.want[1]}
			rt, stdout, stderr, path := fakeGitLabOnDisk(t, g.handler(currentUser), tc.before)
			loads := countLoads(rt)
			// glab renews the token while the request is on its way.
			g.rejecting = func(int) { writeGlabConfig(t, path, tc.after) }
			if code := run(rt, []string{"auth", "test"}); code != 0 {
				t.Fatalf("exit %d (stderr: %s)", code, stderr.String())
			}
			if got := stdout.String(); got != `{"host":"gitlab.com","username":"me","name":"Me"}`+"\n" {
				t.Errorf("got %s", got)
			}
			if stderr.Len() != 0 {
				t.Errorf("nothing expected on stderr, got %s", stderr.String())
			}
			if got := g.requests(); !slices.Equal(got, tc.want) {
				t.Errorf("requests:\n got %q\nwant %q", got, tc.want)
			}
			if n := loads.Load(); n != 2 {
				t.Errorf("the config was read %d times, want 2 (once, and once after the 401)", n)
			}
		})
	}
}

// GitLab answers 401 and glab's config still has the same token: the
// command fails as before, after one request, with the hint for the case.
func TestRejectedTokenUnchangedHints(t *testing.T) {
	oauthAdvice := []string{
		"glcli never renews it; glab does, when it calls GitLab: run `glab auth status --hostname gitlab.com`, then the same glcli command again (`mr wait` with the same --cursor misses nothing)",
		"If glab cannot renew it, run `glab auth login --hostname gitlab.com`",
	}
	for _, tc := range []struct {
		name   string
		config string
		env    map[string]string
		args   []string
		sent   string
		loads  int32
		want   []string
		not    []string
	}{
		{name: "personal access token", config: patConfig("glpat-SECRET"), sent: "PRIVATE-TOKEN: glpat-SECRET", loads: 2,
			want: []string{"glcli: the token was rejected: run `glab auth login --hostname gitlab.com` (glcli reads glab's config), or `glcli auth status`\n"},
			not:  []string{"OAuth", "expire", "glab auth status"}},
		{name: "personal access token of another host", config: patConfig("glpat-SECRET"), args: []string{"--host", "code.pan.run"}, sent: "PRIVATE-TOKEN: glpat-other-SECRET", loads: 2,
			want: []string{"glcli: the token was rejected: run `glab auth login --hostname code.pan.run` (glcli reads glab's config), or `glcli auth status`\n"},
			not:  []string{"OAuth", "expire", "glab auth status"}},
		{name: "expired OAuth token", config: oauthConfig("oauth-SECRET", past), sent: "Authorization: Bearer oauth-SECRET", loads: 2,
			want: append([]string{"glcli: the OAuth token of gitlab.com expired at 2020-01-02T03:04:05Z, and glab's config has no newer one. "}, oauthAdvice...)},
		{name: "expired OAuth token, in the zone glab wrote", config: oauthConfig("oauth-SECRET", "2020-01-02T04:04:05+01:00"), sent: "Authorization: Bearer oauth-SECRET", loads: 2,
			want: append([]string{"expired at 2020-01-02T04:04:05+01:00, and"}, oauthAdvice...)},
		{name: "OAuth token rejected before its expiry", config: oauthConfig("oauth-SECRET", future), sent: "Authorization: Bearer oauth-SECRET", loads: 2,
			want: append([]string{"glcli: the OAuth token of gitlab.com was rejected, although glab's config says that it expires at 2099-01-02T03:04:05Z. "}, oauthAdvice...),
			not:  []string{"expired at"}},
		{name: "OAuth token without an expiry", config: oauthConfig("oauth-SECRET", ""), sent: "Authorization: Bearer oauth-SECRET", loads: 2,
			want: append([]string{"glcli: the OAuth token of gitlab.com was rejected (glab's config does not say when it expires). "}, oauthAdvice...),
			not:  []string{"expired at"}},
		{name: "OAuth token with an expiry that cannot be read", config: oauthConfig("oauth-SECRET", "tomorrow"), sent: "Authorization: Bearer oauth-SECRET", loads: 2,
			want: append([]string{"(glab's config does not say when it expires)"}, oauthAdvice...),
			not:  []string{"tomorrow"}},
		// A token from the environment: glab's config is not read again,
		// logging in or running glab would change nothing.
		{name: "$GLCLI_TOKEN on a host glab logged in to with OAuth", config: oauthConfig("oauth-SECRET", past), env: map[string]string{"GLCLI_TOKEN": "glpat-env-SECRET"}, sent: "PRIVATE-TOKEN: glpat-env-SECRET", loads: 1,
			want: []string{"glcli: the token from $GLCLI_TOKEN was rejected by gitlab.com: unset it (glcli then reads glab's config) or replace it; `glcli auth status` shows where each token comes from\n"},
			not:  []string{"OAuth", "expire", "glab auth"}},
		{name: "$GITLAB_TOKEN", config: patConfig("glpat-SECRET"), env: map[string]string{"GITLAB_TOKEN": "glpat-env-SECRET"}, sent: "PRIVATE-TOKEN: glpat-env-SECRET", loads: 1,
			want: []string{"glcli: the token from $GITLAB_TOKEN was rejected by gitlab.com: unset it"},
			not:  []string{"OAuth", "expire", "glab auth"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &gate{accept: "nothing"}
			rt, stdout, stderr, _ := fakeGitLabOnDisk(t, g.handler(currentUser), tc.config)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			loads := countLoads(rt)
			if code := run(rt, append([]string{"auth", "test"}, tc.args...)); code != 1 {
				t.Fatalf("exit %d, want 1 (stderr: %s)", code, stderr.String())
			}
			got := stderr.String()
			if !strings.HasPrefix(got, "glcli: GitLab GET user: HTTP 401: 401 Unauthorized\nglcli: ") || strings.Count(got, "\n") != 2 {
				t.Errorf("want the error, then one line of hint, got:\n%s", got)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("want %q in:\n%s", want, got)
				}
			}
			for _, not := range append(tc.not, "SECRET", "refresh") {
				if strings.Contains(got, not) {
					t.Errorf("%q is not expected in:\n%s", not, got)
				}
			}
			if stdout.Len() != 0 {
				t.Errorf("nothing expected on stdout, got %s", stdout.String())
			}
			if got := g.requests(); !slices.Equal(got, []string{tc.sent}) {
				t.Errorf("want one request with %q, got %q", tc.sent, got)
			}
			if n := loads.Load(); n != tc.loads {
				t.Errorf("the config was read %d times, want %d", n, tc.loads)
			}
		})
	}
}

// The new token of the config is tried once: when GitLab rejects it too,
// the command fails with the hint for that token.
func TestRenewedTokenRejectedToo(t *testing.T) {
	g := &gate{accept: "nothing"}
	rt, _, stderr, path := fakeGitLabOnDisk(t, g.handler(currentUser), oauthConfig("oauth-OLD-SECRET", past))
	loads := countLoads(rt)
	g.rejecting = func(seen int) {
		if seen == 1 {
			writeGlabConfig(t, path, oauthConfig("oauth-NEW-SECRET", future))
		}
	}
	if code := run(rt, []string{"auth", "test"}); code != 1 {
		t.Fatalf("exit %d, want 1 (stderr: %s)", code, stderr.String())
	}
	if got, want := g.requests(), []string{"Authorization: Bearer oauth-OLD-SECRET", "Authorization: Bearer oauth-NEW-SECRET"}; !slices.Equal(got, want) {
		t.Errorf("requests:\n got %q\nwant %q", got, want)
	}
	if got := stderr.String(); !strings.Contains(got, "was rejected, although glab's config says that it expires at 2099-01-02T03:04:05Z") || strings.Contains(got, "SECRET") {
		t.Errorf("want the hint for the new token, got:\n%s", got)
	}
	if n := loads.Load(); n != 2 {
		t.Errorf("the config was read %d times, want 2", n)
	}
}

// When the config cannot be read again (half-edited by hand, say), the
// command fails with the 401 as if the token had not changed, and says
// nothing of what the file holds.
func TestRejectedTokenAndBrokenConfig(t *testing.T) {
	g := &gate{accept: "nothing"}
	rt, _, stderr, path := fakeGitLabOnDisk(t, g.handler(currentUser), oauthConfig("oauth-OLD-SECRET", past))
	g.rejecting = func(int) {
		writeGlabConfig(t, path, "hosts:\n    gitlab.com:\n        token: \"oauth-NEW-SECRET\n")
	}
	if code := run(rt, []string{"auth", "test"}); code != 1 {
		t.Fatalf("exit %d, want 1 (stderr: %s)", code, stderr.String())
	}
	got := stderr.String()
	if !strings.Contains(got, "glcli: GitLab GET user: HTTP 401: 401 Unauthorized\n") || !strings.Contains(got, "the OAuth token of gitlab.com expired at 2020-01-02T03:04:05Z") || strings.Contains(got, "SECRET") {
		t.Errorf("got:\n%s", got)
	}
	if n := len(g.requests()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

// A 401 from another host, after a redirect, says nothing about the
// token: glab's config is not read again.
func TestRedirectedRejectionDoesNotReadTheConfigAgain(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(unauthorized))
	defer other.Close()
	rt, _, stderr, _ := fakeGitLabOnDisk(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/users/sign_in", http.StatusFound)
	}, oauthConfig("oauth-SECRET", past))
	loads := countLoads(rt)
	if code := run(rt, []string{"auth", "test"}); code != 1 {
		t.Fatalf("exit %d, want 1 (stderr: %s)", code, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "where the token is not sent") || strings.Contains(got, "expired") {
		t.Errorf("got:\n%s", got)
	}
	if n := loads.Load(); n != 1 {
		t.Errorf("the config was read %d times, want 1", n)
	}
}

// mergeRequest answers what "mr get group/project!1" asks for. Its four
// first requests leave together.
func mergeRequest(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/merge_requests/1"):
		fmt.Fprint(w, `{"id":10,"iid":1,"project_id":5,"sha":"abc","state":"opened","title":"A change","web_url":"https://gitlab.com/group/project/-/merge_requests/1"}`)
	case strings.HasSuffix(r.URL.Path, "/merge_requests/1/approvals"):
		fmt.Fprint(w, `{}`)
	case strings.HasSuffix(r.URL.Path, "/merge_requests/1/discussions"), strings.HasSuffix(r.URL.Path, "/merge_requests/1/versions"):
		fmt.Fprint(w, `[]`)
	default:
		http.NotFound(w, r)
	}
}

// barrier holds the first n rejected requests until all n are there (or
// two seconds have passed): they were all sent before any was answered,
// as the requests of one burst are when GitLab is slower than glcli.
func barrier(t *testing.T, n int) func(seen int) {
	var once sync.Once
	all := make(chan struct{})
	return func(seen int) {
		if seen >= n {
			once.Do(func() { close(all) })
		}
		select {
		case <-all:
		case <-time.After(2 * time.Second):
			t.Errorf("only %d of the %d requests came together", seen, n)
		}
	}
}

// "mr get" sends four requests at once. When the four are rejected, the
// config is read again once, not four times, and the four are made again
// with the new token.
func TestConcurrentRejectionsReadTheConfigOnce(t *testing.T) {
	g := &gate{accept: "Authorization: Bearer oauth-NEW-SECRET"}
	rt, stdout, stderr, path := fakeGitLabOnDisk(t, g.handler(mergeRequest), oauthConfig("oauth-OLD-SECRET", past))
	loads := countLoads(rt)
	// glab renewed the token before the four requests were answered.
	wait := barrier(t, 4)
	var renew sync.Once
	g.rejecting = func(seen int) {
		wait(seen)
		renew.Do(func() { writeGlabConfig(t, path, oauthConfig("oauth-NEW-SECRET", future)) })
	}
	if code := run(rt, []string{"mr", "get", "group/project!1"}); code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, stderr.String())
	}
	var got struct {
		Title  string
		Cursor string
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || got.Title != "A change" || got.Cursor == "" {
		t.Errorf("want the merge request, got %s (%v)", stdout.String(), err)
	}
	if stderr.Len() != 0 {
		t.Errorf("nothing expected on stderr, got %s", stderr.String())
	}
	seen := g.requests()
	if len(seen) < 8 {
		t.Fatalf("want four rejected requests and the same four again, got %q", seen)
	}
	for i, s := range seen {
		if want := map[bool]string{true: "Authorization: Bearer oauth-OLD-SECRET", false: "Authorization: Bearer oauth-NEW-SECRET"}[i < 4]; s != want {
			t.Errorf("request %d: got %q, want %q", i+1, s, want)
		}
	}
	if n := loads.Load(); n != 2 {
		t.Errorf("the config was read %d times, want 2 (once, and once for the four 401s)", n)
	}
}

// The same four requests, rejected while the config still has the same
// token: it is read again once, and the command fails after four requests.
func TestConcurrentRejectionsWithTheSameTokenReadTheConfigOnce(t *testing.T) {
	g := &gate{accept: "nothing", rejecting: barrier(t, 4)}
	rt, stdout, stderr, _ := fakeGitLabOnDisk(t, g.handler(mergeRequest), oauthConfig("oauth-SECRET", past))
	loads := countLoads(rt)
	if code := run(rt, []string{"mr", "get", "group/project!1"}); code != 1 {
		t.Fatalf("exit %d, want 1 (stderr: %s)", code, stderr.String())
	}
	got := stderr.String()
	if !strings.Contains(got, "HTTP 401: 401 Unauthorized\n") || !strings.Contains(got, "the OAuth token of gitlab.com expired at 2020-01-02T03:04:05Z") || strings.Count(got, "\n") != 2 || strings.Contains(got, "SECRET") {
		t.Errorf("want the error and one hint, got:\n%s", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("nothing expected on stdout, got %s", stdout.String())
	}
	if seen := g.requests(); len(seen) != 4 {
		t.Errorf("want four requests and none made again, got %q", seen)
	}
	if n := loads.Load(); n != 2 {
		t.Errorf("the config was read %d times, want 2 (once, and once for the four 401s)", n)
	}
}

// minuteClock is a clock that moves one minute each time it is read: a
// wait command polls again and again without sleeping, and times out
// after a few polls.
func minuteClock(rt *cli.Runtime) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	rt.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(time.Minute)
		return now
	}
}

// A wait command started with a token that glab renews while it waits:
// the poll that gets the 401 reads the new token and goes on, and so do
// the polls after it. Here "pipeline wait" polls a merge request without
// a pipeline until --timeout.
func TestWaitSurvivesATokenRenewal(t *testing.T) {
	g := &gate{accept: "Authorization: Bearer oauth-OLD-SECRET"}
	// After the first poll (two requests), the old token expires and glab,
	// run by someone else, renews it: GitLab only takes the new one.
	var path string
	var polls atomic.Int32
	rt, stdout, stderr, p := fakeGitLabOnDisk(t, g.handler(func(w http.ResponseWriter, r *http.Request) {
		noPipeline(w, r)
		if strings.HasSuffix(r.URL.Path, "/pipelines") && polls.Add(1) == 1 {
			writeGlabConfig(t, path, oauthConfig("oauth-NEW-SECRET", future))
			g.setAccept("Authorization: Bearer oauth-NEW-SECRET")
		}
	}), oauthConfig("oauth-OLD-SECRET", future))
	path = p
	loads := countLoads(rt)
	minuteClock(rt)
	if code := run(rt, []string{"pipeline", "wait", "group/project!1", "--timeout", "10m"}); code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, stderr.String())
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || string(got["timed_out"]) != "true" || string(got["running"]) != "true" {
		t.Errorf("want the result of a wait that timed out, got %s (%v)", stdout.String(), err)
	}
	if stderr.Len() != 0 {
		t.Errorf("nothing expected on stderr, got %s", stderr.String())
	}
	seen := g.requests()
	polled := int(polls.Load())
	if polled < 3 {
		t.Fatalf("want at least three polls, got %d (%q)", polled, seen)
	}
	// Two requests with the old token, one rejected, and from then on the
	// new token only: the 401 happened once.
	old, renewed := "Authorization: Bearer oauth-OLD-SECRET", "Authorization: Bearer oauth-NEW-SECRET"
	if len(seen) != 2*polled+1 || !slices.Equal(seen[:3], []string{old, old, old}) {
		t.Fatalf("got %q", seen)
	}
	for _, s := range seen[3:] {
		if s != renewed {
			t.Fatalf("after the renewal, every request must carry the new token: got %q", seen)
		}
	}
	if n := loads.Load(); n != 2 {
		t.Errorf("the config was read %d times, want 2 (once, and once after the 401)", n)
	}
}

// The same wait, when nobody ran glab: the poll that gets the 401 ends
// the command, with the hint that says how to go on.
func TestWaitEndsOnAnExpiredTokenWithTheHint(t *testing.T) {
	g := &gate{accept: "Authorization: Bearer oauth-SECRET"}
	var polls atomic.Int32
	rt, stdout, stderr, _ := fakeGitLabOnDisk(t, g.handler(func(w http.ResponseWriter, r *http.Request) {
		noPipeline(w, r)
		if strings.HasSuffix(r.URL.Path, "/pipelines") && polls.Add(1) == 1 {
			g.setAccept("nothing")
		}
	}), oauthConfig("oauth-SECRET", past))
	minuteClock(rt)
	if code := run(rt, []string{"pipeline", "wait", "group/project!1", "--timeout", "10m"}); code != 1 {
		t.Fatalf("exit %d, want 1 (stderr: %s)", code, stderr.String())
	}
	got := stderr.String()
	if !strings.Contains(got, "HTTP 401: 401 Unauthorized\n") || !strings.Contains(got, "the OAuth token of gitlab.com expired at 2020-01-02T03:04:05Z") || !strings.Contains(got, "`glab auth status --hostname gitlab.com`") || strings.Contains(got, "SECRET") {
		t.Errorf("got:\n%s", got)
	}
	if stdout.Len() != 0 || len(g.requests()) != 3 {
		t.Errorf("want three requests and nothing on stdout, got %q and %s", g.requests(), stdout.String())
	}
}

// "auth status" tells when the OAuth token of a host has expired, and what
// renews it.
func TestAuthStatusHintsAtAnExpiredOAuthToken(t *testing.T) {
	for _, tc := range []struct {
		name string
		now  time.Time
		hint bool
	}{
		{"before the expiry", time.Date(2026, 11, 27, 18, 42, 9, 0, time.UTC), false},
		{"after the expiry", time.Date(2026, 11, 27, 18, 42, 11, 0, time.UTC), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, stdout, stderr, _ := fakeGitLabOnDisk(t, noRequest(t), oauthConfig("oauth-SECRET", "2026-11-27T19:42:10+01:00"))
			rt.Now = func() time.Time { return tc.now }
			if code := run(rt, []string{"auth", "status"}); code != 0 {
				t.Fatalf("exit %d (stderr: %s)", code, stderr.String())
			}
			var got struct{ Hints []string }
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("%v: %s", err, stdout.String())
			}
			want := []string{"The OAuth token of gitlab.com expired at 2026-11-27T19:42:10+01:00. glcli never renews it; glab does, when it calls GitLab: run `glab auth status --hostname gitlab.com` before the next glcli command."}
			if !tc.hint {
				want = nil
			}
			if !slices.Equal(got.Hints, want) || strings.Contains(stdout.String(), "SECRET") {
				t.Errorf("got %s", stdout.String())
			}
		})
	}
}
