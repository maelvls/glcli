package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
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
	return e.run(t, append([]string{"pipeline", "wait", "group/project!1", "--interval", "5s"}, args...)...)
}

// run runs glcli with args and returns its exit code and the JSON it
// printed.
func (e *waitEnv) run(t *testing.T, args ...string) (code int, out map[string]json.RawMessage) {
	t.Helper()
	code = run(e.rt, args)
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

const waitPipeline = "projects/group/project/pipelines/7"

// onePipeline is a merge request whose head pipeline (7, no downstream
// pipeline) has one failed job, "test", with a log. status is asked each
// time the pipeline is read.
func onePipeline(status func() string) func(path string) (string, int) {
	return func(path string) (string, int) {
		switch path {
		case waitMR:
			return `{"iid":1,"project_id":5,"sha":"aaa111","head_pipeline":{"id":7,"sha":"aaa111","ref":"refs/merge-requests/1/head","status":"running"}}`, 200
		case waitPipeline:
			return `{"id":7,"sha":"aaa111","status":"` + status() + `","web_url":"https://gitlab.com/group/project/-/pipelines/7"}`, 200
		case waitPipeline + "/jobs":
			return `[{"id":70,"name":"test","stage":"test","status":"failed"}]`, 200
		case waitPipeline + "/bridges":
			return `[]`, 200
		case "projects/group/project/jobs/70/trace":
			return "$ make test\nFAIL: TestX\n", 200
		}
		return `{"message":"404 Not Found"}`, 404
	}
}

// checkFailedJobs checks that the result has the failed job with its log,
// and that the jobs were asked for once, failed ones only.
func checkFailedJobs(t *testing.T, e *waitEnv, out map[string]json.RawMessage) {
	t.Helper()
	var jobs []struct {
		Name string `json:"name"`
		Log  string `json:"log"`
	}
	if err := json.Unmarshal(out["failed_jobs"], &jobs); err != nil || len(jobs) != 1 || jobs[0].Name != "test" || !strings.Contains(jobs[0].Log, "FAIL: TestX") {
		t.Errorf("want the failed job with its log, got %s", e.stdout.String())
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	asked := e.queries[waitPipeline+"/jobs"]
	if len(asked) != 1 {
		t.Fatalf("the jobs were asked for %d times, want once, at the end", len(asked))
	}
	if scope := asked[0]["scope[]"]; len(scope) != 1 || scope[0] != "failed" {
		t.Errorf("want scope[]=failed, got %s", asked[0].Encode())
	}
}

// While the pipeline runs, the polls read no jobs; when it has finished,
// the failed jobs and their logs are read once.
func TestPipelineWaitReadsJobsOnceAtTheEnd(t *testing.T) {
	read := 0
	e := newWaitEnv(t, onePipeline(func() string {
		if read++; read <= 3 {
			return "running"
		}
		return "failed"
	}))
	code, out := e.wait(t)
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	var p struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out["pipeline"], &p); err != nil || p.Status != "failed" || string(out["running"]) != "false" {
		t.Errorf("want a failed pipeline, got %s", e.stdout.String())
	}
	if _, ok := out["timed_out"]; ok {
		t.Errorf("want no timed_out, got %s", e.stdout.String())
	}
	if n := e.count(waitPipeline); n < 4 {
		t.Errorf("the pipeline was read %d times, want at least 4 polls", n)
	}
	checkFailedJobs(t, e, out)
}

// On timeout, the current state is complete: the jobs that have failed so
// far, with their logs.
func TestPipelineWaitTimeoutPrintsFailedJobs(t *testing.T) {
	e := newWaitEnv(t, onePipeline(func() string { return "running" }))
	code, out := e.wait(t, "--timeout", "2m")
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	if string(out["timed_out"]) != "true" || string(out["running"]) != "true" || len(out["hints"]) == 0 {
		t.Errorf("want timed_out, running and a hint, got %s", e.stdout.String())
	}
	checkFailedJobs(t, e, out)
}

// The pipeline finishes between the last poll and the read made on
// timeout: it is printed as finished, not as timed out.
func TestPipelineWaitFinishedAtTheTimeout(t *testing.T) {
	// How many times a wait that times out reads the pipeline: the last
	// read is the one made on timeout.
	e := newWaitEnv(t, onePipeline(func() string { return "running" }))
	if code, out := e.wait(t, "--timeout", "2m"); code != 0 || string(out["timed_out"]) != "true" {
		t.Fatalf("exit %d, %s (stderr: %s)", code, e.stdout.String(), e.stderr.String())
	}
	last := e.count(waitPipeline)

	read := 0
	e = newWaitEnv(t, onePipeline(func() string {
		if read++; read < last {
			return "running"
		}
		return "failed"
	}))
	code, out := e.wait(t, "--timeout", "2m")
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	if _, ok := out["timed_out"]; ok || string(out["running"]) != "false" || len(out["hints"]) != 0 {
		t.Errorf("want a finished pipeline, no timed_out and no hint, got %s", e.stdout.String())
	}
	checkFailedJobs(t, e, out)
}

// A log that cannot be read is said in "warnings", by "pipeline get" and
// by "pipeline wait".
func TestPipelineWarnsAboutALogItCannotRead(t *testing.T) {
	for _, args := range [][]string{
		{"pipeline", "get", "group/project!1"},
		{"pipeline", "wait", "group/project!1", "--interval", "5s"},
	} {
		t.Run(args[1], func(t *testing.T) {
			answers := onePipeline(func() string { return "failed" })
			e := newWaitEnv(t, func(path string) (string, int) {
				if strings.HasSuffix(path, "/trace") {
					return `{"message":"403 Forbidden"}`, 403
				}
				return answers(path)
			})
			code, out := e.run(t, args...)
			if code != 0 {
				t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
			}
			var warnings []string
			if err := json.Unmarshal(out["warnings"], &warnings); err != nil || len(warnings) != 1 ||
				!strings.Contains(warnings[0], `"test" (70, in group/project)`) || !strings.Contains(warnings[0], "HTTP 403") {
				t.Errorf("want one warning with the job and the reason, got %s", e.stdout.String())
			}
			var jobs []map[string]json.RawMessage
			if err := json.Unmarshal(out["failed_jobs"], &jobs); err != nil || len(jobs) != 1 || jobs[0]["log"] != nil {
				t.Errorf("want the failed job, without log, got %s", e.stdout.String())
			}
		})
	}
}

// outage is what GitLab answers when it is failing: Poll tries again.
const outage = `{"message":"500 Internal Server Error"}`

// with answers like answers, except for the paths of except.
func with(answers func(path string) (string, int), except map[string]func() (string, int)) func(path string) (string, int) {
	return func(path string) (string, int) {
		if f, ok := except[path]; ok {
			return f()
		}
		return answers(path)
	}
}

// printed reads the pipeline, the warnings and the hints of a result.
func printed(t *testing.T, e *waitEnv, out map[string]json.RawMessage) (id int, status string, warnings, hints []string) {
	t.Helper()
	var p struct {
		ID     int    `json:"id"`
		Status string `json:"status"`
	}
	for key, into := range map[string]any{"pipeline": &p, "warnings": &warnings, "hints": &hints} {
		if raw, ok := out[key]; ok {
			if err := json.Unmarshal(raw, into); err != nil {
				t.Fatalf("%s: %v: %s", key, err, e.stdout.String())
			}
		}
	}
	return p.ID, p.Status, warnings, hints
}

// The failed jobs of the head's pipeline cannot be read, someone pushes,
// and the new head's pipeline cannot be read either: on timeout, the
// pipeline printed is the new one, not the finished pipeline of the old
// head, and the warning is about the pipeline printed.
func TestPipelineWaitTimeoutDoesNotPrintThePreviousPipeline(t *testing.T) {
	var (
		e     *waitEnv
		start time.Time
	)
	old := onePipeline(func() string { return "failed" })
	e = newWaitEnv(t, with(old, map[string]func() (string, int){
		waitMR: func() (string, int) {
			if e.clock.peek().Sub(start) < time.Minute {
				return old(waitMR)
			}
			return `{"iid":1,"project_id":5,"sha":"bbb222","head_pipeline":{"id":9,"sha":"bbb222","ref":"refs/merge-requests/1/head","status":"running"}}`, 200
		},
		waitPipeline + "/jobs":               func() (string, int) { return outage, 500 },
		"projects/group/project/pipelines/9": func() (string, int) { return outage, 500 },
	}))
	start = e.clock.peek()
	code, out := e.wait(t, "--timeout", "3m")
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	line := e.stdout.String()
	id, status, warnings, hints := printed(t, e, out)
	if id != 9 || status != "" {
		t.Errorf("want the pipeline 9, of which nothing is known, got %s", line)
	}
	if string(out["timed_out"]) != "true" || string(out["running"]) != "true" || len(hints) != 1 {
		t.Errorf("want timed_out, running and a hint, got %s", line)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "pipeline 9 could not be read") || !strings.Contains(warnings[0], "HTTP 500") {
		t.Errorf("want one warning about the pipeline 9, got %s", line)
	}
	if e.count(waitPipeline) == 0 || e.count("projects/group/project/pipelines/9") == 0 {
		t.Errorf("the test must have read both pipelines: %v", e.seen)
	}
}

// The pipeline has failed but its jobs cannot be read before --timeout:
// the result is not complete, and must not look so.
func TestPipelineWaitTimeoutWhenTheFailedJobsCannotBeRead(t *testing.T) {
	for _, pipelineStatus := range []string{"failed", "running"} {
		t.Run(pipelineStatus, func(t *testing.T) {
			e := newWaitEnv(t, with(onePipeline(func() string { return pipelineStatus }), map[string]func() (string, int){
				waitPipeline + "/jobs": func() (string, int) { return outage, 500 },
			}))
			code, out := e.wait(t, "--timeout", "2m")
			if code != 0 {
				t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
			}
			line := e.stdout.String()
			id, status, warnings, hints := printed(t, e, out)
			if id != 7 || status != pipelineStatus || string(out["failed_jobs"]) != "[]" {
				t.Errorf("want the pipeline 7, %s, without jobs, got %s", pipelineStatus, line)
			}
			if string(out["timed_out"]) != "true" || len(hints) != 1 {
				t.Errorf("want timed_out and a hint, got %s", line)
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], "the failed jobs of pipeline 7 could not be read") {
				t.Errorf("want one warning about the jobs of the pipeline 7, got %s", line)
			}
		})
	}
}

// Someone pushes while the pipeline of the old head runs, and the new
// head has no pipeline when --timeout expires: the old pipeline is not
// the answer.
func TestPipelineWaitTimeoutAfterAPushWithoutPipeline(t *testing.T) {
	var (
		e     *waitEnv
		start time.Time
	)
	old := onePipeline(func() string { return "running" })
	e = newWaitEnv(t, func(path string) (string, int) {
		if e.clock.peek().Sub(start) < time.Minute {
			return old(path)
		}
		switch path {
		case waitMR:
			return `{"iid":1,"project_id":5,"sha":"bbb222"}`, 200
		case waitMR + "/pipelines":
			return `[{"id":7,"sha":"aaa111","ref":"refs/merge-requests/1/head","status":"running"}]`, 200
		}
		return old(path)
	})
	start = e.clock.peek()
	code, out := e.wait(t, "--timeout", "3m", "--create-timeout", "10m")
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	if e.count(waitPipeline) == 0 {
		t.Fatal("the test must have read the pipeline of the old head")
	}
	id, _, _, _ := printed(t, e, out)
	if id == 7 || string(out["sha"]) != `"bbb222"` || string(out["timed_out"]) != "true" || string(out["running"]) != "true" {
		t.Errorf("want no pipeline yet for bbb222 and timed_out, got %s", e.stdout.String())
	}
}

// The pipeline has finished, and GitLab fails while its failed jobs are
// read: that read is tried again, and the result is complete.
func TestPipelineWaitRetriesTheFinalRead(t *testing.T) {
	asked := 0
	answers := onePipeline(func() string { return "failed" })
	e := newWaitEnv(t, with(answers, map[string]func() (string, int){
		waitPipeline + "/jobs": func() (string, int) {
			if asked++; asked == 1 {
				return outage, 500
			}
			return answers(waitPipeline + "/jobs")
		},
	}))
	code, out := e.wait(t)
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	var jobs []struct {
		Name string `json:"name"`
		Log  string `json:"log"`
	}
	if err := json.Unmarshal(out["failed_jobs"], &jobs); err != nil || len(jobs) != 1 || jobs[0].Name != "test" || jobs[0].Log == "" {
		t.Errorf("want the failed job with its log, got %s", e.stdout.String())
	}
	_, timedOut := out["timed_out"]
	if _, warned := out["warnings"]; timedOut || warned || string(out["running"]) != "false" {
		t.Errorf("want a finished pipeline, no timed_out and no warning, got %s", e.stdout.String())
	}
	if asked != 2 {
		t.Errorf("the jobs were asked for %d times, want 2: the failure, then the retry", asked)
	}
}

// add moves the clock forward: the machine slept.
func (c *stepClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// GitLab fails for longer than --create-timeout while the pipeline is
// awaited, and creates it late once it answers again: the outage is not
// counted, and the pipeline is waited for.
func TestPipelineWaitOutageDoesNotCountInCreateTimeout(t *testing.T) {
	var (
		e     *waitEnv
		start time.Time
	)
	created := onePipeline(func() string { return "success" })
	e = newWaitEnv(t, func(path string) (string, int) {
		switch since := e.clock.peek().Sub(start); {
		case since < 30*time.Second:
			return noPipelineAnswers(path)
		case since < 6*time.Minute:
			return outage, 500
		case since < 6*time.Minute+40*time.Second:
			return noPipelineAnswers(path)
		}
		return created(path)
	})
	start = e.clock.peek()
	code, out := e.wait(t, "--create-timeout", "3m")
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	id, status, _, _ := printed(t, e, out)
	if _, ok := out["no_pipeline"]; ok || id != 7 || status != "success" {
		t.Errorf("want the pipeline 7, created after the outage, got %s", e.stdout.String())
	}
}

// The rest of --create-timeout still runs after an outage.
func TestPipelineWaitCreateTimeoutEndsAfterAnOutage(t *testing.T) {
	var (
		e     *waitEnv
		start time.Time
	)
	e = newWaitEnv(t, func(path string) (string, int) {
		if since := e.clock.peek().Sub(start); since >= 30*time.Second && since < 6*time.Minute {
			return outage, 500
		}
		return noPipelineAnswers(path)
	})
	start = e.clock.peek()
	code, out := e.wait(t, "--create-timeout", "3m")
	if code != 0 || string(out["no_pipeline"]) != "true" {
		t.Fatalf("exit %d, want no_pipeline, got %s (stderr: %s)", code, e.stdout.String(), e.stderr.String())
	}
	// 5m30s of outage and 3m of answers, less the poll during which the
	// outage started.
	if waited := e.clock.peek().Sub(start); waited < 7*time.Minute+30*time.Second {
		t.Errorf("gave up after %s: the outage was counted in --create-timeout", waited)
	}
}

// The machine sleeps for two hours: GitLab had all that time to create
// the pipeline, so its first answer after the sleep settles it. When the
// first request after the sleep fails, that cannot be told from an outage
// of GitLab: the two hours are not counted, and the period goes on.
func TestPipelineWaitSleepCountsInCreateTimeout(t *testing.T) {
	for _, tt := range []struct {
		name        string
		networkLate bool
		settled     bool
	}{
		{"network back at once", false, true},
		{"network back at the second try", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var e *waitEnv
			asked := 0
			e = newWaitEnv(t, func(path string) (string, int) {
				if path == waitMR {
					asked++
					if asked == 2 {
						e.clock.add(2 * time.Hour)
					}
					if asked == 2 && tt.networkLate {
						return outage, 500
					}
				}
				return noPipelineAnswers(path)
			})
			code, out := e.wait(t)
			if code != 0 || string(out["no_pipeline"]) != "true" {
				t.Fatalf("exit %d, want no_pipeline, got %s (stderr: %s)", code, e.stdout.String(), e.stderr.String())
			}
			// Settled: by the answer after the sleep, the second.
			if n := e.count(waitMR); (n == 2) != tt.settled {
				t.Errorf("the merge request was read %d times; settled by the answer after the sleep: want %v", n, tt.settled)
			}
		})
	}
}

// The pipeline is created late, runs, then is deleted: GitLab gets the
// whole --create-timeout again, not what was left of the first wait.
func TestPipelineWaitCreateTimeoutRestartsWhenThePipelineIsGone(t *testing.T) {
	var (
		e     *waitEnv
		start time.Time
		gone  time.Time
	)
	running := onePipeline(func() string { return "running" })
	e = newWaitEnv(t, func(path string) (string, int) {
		now := e.clock.peek()
		switch since := now.Sub(start); {
		case since < time.Minute:
			return noPipelineAnswers(path)
		case since < 4*time.Minute:
			return running(path)
		}
		if gone.IsZero() {
			gone = now
		}
		return noPipelineAnswers(path)
	})
	start = e.clock.peek()
	code, out := e.wait(t, "--create-timeout", "2m")
	if code != 0 || string(out["no_pipeline"]) != "true" {
		t.Fatalf("exit %d, want no_pipeline, got %s (stderr: %s)", code, e.stdout.String(), e.stderr.String())
	}
	if e.count(waitPipeline) == 0 || gone.IsZero() {
		t.Fatal("the test must have read the pipeline, then lost it")
	}
	if waited := e.clock.peek().Sub(gone); waited < 2*time.Minute {
		t.Errorf("gave up %s after the pipeline was gone, before --create-timeout", waited)
	}
}

// mergeCommits numbers the merge commits of the tests: their parents are
// cached for as long as the process lives.
var mergeCommits atomic.Int64

// mergedResultsPipeline is a merge request whose head pipeline (7) runs
// on a merge commit, as in a project with merged-results pipelines.
// status is asked each time the pipeline is read, readable each time the
// merge commit is.
func mergedResultsPipeline(status func() string, readable func() bool) func(path string) (string, int) {
	merge := fmt.Sprintf("merge%06d", mergeCommits.Add(1))
	head := `{"id":7,"sha":"` + merge + `","ref":"refs/merge-requests/1/merge","status":"running"}`
	return with(onePipeline(status), map[string]func() (string, int){
		waitMR: func() (string, int) {
			return `{"iid":1,"project_id":5,"sha":"aaa111","head_pipeline":` + head + `}`, 200
		},
		waitMR + "/pipelines": func() (string, int) { return `[` + head + `]`, 200 },
		"projects/5/repository/commits/" + merge: func() (string, int) {
			if !readable() {
				return `{"message":"404 Commit Not Found"}`, 404
			}
			return `{"parent_ids":["ccc333","aaa111"]}`, 200
		},
	})
}

// The head pipeline runs on a merge commit that cannot be read. It is not
// "no pipeline": after --create-timeout it is waited on, and printed with
// a warning.
func TestPipelineWaitHeadPipelineOnAnUnreadableMergeCommit(t *testing.T) {
	var (
		e         *waitEnv
		start     time.Time
		firstRead time.Time
	)
	e = newWaitEnv(t, mergedResultsPipeline(func() string {
		now := e.clock.peek()
		if firstRead.IsZero() {
			firstRead = now
		}
		if now.Sub(start) < 4*time.Minute {
			return "running"
		}
		return "failed"
	}, func() bool { return false }))
	start = e.clock.peek()
	code, out := e.wait(t, "--create-timeout", "2m")
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	line := e.stdout.String()
	id, status, warnings, _ := printed(t, e, out)
	if _, ok := out["no_pipeline"]; ok || id != 7 || status != "failed" {
		t.Errorf("want the pipeline 7, failed, and no no_pipeline, got %s", line)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "could not confirm that pipeline 7 ran on aaa111") {
		t.Errorf("want a warning that the pipeline could not be confirmed, got %s", line)
	}
	checkFailedJobs(t, e, out)
	// Until --create-timeout, the commit may become readable.
	if waited := firstRead.Sub(start); waited < 2*time.Minute {
		t.Errorf("the unconfirmed pipeline was waited on after %s, before --create-timeout", waited)
	}
	if n := strings.Count(e.stderr.String(), "cannot confirm that pipeline 7 is the pipeline of aaa111"); n != 1 {
		t.Errorf("want one note on stderr, got %d: %s", n, e.stderr.String())
	}
}

// The merge commit becomes readable before --create-timeout: the pipeline
// is confirmed, and printed without a warning.
func TestPipelineWaitMergeCommitBecomesReadable(t *testing.T) {
	var (
		e     *waitEnv
		start time.Time
	)
	e = newWaitEnv(t, mergedResultsPipeline(
		func() string { return "success" },
		func() bool { return e.clock.peek().Sub(start) >= time.Minute },
	))
	start = e.clock.peek()
	code, out := e.wait(t, "--create-timeout", "10m")
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	id, status, warnings, _ := printed(t, e, out)
	if id != 7 || status != "success" || len(warnings) != 0 {
		t.Errorf("want the pipeline 7, success, without a warning, got %s", e.stdout.String())
	}
	if waited := e.clock.peek().Sub(start); waited >= 10*time.Minute {
		t.Errorf("waited %s: until --create-timeout, although the commit was readable after 1m", waited)
	}
}

// The merge commit becomes readable after --create-timeout, while the
// unconfirmed pipeline is already waited on: it is confirmed then, and
// the result has no warning.
func TestPipelineWaitMergeCommitBecomesReadableWhileWaiting(t *testing.T) {
	var (
		e     *waitEnv
		start time.Time
	)
	e = newWaitEnv(t, mergedResultsPipeline(
		func() string {
			if e.clock.peek().Sub(start) < 5*time.Minute {
				return "running"
			}
			return "success"
		},
		func() bool { return e.clock.peek().Sub(start) >= 3*time.Minute },
	))
	start = e.clock.peek()
	code, out := e.wait(t, "--create-timeout", "1m")
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	if !strings.Contains(e.stderr.String(), "cannot confirm that pipeline 7 is the pipeline of aaa111") {
		t.Fatalf("the test must have waited on the unconfirmed pipeline first (stderr: %s)", e.stderr.String())
	}
	id, status, warnings, _ := printed(t, e, out)
	if id != 7 || status != "success" || len(warnings) != 0 {
		t.Errorf("want the pipeline 7, success, without a warning, got %s", e.stdout.String())
	}
}

// --timeout or "pipeline get" before the unconfirmed pipeline is waited
// on: there is no pipeline to print, and the warning says which pipeline
// could not be confirmed.
func TestPipelineUnconfirmedHeadPipelineIsWarnedAbout(t *testing.T) {
	for _, args := range [][]string{
		{"pipeline", "get", "group/project!1"},
		{"pipeline", "wait", "group/project!1", "--interval", "5s", "--timeout", "1m"},
	} {
		t.Run(args[1], func(t *testing.T) {
			e := newWaitEnv(t, mergedResultsPipeline(func() string { return "running" }, func() bool { return false }))
			code, out := e.run(t, args...)
			if code != 0 {
				t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
			}
			id, _, warnings, hints := printed(t, e, out)
			if id != 0 || string(out["running"]) != "true" || string(out["sha"]) != `"aaa111"` || len(hints) != 1 {
				t.Errorf("want no pipeline yet for aaa111, got %s", e.stdout.String())
			}
			if _, ok := out["no_pipeline"]; ok {
				t.Errorf("want no no_pipeline, got %s", e.stdout.String())
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], "could not confirm that pipeline 7 ran on aaa111") {
				t.Errorf("want a warning that the pipeline could not be confirmed, got %s", e.stdout.String())
			}
		})
	}
}

// --sha is the SHA of a commit: anything else is a usage error, before
// GitLab is asked anything.
func TestPipelineRejectsWhatIsNotACommitSHA(t *testing.T) {
	for _, sha := range []string{"f", "abc123", "HEAD", "main", "zzzzzzz", strings.Repeat("a", 41)} {
		for _, args := range [][]string{
			{"pipeline", "get", "group/project!1", "--sha", sha},
			{"pipeline", "wait", "group/project!1", "--interval", "5s", "--sha", sha},
		} {
			e := newWaitEnv(t, noPipelineAnswers)
			if code, _ := e.run(t, args...); code != 2 || !strings.Contains(e.stderr.String(), "--sha") {
				t.Errorf("%v: exit %d, want 2 and a word about --sha (stderr: %s)", args, code, e.stderr.String())
			}
			if n := e.count(waitMR); n != 0 {
				t.Errorf("%v: GitLab was asked %d times", args, n)
			}
		}
	}
}

// longSHA is a merge request whose commits have SHAs long enough for
// --sha: its head pipeline (7) ran on abcdef1234.
func longSHA(path string) (string, int) {
	switch path {
	case waitMR:
		return `{"iid":1,"project_id":5,"sha":"abcdef1234","head_pipeline":{"id":7,"sha":"abcdef1234","ref":"refs/merge-requests/1/head","status":"success"}}`, 200
	case waitMR + "/pipelines":
		return `[{"id":7,"sha":"abcdef1234","ref":"refs/merge-requests/1/head","status":"success"}]`, 200
	case waitPipeline:
		return `{"id":7,"sha":"abcdef1234","status":"success","web_url":"https://gitlab.com/group/project/-/pipelines/7"}`, 200
	case waitPipeline + "/jobs", waitPipeline + "/bridges":
		return `[]`, 200
	}
	return `{"message":"404 Not Found"}`, 404
}

func TestPipelineShaInUpperCase(t *testing.T) {
	e := newWaitEnv(t, longSHA)
	code, out := e.run(t, "pipeline", "get", "group/project!1", "--sha", "ABCDEF1")
	if id, status, _, _ := printed(t, e, out); code != 0 || id != 7 || status != "success" {
		t.Errorf("exit %d, want the pipeline 7, got %s (stderr: %s)", code, e.stdout.String(), e.stderr.String())
	}
}

// A --sha that is not the head and matches no pipeline: the reason is not
// "this project has no CI".
func TestPipelineShaThatMatchesNoPipeline(t *testing.T) {
	for _, args := range [][]string{
		{"pipeline", "get", "group/project!1", "--sha", "1234567"},
		{"pipeline", "wait", "group/project!1", "--interval", "5s", "--sha", "1234567", "--create-timeout", "1m"},
	} {
		t.Run(args[1], func(t *testing.T) {
			e := newWaitEnv(t, longSHA)
			code, out := e.run(t, args...)
			if code != 0 {
				t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
			}
			_, _, _, hints := printed(t, e, out)
			if len(hints) != 1 || !strings.Contains(hints[0], "None of the 20 most recent pipelines of this merge request ran on 1234567") ||
				!strings.Contains(hints[0], "abcdef1234") || strings.Contains(hints[0], "skip ci") {
				t.Errorf("want a hint about the commit, with the head, and not about CI, got %s", e.stdout.String())
			}
			if string(out["sha"]) != `"1234567"` || string(out["failed_jobs"]) != "[]" {
				t.Errorf("want the sha and an empty failed_jobs, got %s", e.stdout.String())
			}
			if args[1] == "wait" && string(out["no_pipeline"]) != "true" {
				t.Errorf("want no_pipeline, got %s", e.stdout.String())
			}
		})
	}
}

// The note on stderr when another pipeline is waited on says "new head"
// only when the head changed: a pipeline run again is on the same commit.
func TestPipelineWaitNoteForANewPipeline(t *testing.T) {
	for _, tt := range []struct {
		name, newSHA, want string
	}{
		{"run again on the same commit", "aaa111", "glcli: now waiting for pipeline 8\n"},
		{"new head", "bbb222", "glcli: now waiting for pipeline 8 (new head bbb222)\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var (
				e     *waitEnv
				start time.Time
			)
			old := onePipeline(func() string { return "running" })
			e = newWaitEnv(t, func(path string) (string, int) {
				if e.clock.peek().Sub(start) < time.Minute {
					return old(path)
				}
				switch path {
				case waitMR:
					return `{"iid":1,"project_id":5,"sha":"` + tt.newSHA + `","head_pipeline":{"id":8,"sha":"` + tt.newSHA + `","ref":"refs/merge-requests/1/head","status":"success"}}`, 200
				case "projects/group/project/pipelines/8":
					return `{"id":8,"sha":"` + tt.newSHA + `","status":"success","web_url":"https://gitlab.com/group/project/-/pipelines/8"}`, 200
				case "projects/group/project/pipelines/8/jobs", "projects/group/project/pipelines/8/bridges":
					return `[]`, 200
				}
				return old(path)
			})
			start = e.clock.peek()
			code, out := e.wait(t)
			if id, _, _, _ := printed(t, e, out); code != 0 || id != 8 {
				t.Fatalf("exit %d, want the pipeline 8, got %s (stderr: %s)", code, e.stdout.String(), e.stderr.String())
			}
			if e.stderr.String() != tt.want {
				t.Errorf("stderr: %q, want %q", e.stderr.String(), tt.want)
			}
		})
	}
}

// An outage of GitLab starts just before --create-timeout would end: the
// time between the last answer and the first failed request is part of
// the outage, not time GitLab had to create the pipeline.
func TestPipelineWaitOutageAtTheEndOfCreateTimeout(t *testing.T) {
	// The fake fails from 1m to 6m, then answers "no pipeline" once, then
	// has the pipeline. It tells when it last answered before failing, and
	// when it first failed.
	type timeline struct{ first, last, failed, after time.Time }
	scenario := func(tl *timeline) *waitEnv {
		var (
			e     *waitEnv
			start time.Time
		)
		created := onePipeline(func() string { return "success" })
		e = newWaitEnv(t, func(path string) (string, int) {
			now := e.clock.peek()
			switch since := now.Sub(start); {
			case since < time.Minute:
				if tl.first.IsZero() {
					tl.first = now
				}
				tl.last = now
				return noPipelineAnswers(path)
			case since < 6*time.Minute:
				if tl.failed.IsZero() {
					tl.failed = now
				}
				return outage, 500
			}
			if tl.after.IsZero() || tl.after.Equal(now) {
				tl.after = now
				return noPipelineAnswers(path)
			}
			return created(path)
		})
		start = e.clock.peek()
		return e
	}
	// A first run to learn the timeline, which does not depend on
	// --create-timeout as long as that does not end the wait.
	var tl timeline
	e := scenario(&tl)
	if code, out := e.wait(t, "--create-timeout", "1h"); code != 0 || out["no_pipeline"] != nil {
		t.Fatalf("exit %d, %s (stderr: %s)", code, e.stdout.String(), e.stderr.String())
	}
	answered, untilFailure := tl.last.Sub(tl.first), tl.failed.Sub(tl.first)
	if answered <= 0 || untilFailure <= answered {
		t.Fatalf("timeline: answers over %s, first failure after %s", answered, untilFailure)
	}
	// A period longer than the time covered by answers, and shorter than
	// the time until the first failed request.
	period := answered + (untilFailure-answered)/2
	var again timeline
	e = scenario(&again)
	code, out := e.wait(t, "--create-timeout", period.String())
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	id, status, _, _ := printed(t, e, out)
	if _, ok := out["no_pipeline"]; ok || id != 7 || status != "success" {
		t.Errorf("--create-timeout %s, answers over %s, first failure after %s: want the pipeline 7, created after the outage, got %s", period, answered, untilFailure, e.stdout.String())
	}
}

// A --sha that matches nothing, while the merge commit of a listed
// pipeline cannot be read: the hint does not claim that no pipeline ran
// on the commit.
func TestPipelineShaWithPipelinesThatCannotBeChecked(t *testing.T) {
	for _, args := range [][]string{
		{"pipeline", "get", "group/project!1", "--sha", "1234567"},
		{"pipeline", "wait", "group/project!1", "--interval", "5s", "--sha", "1234567", "--create-timeout", "1m"},
	} {
		t.Run(args[1], func(t *testing.T) {
			e := newWaitEnv(t, with(longSHA, map[string]func() (string, int){
				waitMR + "/pipelines": func() (string, int) {
					return `[{"id":7,"sha":"abcdef1234","ref":"refs/merge-requests/1/head","status":"success"},
						{"id":6,"sha":"gone000006","ref":"refs/merge-requests/1/merge","status":"success"},
						{"id":5,"sha":"gone000005","ref":"refs/merge-requests/1/train","status":"success"},
						{"id":4,"sha":"fedcba4321","ref":"refs/merge-requests/1/head","status":"success"}]`, 200
				},
			}))
			code, out := e.run(t, args...)
			if code != 0 {
				t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
			}
			_, _, _, hints := printed(t, e, out)
			if len(hints) != 1 || !strings.Contains(hints[0], "2 could not be checked") || strings.Contains(hints[0], "None of the 20") || strings.Contains(hints[0], "skip ci") {
				t.Errorf("want a hint that 2 pipelines could not be checked, got %s", e.stdout.String())
			}
		})
	}
}

// GitLab never answers before --timeout: there is no commit to name, and
// nothing is known of its pipeline.
func TestPipelineWaitTimeoutWhenGitLabNeverAnswers(t *testing.T) {
	e := newWaitEnv(t, func(string) (string, int) { return outage, 500 })
	code, out := e.wait(t, "--timeout", "1m")
	if code != 0 {
		t.Fatalf("exit %d (stderr: %s)", code, e.stderr.String())
	}
	_, _, _, hints := printed(t, e, out)
	if len(hints) != 1 || !strings.Contains(hints[0], "GitLab could not be reached within 1m0s") || strings.Contains(hints[0], "No pipeline was created") {
		t.Errorf("want a hint that GitLab could not be reached, got %s", e.stdout.String())
	}
	if _, ok := out["sha"]; ok || string(out["timed_out"]) != "true" || string(out["running"]) != "true" {
		t.Errorf("want timed_out, running and no sha, got %s", e.stdout.String())
	}
}
