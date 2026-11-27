package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maelvls/glcli/internal/cli"
	"github.com/maelvls/glcli/internal/config"
	"github.com/maelvls/glcli/internal/gitlab"
)

// stepClock moves forward by step each time it is read. With a step of at
// least --interval, Poll never sleeps for real (each sleep is already over
// when it looks at the clock), and the time that passes in a test is the
// number of times the command read the clock.
type stepClock struct {
	mu   sync.Mutex
	now  time.Time
	step time.Duration
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(c.step)
	return c.now
}

// peek reads the clock without moving it.
func (c *stepClock) peek() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// waitMR is the API path of the merge request the tests wait on.
const waitMR = "projects/group/project/merge_requests/1"

// waitEnv runs "pipeline wait group/project!1" against a fake GitLab, with
// a stepClock.
type waitEnv struct {
	rt             *cli.Runtime
	clock          *stepClock
	stdout, stderr bytes.Buffer

	mu      sync.Mutex
	seen    map[string]int
	queries map[string][]url.Values
}

// newWaitEnv starts a fake GitLab that answers GET /api/v4/<path> with
// answer(path): a body and an HTTP status. One request is answered at a
// time, so answer can keep state without locking.
func newWaitEnv(t *testing.T, answer func(path string) (body string, status int)) *waitEnv {
	t.Helper()
	for _, env := range []string{"GLCLI_HOST", "GITLAB_HOST", "GLCLI_TOKEN", "GITLAB_TOKEN", "GLCLI_USER", "GLCLI_COMMAND_TIMEOUT_MS"} {
		t.Setenv(env, "")
	}
	e := &waitEnv{
		clock:   &stepClock{now: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC), step: 10 * time.Second},
		seen:    map[string]int{},
		queries: map[string][]url.Values{},
	}
	total := 0
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/api/v4/")
		e.seen[path]++
		e.queries[path] = append(e.queries[path], r.URL.Query())
		// A wait that never ends must fail the test, not hang it: HTTP
		// 400 is an error that ends the poll.
		if total++; total > 2000 {
			http.Error(w, `{"message":"the test polled 2000 times: the wait does not end"}`, http.StatusBadRequest)
			return
		}
		body, status := answer(path)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(gl.Close)
	e.rt = cli.New(&e.stdout, &e.stderr)
	e.rt.Debug = false
	e.rt.Now = e.clock.Now
	e.rt.LoadConfig = func() (*config.Config, error) {
		return &config.Config{Path: "config.yml", DefaultHost: "gitlab.com", Hosts: map[string]config.Host{
			"gitlab.com": {Name: "gitlab.com", APIHost: "gitlab.com", Protocol: "https", Token: "t", TokenSource: "config.yml"},
		}}, nil
	}
	e.rt.Remote = func() (string, string, error) { return "", "", errors.New("not a git checkout") }
	e.rt.NewClient = func(h config.Host) *gitlab.Client {
		c := gitlab.New(h)
		c.BaseURL = gl.URL
		return c
	}
	return e
}

// count is how many times the API path was requested.
func (e *waitEnv) count(path string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.seen[path]
}

// wait runs the command (polling every 5s of the stepClock) and returns
// its exit code and the JSON it printed.
func (e *waitEnv) wait(t *testing.T, args ...string) (code int, out map[string]json.RawMessage) {
	t.Helper()
	code = run(e.rt, append([]string{"pipeline", "wait", "group/project!1", "--interval", "5s"}, args...))
	if e.stdout.Len() > 0 {
		if err := json.Unmarshal(e.stdout.Bytes(), &out); err != nil {
			t.Fatalf("%v: %s", err, e.stdout.String())
		}
	}
	return code, out
}

// noPipelineAnswers is a merge request whose head commit never gets a
// pipeline.
func noPipelineAnswers(path string) (string, int) {
	switch path {
	case waitMR:
		return `{"iid":1,"project_id":5,"sha":"aaa111"}`, 200
	case waitMR + "/pipelines":
		return `[]`, 200
	}
	return `{"message":"404 Not Found"}`, 404
}

func TestPipelineWaitStopsWhenNoPipelineIsCreated(t *testing.T) {
	e := newWaitEnv(t, noPipelineAnswers)
	start := e.clock.peek()
	// No flag: by default, the wait for a pipeline to be created ends.
	code, out := e.wait(t)
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	line := e.stdout.String()
	if string(out["no_pipeline"]) != "true" || string(out["sha"]) != `"aaa111"` || string(out["failed_jobs"]) != "[]" || string(out["running"]) != "false" {
		t.Errorf("want no_pipeline, the sha, running false and an empty failed_jobs, got %s", line)
	}
	if _, ok := out["timed_out"]; ok {
		t.Errorf("no_pipeline and timed_out must not both be set: %s", line)
	}
	if !strings.Contains(string(out["hints"]), "skip ci") {
		t.Errorf("want a hint with the likely causes, got %s", line)
	}
	if n := strings.Count(e.stderr.String(), "waiting up to 5m0s for GitLab to create it"); n != 1 {
		t.Errorf("want one note on stderr when the wait for the pipeline starts, got %d: %s", n, e.stderr.String())
	}
	if waited := e.clock.peek().Sub(start); waited < defaultCreateTimeout {
		t.Errorf("gave up after %s, before --create-timeout", waited)
	}
	if n := e.count(waitMR); n < 2 {
		t.Errorf("want several polls before giving up, got %d", n)
	}
}

// Whichever of --timeout and --create-timeout comes first decides, and
// only one of timed_out and no_pipeline is printed.
func TestPipelineWaitTimeoutAndCreateTimeout(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string // the field expected to be true, "" for either
	}{
		{"timeout first", []string{"--timeout", "1m", "--create-timeout", "10m"}, "timed_out"},
		{"create-timeout first", []string{"--timeout", "10m", "--create-timeout", "1m"}, "no_pipeline"},
		{"create-timeout 0 waits until timeout", []string{"--timeout", "30m", "--create-timeout", "0"}, "timed_out"},
		{"both at once", []string{"--timeout", "2m", "--create-timeout", "2m"}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newWaitEnv(t, noPipelineAnswers)
			code, out := e.wait(t, tt.args...)
			if code != 0 {
				t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
			}
			line := e.stdout.String()
			timedOut, noPipeline := string(out["timed_out"]) == "true", string(out["no_pipeline"]) == "true"
			if timedOut == noPipeline {
				t.Fatalf("want exactly one of timed_out and no_pipeline, got %s", line)
			}
			if tt.want != "" && string(out[tt.want]) != "true" {
				t.Errorf("want %s, got %s", tt.want, line)
			}
			// On timeout the pipeline may still come: it is not "no pipeline".
			if wantRunning := map[bool]string{true: "true", false: "false"}[timedOut]; string(out["running"]) != wantRunning {
				t.Errorf("want running %s, got %s", wantRunning, line)
			}
			if string(out["sha"]) != `"aaa111"` || string(out["failed_jobs"]) != "[]" || len(out["hints"]) == 0 {
				t.Errorf("want the sha, an empty failed_jobs and a hint, got %s", line)
			}
		})
	}
}

func TestPipelineWaitRejectsNegativeCreateTimeout(t *testing.T) {
	e := newWaitEnv(t, noPipelineAnswers)
	if code, _ := e.wait(t, "--create-timeout", "-1m"); code != 2 {
		t.Errorf("exit %d, want 2 (stderr: %s)", code, e.stderr.String())
	}
}

// Someone pushes while the command waits for the pipeline of the previous
// head: GitLab gets the whole --create-timeout again for the new head.
func TestPipelineWaitCreateTimeoutRestartsOnNewHead(t *testing.T) {
	var (
		e      *waitEnv
		start  time.Time
		pushed time.Time
	)
	e = newWaitEnv(t, func(path string) (string, int) {
		if path != waitMR {
			return noPipelineAnswers(path)
		}
		now := e.clock.peek()
		if now.Sub(start) < 90*time.Second {
			return `{"iid":1,"project_id":5,"sha":"aaa111"}`, 200
		}
		if pushed.IsZero() {
			pushed = now
		}
		return `{"iid":1,"project_id":5,"sha":"bbb222"}`, 200
	})
	start = e.clock.peek()
	code, out := e.wait(t, "--create-timeout", "2m")
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	if string(out["no_pipeline"]) != "true" || string(out["sha"]) != `"bbb222"` {
		t.Errorf("want no_pipeline for the new head, got %s", e.stdout.String())
	}
	if pushed.IsZero() {
		t.Fatal("the command stopped before the push")
	}
	if waited := e.clock.peek().Sub(pushed); waited < 2*time.Minute {
		t.Errorf("gave up %s after the push, before --create-timeout", waited)
	}
	for _, sha := range []string{"aaa111", "bbb222"} {
		if n := strings.Count(e.stderr.String(), "no pipeline for "+sha+" yet"); n != 1 {
			t.Errorf("want one note on stderr for %s, got %d: %s", sha, n, e.stderr.String())
		}
	}
}

// GitLab creates the pipeline a while after the push, within
// --create-timeout: the command waits for it and prints it.
func TestPipelineWaitWaitsForASlowPipeline(t *testing.T) {
	asked := 0
	e := newWaitEnv(t, func(path string) (string, int) {
		switch path {
		case waitMR:
			if asked++; asked <= 3 {
				return `{"iid":1,"project_id":5,"sha":"aaa111"}`, 200
			}
			return `{"iid":1,"project_id":5,"sha":"aaa111","head_pipeline":{"id":7,"sha":"aaa111","ref":"refs/merge-requests/1/head","status":"success"}}`, 200
		case "projects/group/project/pipelines/7":
			return `{"id":7,"sha":"aaa111","ref":"refs/merge-requests/1/head","status":"success","web_url":"https://gitlab.com/group/project/-/pipelines/7"}`, 200
		case "projects/group/project/pipelines/7/jobs", "projects/group/project/pipelines/7/bridges":
			return `[]`, 200
		}
		return noPipelineAnswers(path)
	})
	code, out := e.wait(t, "--create-timeout", "10m")
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	var p struct {
		ID     int    `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out["pipeline"], &p); err != nil || p.ID != 7 || p.Status != "success" {
		t.Errorf("want the pipeline 7, got %s", e.stdout.String())
	}
	if _, ok := out["no_pipeline"]; ok || string(out["running"]) != "false" {
		t.Errorf("want a finished pipeline and no no_pipeline, got %s", e.stdout.String())
	}
}
