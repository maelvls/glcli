package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/maelvls/glcli/internal/config"
	"github.com/maelvls/glcli/internal/gitlab"
	"github.com/maelvls/glcli/internal/ref"
)

// fake is a GitLab that answers GET /api/v4/<path> with routes[path] and
// remembers what was asked. A path without a route is a 404.
type fake struct {
	routes map[string]func(q url.Values) (status int, body string)

	mu      sync.Mutex
	queries map[string][]url.Values
}

// ok is a route that always answers body.
func ok(body string) func(url.Values) (int, string) {
	return func(url.Values) (int, string) { return 200, body }
}

// hosts numbers the fake hosts.
var hosts atomic.Int64

// client starts the fake and returns a client for it. Each fake has its
// own host name: the parents of merge commits are cached per host, for as
// long as the process lives.
func (f *fake) client(t *testing.T) *gitlab.Client {
	t.Helper()
	f.queries = map[string][]url.Values{}
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v4/")
		f.mu.Lock()
		f.queries[path] = append(f.queries[path], r.URL.Query())
		f.mu.Unlock()
		route, found := f.routes[path]
		if !found {
			http.Error(w, `{"message":"404 Not Found"}`, http.StatusNotFound)
			return
		}
		status, body := route(r.URL.Query())
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(gl.Close)
	c := gitlab.New(config.Host{Name: fmt.Sprintf("fake-%d", hosts.Add(1)), Token: "t"})
	c.BaseURL = gl.URL
	return c
}

// asked returns the query of each request made for path.
func (f *fake) asked(path string) []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queries[path]
}

// total is the number of requests made.
func (f *fake) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, q := range f.queries {
		n += len(q)
	}
	return n
}

const (
	oldSHA    = "aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000"
	headSHA   = "bbbb1111bbbb1111bbbb1111bbbb1111bbbb1111"
	targetSHA = "cccc2222cccc2222cccc2222cccc2222cccc2222"
)

// mergedResults is a merge request of a project with merged-results
// pipelines and a merge train: the SHA of each pipeline is a temporary
// merge commit, whose second parent is the commit of the merge request.
// Pipelines 90 and 93 ran on oldSHA, 95 (the head pipeline) on headSHA.
func mergedResults() *fake {
	const mrPath = "projects/group/project/merge_requests/1"
	commit := func(parents ...string) func(url.Values) (int, string) {
		return ok(`{"parent_ids":["` + strings.Join(parents, `","`) + `"]}`)
	}
	return &fake{routes: map[string]func(url.Values) (int, string){
		mrPath: ok(`{"iid":1,"project_id":5,"sha":"` + headSHA + `",
			"head_pipeline":{"id":95,"sha":"m95","ref":"refs/merge-requests/1/merge","status":"running"}}`),
		mrPath + "/pipelines": ok(`[
			{"id":95,"sha":"m95","ref":"refs/merge-requests/1/merge","status":"running"},
			{"id":93,"sha":"t93","ref":"refs/merge-requests/1/train","status":"canceled"},
			{"id":90,"sha":"m90","ref":"refs/merge-requests/1/merge","status":"failed"},
			{"id":80,"sha":"m80","ref":"refs/merge-requests/1/merge","status":"success"}]`),
		"projects/5/repository/commits/m95": commit(targetSHA, headSHA),
		"projects/5/repository/commits/t93": commit(targetSHA, oldSHA),
		"projects/5/repository/commits/m90": commit(targetSHA, oldSHA),
		// m80 is gone: GitLab does not keep these commits forever.
	}}
}

var testMR = ref.MR{Host: "gitlab.com", Project: "group/project", IID: 1}

func TestForMRFindsMergedResultsPipelineOfAnOlderCommit(t *testing.T) {
	for _, sha := range []string{oldSHA, oldSHA[:8]} {
		t.Run(sha, func(t *testing.T) {
			f := mergedResults()
			c := f.client(t)
			for range 2 {
				id, got, err := ForMR(context.Background(), c, testMR, sha)
				if err != nil {
					t.Fatal(err)
				}
				// 93 and 90 ran on it: the newest wins.
				if id != 93 || got != sha {
					t.Errorf("ForMR(%s) = %d, %s; want pipeline 93", sha, id, got)
				}
			}
			// One request per merge commit looked at, whatever the number
			// of polls; none for the pipelines older than the one found.
			for path, want := range map[string]int{
				"projects/5/repository/commits/m95": 1,
				"projects/5/repository/commits/t93": 1,
				"projects/5/repository/commits/m90": 0,
				"projects/5/repository/commits/m80": 0,
			} {
				if n := len(f.asked(path)); n != want {
					t.Errorf("%s: %d requests, want %d", path, n, want)
				}
			}
		})
	}
}

func TestForMRHeadOfMergedResults(t *testing.T) {
	// The head commit: named, abbreviated, or by default.
	for _, sha := range []string{"", headSHA, headSHA[:8]} {
		t.Run(sha, func(t *testing.T) {
			f := mergedResults()
			id, got, err := ForMR(context.Background(), f.client(t), testMR, sha)
			if err != nil {
				t.Fatal(err)
			}
			if id != 95 || got != headSHA {
				t.Errorf("ForMR(%q) = %d, %s; want the head pipeline 95 and the full head SHA", sha, id, got)
			}
		})
	}
}

func TestForMRWithoutPipelineForTheCommit(t *testing.T) {
	f := mergedResults()
	// No pipeline ran on this commit, and the commit of pipeline 80
	// cannot be read (404): that is not an error.
	id, _, err := ForMR(context.Background(), f.client(t), testMR, "dddd3333")
	if err != nil || id != 0 {
		t.Errorf("ForMR = %d, %v; want 0 and no error", id, err)
	}
}

// Without merged results, the pipeline runs on the commit itself.
func TestForMRPipelineOnTheCommitItself(t *testing.T) {
	const mrPath = "projects/group/project/merge_requests/1"
	f := &fake{routes: map[string]func(url.Values) (int, string){
		mrPath: ok(`{"iid":1,"project_id":5,"sha":"` + headSHA + `",
			"head_pipeline":{"id":95,"sha":"` + headSHA + `","ref":"refs/merge-requests/1/head","status":"running"}}`),
		mrPath + "/pipelines": ok(`[
			{"id":95,"sha":"` + headSHA + `","ref":"refs/merge-requests/1/head","status":"running"},
			{"id":90,"sha":"` + oldSHA + `","ref":"refs/merge-requests/1/head","status":"failed"},
			{"id":85,"sha":"` + oldSHA + `","ref":"refs/merge-requests/1/head","status":"failed"}]`),
	}}
	id, _, err := ForMR(context.Background(), f.client(t), testMR, oldSHA[:8])
	if err != nil || id != 90 {
		t.Errorf("ForMR = %d, %v; want pipeline 90", id, err)
	}
}

// fakeJob is a job of the fake GitLab; retried is true for a run that
// was replaced by a newer run of the same job.
type fakeJob struct {
	id           int
	name, status string
	allowFailure bool
	retried      bool
}

// jobsRoute lists jobs as GitLab does: the latest run of each job unless
// include_retried, and the statuses of scope[] when given.
func jobsRoute(jobs ...fakeJob) func(url.Values) (int, string) {
	return func(q url.Values) (int, string) {
		var out []string
		for _, j := range jobs {
			if j.retried && q.Get("include_retried") != "true" {
				continue
			}
			if scope := q["scope[]"]; len(scope) > 0 && !slices.Contains(scope, j.status) {
				continue
			}
			out = append(out, fmt.Sprintf(`{"id":%d,"name":%q,"stage":"test","status":%q,"allow_failure":%t}`, j.id, j.name, j.status, j.allowFailure))
		}
		return 200, "[" + strings.Join(out, ",") + "]"
	}
}

// tree is a pipeline (100, failed) with two downstream pipelines: 200 in
// another project (status given) and 300 in the same project (success).
func tree(status200 string) *fake {
	const (
		parent = "projects/group/project/pipelines/100"
		e2e    = "projects/group/e2e/pipelines/200"
		deploy = "projects/group/project/pipelines/300"
	)
	return &fake{routes: map[string]func(url.Values) (int, string){
		parent: ok(`{"id":100,"status":"failed","web_url":"https://gitlab.com/group/project/-/pipelines/100"}`),
		parent + "/jobs": jobsRoute(
			fakeJob{id: 1, name: "build", status: "success"},
			fakeJob{id: 2, name: "test", status: "failed"},
			fakeJob{id: 3, name: "lint", status: "failed", allowFailure: true},
			// flaky failed, was retried, and passed.
			fakeJob{id: 4, name: "flaky", status: "failed", retried: true},
			fakeJob{id: 5, name: "flaky", status: "success"},
		),
		parent + "/bridges": ok(`[
			{"id":10,"name":"e2e","status":"` + status200 + `","downstream_pipeline":{"id":200,"project_id":6,"status":"` + status200 + `","web_url":"https://gitlab.com/group/e2e/-/pipelines/200"}},
			{"id":11,"name":"deploy","status":"success","downstream_pipeline":{"id":300,"project_id":5,"status":"success","web_url":"https://gitlab.com/group/project/-/pipelines/300"}}]`),
		e2e + "/jobs":       jobsRoute(fakeJob{id: 20, name: "e2e-1", status: "failed"}, fakeJob{id: 21, name: "e2e-2", status: "success"}),
		e2e + "/bridges":    ok(`[]`),
		deploy + "/jobs":    jobsRoute(fakeJob{id: 30, name: "deploy", status: "success"}),
		deploy + "/bridges": ok(`[]`),
	}}
}

var treeJobs = []string{
	"projects/group/project/pipelines/100/jobs",
	"projects/group/e2e/pipelines/200/jobs",
	"projects/group/project/pipelines/300/jobs",
}

func TestCollectAsksGitLabForFailedJobsOnly(t *testing.T) {
	f := tree("failed")
	r, err := Collect(context.Background(), f.client(t), "group/project", 100, Options{Downstream: true, MaxDepth: 4})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range treeJobs {
		asked := f.asked(path)
		if len(asked) != 1 {
			t.Errorf("%s: %d requests, want 1", path, len(asked))
			continue
		}
		q := asked[0]
		if !slices.Equal(q["scope[]"], []string{"failed"}) {
			t.Errorf("%s: want scope[]=failed, got %s", path, q.Encode())
		}
		if q.Get("include_retried") == "true" {
			t.Errorf("%s: retried jobs must not be asked for, got %s", path, q.Encode())
		}
	}
	var names []string
	for _, j := range r.FailedJobs {
		names = append(names, j.Name+"@"+j.Project)
	}
	// Not lint (allowed to fail), not flaky (passed when retried).
	if want := []string{"test@group/project", "e2e-1@group/e2e"}; !slices.Equal(names, want) {
		t.Errorf("failed jobs: %v, want %v", names, want)
	}
	if r.AllowedFailures != 1 || r.Running || r.Truncated || len(r.Downstream) != 2 {
		t.Errorf("want 1 allowed failure, finished, 2 downstream pipelines; got %+v", r)
	}
}

func TestCollectIncludeAllowed(t *testing.T) {
	f := tree("failed")
	r, err := Collect(context.Background(), f.client(t), "group/project", 100, Options{Downstream: true, MaxDepth: 4, IncludeAllowed: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.FailedJobs) != 3 || r.AllowedFailures != 0 {
		t.Errorf("want test, lint and e2e-1, got %+v", r.FailedJobs)
	}
}

// What "pipeline wait" asks at each poll: the pipeline and the trigger
// jobs of each pipeline of the tree, and no jobs.
func TestCollectStatusOnlyReadsNoJobs(t *testing.T) {
	f := tree("running")
	r, err := Collect(context.Background(), f.client(t), "group/project", 100, Options{Downstream: true, MaxDepth: 4, StatusOnly: true, LogLines: 40})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Running || len(r.Downstream) != 2 || len(r.FailedJobs) != 0 {
		t.Errorf("want a running tree of 2 downstream pipelines and no jobs, got %+v", r)
	}
	for _, path := range treeJobs {
		if n := len(f.asked(path)); n != 0 {
			t.Errorf("%s: %d requests, want none", path, n)
		}
	}
	if n := f.total(); n != 4 {
		t.Errorf("%d requests, want 4: the pipeline, and the trigger jobs of the 3 pipelines", n)
	}
}

// The logs are read in parallel: this test is also there for the race
// detector.
func TestCollectWarnsAboutLogsItCannotRead(t *testing.T) {
	f := tree("failed")
	f.routes["projects/group/project/jobs/2/trace"] = ok("$ make test\nFAIL: TestX\n")
	// No log for job 3 (lint): a 404, as for an erased job.
	// The token cannot read the jobs of the downstream project.
	f.routes["projects/group/e2e/jobs/20/trace"] = func(url.Values) (int, string) { return 403, `{"message":"403 Forbidden"}` }
	r, err := Collect(context.Background(), f.client(t), "group/project", 100, Options{Downstream: true, MaxDepth: 4, IncludeAllowed: true, LogLines: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.FailedJobs) != 3 {
		t.Fatalf("want test, lint and e2e-1, got %+v", r.FailedJobs)
	}
	if got := r.FailedJobs[0].Log; got != "$ make test\nFAIL: TestX" {
		t.Errorf("log of test: %q", got)
	}
	if r.FailedJobs[1].Log != "" || r.FailedJobs[2].Log != "" {
		t.Errorf("want no log for lint and e2e-1, got %+v", r.FailedJobs)
	}
	// One warning for each, in the order of the jobs, with the job and
	// the reason.
	if len(r.Warnings) != 2 ||
		!strings.Contains(r.Warnings[0], `"lint" (3, in group/project)`) || !strings.Contains(r.Warnings[0], "HTTP 404") ||
		!strings.Contains(r.Warnings[1], `"e2e-1" (20, in group/e2e)`) || !strings.Contains(r.Warnings[1], "HTTP 403") {
		t.Errorf("warnings: %q", r.Warnings)
	}
	// The command prints them; they are not in the report's own JSON.
	if b, err := json.Marshal(r); err != nil || strings.Contains(strings.ToLower(string(b)), "warnings") {
		t.Errorf("the report's JSON must not have the warnings: %s, %v", b, err)
	}
}

func TestCollectWithoutLogsHasNoWarnings(t *testing.T) {
	f := tree("failed")
	// No route for the logs: asking for one would be a 404 and a warning.
	r, err := Collect(context.Background(), f.client(t), "group/project", 100, Options{Downstream: true, MaxDepth: 4})
	if err != nil || len(r.Warnings) != 0 {
		t.Errorf("want no warnings, got %q, %v", r.Warnings, err)
	}
}
