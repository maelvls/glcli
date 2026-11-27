package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maelvls/glcli/internal/cli"
	"github.com/maelvls/glcli/internal/config"
	"github.com/maelvls/glcli/internal/gitlab"
)

// followGate sits in front of a fake GitLab: it can make the requests of
// the merge request (or the list of the inbox) fail for a while, with a
// transient (503) or a fatal (403) error, without the fake behind it
// counting them.
type followGate struct {
	mu sync.Mutex
	// match says which requests the gate may fail.
	match func(r *http.Request) bool
	// fail says, for the nth matching request (from 1), the status to
	// answer instead of the fake (0: let it through).
	fail func(n int) int
	// before, when set, is called with the nth matching request before
	// anything else (outside the lock).
	before func(n int, r *http.Request)
	n      int
	next   http.Handler
}

func (g *followGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	status := 0
	if g.match != nil && g.match(r) {
		g.n++
		if g.fail != nil {
			status = g.fail(g.n)
		}
	}
	n := g.n
	g.mu.Unlock()
	if g.before != nil && g.match != nil && g.match(r) {
		g.before(n, r)
	}
	if status != 0 {
		http.Error(w, `{"message":"followGate"}`, status)
		return
	}
	g.next.ServeHTTP(w, r)
}

// followRun is what one run of glcli printed.
type followRun struct {
	code   int
	stdout string
	stderr string
	// lines are the lines of stdout, each decoded on its own.
	lines []map[string]json.RawMessage
}

// types returns the "type" of each line.
func (r followRun) types() []string {
	var out []string
	for _, l := range r.lines {
		var s string
		_ = json.Unmarshal(l["type"], &s)
		out = append(out, s)
	}
	return out
}

// str returns the string field key of line i ("" when absent).
func (r followRun) str(i int, key string) string {
	var s string
	_ = json.Unmarshal(r.lines[i][key], &s)
	return s
}

// raw returns the lines of stdout.
func (r followRun) raw() []string {
	return strings.Split(strings.TrimRight(r.stdout, "\n"), "\n")
}

// runFollow runs glcli with args on rt and checks that every line of
// stdout is a JSON object on its own line.
func runFollow(t *testing.T, rt *cli.Runtime, stdout, stderr *bytes.Buffer, args ...string) followRun {
	t.Helper()
	start := time.Now()
	code := run(rt, args)
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("glcli %s took %s: it slept for real", strings.Join(args, " "), took)
	}
	r := followRun{code: code, stdout: stdout.String(), stderr: stderr.String()}
	if r.stdout == "" {
		return r
	}
	for _, line := range r.raw() {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("glcli %s: line %q is not a JSON object: %v\nstdout: %s", strings.Join(args, " "), line, err, r.stdout)
		}
		r.lines = append(r.lines, m)
	}
	return r
}

// mrFollowRuntime returns a runtime for the merge request of f, whose
// requests go through gate (nil: straight to f), with f's clock.
func mrFollowRuntime(t *testing.T, f *mrWaitFake, gate *followGate) (*cli.Runtime, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	url := f.srv.URL
	if gate != nil {
		gate.next = f
		srv := httptest.NewServer(gate)
		t.Cleanup(srv.Close)
		url = srv.URL
	}
	var stdout, stderr bytes.Buffer
	rt := cli.New(&stdout, &stderr)
	rt.Now = f.now
	rt.LoadConfig = func() (*config.Config, error) {
		return &config.Config{Path: "test", DefaultHost: f.host, Hosts: map[string]config.Host{
			f.host: {Name: f.host, APIHost: f.host, Protocol: "https", Token: "x", User: "me", TokenSource: "test"},
		}}, nil
	}
	rt.NewClient = func(h config.Host) *gitlab.Client {
		c := gitlab.New(h)
		c.BaseURL = url
		return c
	}
	rt.Remote = func() (string, string, error) { return "", "", errors.New("not in a checkout") }
	return rt, &stdout, &stderr
}

// mrFollow runs "mr wait g/p!1 --interval 5s" with args against f.
func mrFollow(t *testing.T, f *mrWaitFake, gate *followGate, args ...string) followRun {
	t.Helper()
	rt, stdout, stderr := mrFollowRuntime(t, f, gate)
	return runFollow(t, rt, stdout, stderr, append([]string{"mr", "wait", "g/p!1", "--interval", "5s"}, args...)...)
}

// isMRFetch matches the request of the merge request itself.
func isMRFetch(r *http.Request) bool {
	return r.URL.Path == "/api/v4/projects/g/p/merge_requests/1"
}

// inboxFollowRuntime returns a runtime for the inbox of h, whose
// requests go through gate (nil: straight to h), with the stepping clock
// of stepClock (a minute per reading, from 2026-11-27 12:00 UTC).
func inboxFollowRuntime(t *testing.T, h http.Handler, gate *followGate) (*cli.Runtime, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	if gate != nil {
		gate.next = h
		h = gate
	}
	rt, stdout, stderr := fakeGitLab(t, h.ServeHTTP)
	stepClock(rt)
	return rt, stdout, stderr
}

// isInboxList matches the list of the merge requests: one per poll.
func isInboxList(r *http.Request) bool { return r.URL.Path == "/api/v4/merge_requests" }
