package pipeline

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

// The tests below use tree: pipeline 100 of group/project started
// pipeline 200 in group/e2e (trigger job "e2e") and pipeline 300 in its
// own project (trigger job "deploy").
const (
	e2eJobs    = "projects/6/pipelines/200/jobs"
	e2eBridges = "projects/6/pipelines/200/bridges"
)

func answer(status int) func(url.Values) (int, string) {
	return func(url.Values) (int, string) {
		return status, `{"message":"` + map[int]string{401: "401 Unauthorized", 403: "403 Forbidden", 404: "404 Project Not Found", 500: "500 Internal Server Error"}[status] + `"}`
	}
}

var whole = Options{Downstream: true, MaxDepth: 4}

func failedNames(r *Report) []string {
	var names []string
	for _, j := range r.FailedJobs {
		names = append(names, j.Name)
	}
	return names
}

func downstreamIDs(r *Report) []int {
	var ids []int
	for _, s := range r.Downstream {
		ids = append(ids, s.ID)
	}
	return ids
}

// has checks the one warning of r: what it must say, and what it must not.
func has(t *testing.T, r *Report, want []string, not ...string) {
	t.Helper()
	if len(r.Warnings) != 1 {
		t.Fatalf("want one warning, got %q", r.Warnings)
	}
	for _, s := range want {
		if !strings.Contains(r.Warnings[0], s) {
			t.Errorf("the warning lacks %q: %s", s, r.Warnings[0])
		}
	}
	for _, s := range not {
		if strings.Contains(r.Warnings[0], s) {
			t.Errorf("the warning must not have %q: %s", s, r.Warnings[0])
		}
	}
}

// marked lists the pipelines of r that are marked as unreadable; 0 is
// the pipeline the report is about.
func marked(r *Report) []int {
	var ids []int
	if r.Pipeline.Unreadable {
		ids = append(ids, 0)
	}
	for _, s := range r.Downstream {
		if s.Unreadable {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

// An agent is not to depend on the prose of a warning: the pipeline that
// could not be read is marked in the JSON, and only that one.
func TestCollectMarksThePipelinesItCouldNotRead(t *testing.T) {
	for _, tt := range []struct {
		name string
		fake func() *fake
		want []int
	}{
		{"all readable", func() *fake { return tree("failed") }, nil},
		{"trigger jobs refused", func() *fake { f := tree("failed"); f.routes[e2eBridges] = answer(403); return f }, []int{200}},
		{"jobs refused", func() *fake { f := tree("failed"); f.routes[e2eJobs] = answer(403); return f }, []int{200}},
		{"second level", func() *fake { return deeper("failed", "failed") }, []int{400}},
	} {
		r, err := Collect(context.Background(), tt.fake().client(t), "group/project", 100, whole)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if got := marked(r); !slices.Equal(got, tt.want) || r.Partial() != (len(tt.want) > 0) {
			t.Errorf("%s: unreadable pipelines %v (partial: %v), want %v", tt.name, got, r.Partial(), tt.want)
		}
		b, err := json.Marshal(r)
		if n := strings.Count(string(b), `"unreadable":true`); err != nil || n != len(tt.want) {
			t.Errorf("%s: \"unreadable\": true %d times in the JSON, want %d: %s, %v", tt.name, n, len(tt.want), b, err)
		}
		if len(tt.want) == 0 && strings.Contains(string(b), "unreadable") {
			t.Errorf("%s: no \"unreadable\" key is wanted for a pipeline that was read: %s", tt.name, b)
		}
	}
}

// The trigger job gives no web_url for the pipeline: the warning still
// says that it failed and that failed_jobs does not have the reason.
func TestCollectUnreadableFailedDownstreamWithoutURL(t *testing.T) {
	f := tree("failed")
	f.routes["projects/group/project/pipelines/100/bridges"] = ok(`[{"id":10,"name":"e2e","status":"failed","downstream_pipeline":{"id":200,"project_id":6,"status":"failed"}}]`)
	f.routes[e2eJobs] = answer(403)
	r, err := Collect(context.Background(), f.client(t), "group/project", 100, whole)
	if err != nil {
		t.Fatal(err)
	}
	has(t, r, []string{"the downstream pipeline 200 (in 6,", "its failed jobs"}, "see ")
	if !strings.HasSuffix(r.Warnings[0], `Missing from this result: its failed jobs. It failed, and "failed_jobs" does not say why.`) {
		t.Errorf("the warning must end on the failure: %s", r.Warnings[0])
	}
}

// who is how the warnings name pipeline 200.
const who = `the downstream pipeline 200 (in group/e2e, started by the trigger job "e2e" of pipeline 100)`

// GitLab refuses the trigger jobs of a downstream pipeline: its failed
// jobs are still read, and the rest of the tree too.
func TestCollectDownstreamTriggerJobsRefused(t *testing.T) {
	f := tree("failed")
	f.routes[e2eBridges] = answer(403)
	r, err := Collect(context.Background(), f.client(t), "group/project", 100, whole)
	if err != nil {
		t.Fatalf("a downstream pipeline that cannot be read must not fail the report: %v", err)
	}
	if got := downstreamIDs(r); !slices.Equal(got, []int{200, 300}) || r.Downstream[0].Status != "failed" {
		t.Errorf("downstream: %+v, want 200 (failed) and 300", r.Downstream)
	}
	if got := failedNames(r); !slices.Equal(got, []string{"test", "e2e-1"}) {
		t.Errorf("failed jobs: %v, want test and e2e-1", got)
	}
	if r.Running {
		t.Error("every pipeline has finished: want running false")
	}
	has(t, r, []string{who, "HTTP 403", "Its status (failed) is the one its trigger job reports",
		`its own downstream pipelines, which "pipeline wait" does not wait for (they may still be running, or have failed)`}, "its failed jobs", "It failed")
	// The pipeline after the unreadable one is still followed.
	if len(f.asked("projects/5/pipelines/300/bridges")) != 1 {
		t.Error("the trigger jobs of pipeline 300 were not read")
	}
}

// GitLab refuses the jobs of a downstream pipeline that failed: the
// warning says that failed_jobs does not have the reason.
func TestCollectDownstreamJobsRefused(t *testing.T) {
	f := tree("failed")
	f.routes[e2eJobs] = answer(403)
	r, err := Collect(context.Background(), f.client(t), "group/project", 100, whole)
	if err != nil {
		t.Fatalf("a downstream pipeline that cannot be read must not fail the report: %v", err)
	}
	if got := downstreamIDs(r); !slices.Equal(got, []int{200, 300}) || r.Downstream[0].Status != "failed" {
		t.Errorf("downstream: %+v, want 200 (failed) and 300", r.Downstream)
	}
	if got := failedNames(r); !slices.Equal(got, []string{"test"}) {
		t.Errorf("failed jobs: %v, want test only", got)
	}
	has(t, r, []string{who, "HTTP 403", "its failed jobs", `It failed, and "failed_jobs" does not say why: see https://gitlab.com/group/e2e/-/pipelines/200`}, "its own downstream pipelines")
	// Its trigger jobs are still asked for: they may be readable.
	if len(f.asked(e2eBridges)) != 1 {
		t.Error("the trigger jobs of pipeline 200 were not read")
	}
}

// The token does not see the downstream project at all: GitLab answers
// 404 for all that is in it. One warning says it all.
func TestCollectDownstreamProjectNotVisible(t *testing.T) {
	for _, status := range []string{"failed", "success"} {
		f := tree(status)
		delete(f.routes, e2eJobs)
		delete(f.routes, e2eBridges)
		r, err := Collect(context.Background(), f.client(t), "group/project", 100, whole)
		if err != nil {
			t.Fatalf("a downstream pipeline that cannot be read must not fail the report: %v", err)
		}
		if got := downstreamIDs(r); !slices.Equal(got, []int{200, 300}) || r.Downstream[0].Status != status || r.Downstream[0].Bridge != "e2e" || r.Downstream[0].Parent != 100 {
			t.Errorf("downstream: %+v, want 200 (%s) and 300", r.Downstream, status)
		}
		if got := failedNames(r); !slices.Equal(got, []string{"test"}) || r.Running {
			t.Errorf("want a finished tree and the failed job test, got %v, running %v", got, r.Running)
		}
		want := []string{who, "HTTP 404", "Its status (" + status + ")", "its failed jobs; its own downstream pipelines"}
		if status == "failed" {
			has(t, r, append(want, "It failed"))
		} else {
			has(t, r, want, "It failed")
		}
	}
}

// deeper adds a pipeline 400 of group/secret, started by the trigger job
// "publish" of pipeline 200: a project that answers 404.
func deeper(status200, status400 string) *fake {
	f := tree(status200)
	f.routes[e2eBridges] = ok(`[{"id":40,"name":"publish","status":"` + status400 + `","downstream_pipeline":{"id":400,"project_id":7,"status":"` + status400 + `","web_url":"https://gitlab.com/group/secret/-/pipelines/400"}}]`)
	return f
}

// A second-level downstream pipeline that cannot be read.
func TestCollectSecondLevelDownstreamNotVisible(t *testing.T) {
	f := deeper("failed", "failed")
	r, err := Collect(context.Background(), f.client(t), "group/project", 100, whole)
	if err != nil {
		t.Fatalf("a downstream pipeline that cannot be read must not fail the report: %v", err)
	}
	if got := downstreamIDs(r); !slices.Equal(got, []int{200, 400, 300}) {
		t.Fatalf("downstream: %+v, want 200, 400 and 300", r.Downstream)
	}
	if s := r.Downstream[1]; s.Status != "failed" || s.Project != "group/secret" || s.Bridge != "publish" || s.Parent != 200 || s.Depth != 2 {
		t.Errorf("pipeline 400: %+v", s)
	}
	if got := failedNames(r); !slices.Equal(got, []string{"test", "e2e-1"}) {
		t.Errorf("failed jobs: %v, want test and e2e-1", got)
	}
	has(t, r, []string{`the downstream pipeline 400 (in group/secret, started by the trigger job "publish" of pipeline 200)`, "HTTP 404", "its failed jobs; its own downstream pipelines", "It failed"})
}

// What "pipeline wait" asks at each poll. The status of a downstream
// pipeline that cannot be read is the one its trigger job reports, read
// with the trigger jobs of its parent at each call: the tree is running
// until that says the pipeline has finished.
func TestCollectStatusOnlyFollowsAnUnreadableDownstreamPipeline(t *testing.T) {
	polls := Options{Downstream: true, MaxDepth: 4, StatusOnly: true}
	for _, tt := range []struct {
		name    string
		fake    func(status string) *fake
		parent  string
		warning string
	}{
		{"first level", func(status string) *fake {
			f := tree(status)
			f.routes[e2eBridges] = answer(403)
			return f
		}, "projects/group/project/pipelines/100/bridges", "pipeline 200"},
		{"second level", func(status string) *fake { return deeper("success", status) }, e2eBridges, "pipeline 400"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status := "running"
			f := tt.fake(status)
			// The parent's trigger jobs give the status of the moment.
			at := f.routes[tt.parent]
			f.routes[tt.parent] = func(q url.Values) (int, string) {
				_, body := at(q)
				return 200, strings.ReplaceAll(body, `"running"`, `"`+status+`"`)
			}
			c := f.client(t)
			for poll, want := range []struct {
				status  string
				running bool
			}{{"running", true}, {"running", true}, {"failed", false}} {
				status = want.status
				r, err := Collect(context.Background(), c, "group/project", 100, polls)
				if err != nil {
					t.Fatalf("poll %d: %v", poll, err)
				}
				if r.Running != want.running {
					t.Errorf("poll %d, the unreadable pipeline is %s: running %v, want %v", poll, want.status, r.Running, want.running)
				}
				// A new report at each poll: the warning is not repeated.
				has(t, r, []string{tt.warning, "Its status (" + want.status + ")", "its own downstream pipelines"}, "its failed jobs", "It failed")
				if n := len(f.asked(tt.parent)); n != poll+1 {
					t.Errorf("poll %d: the trigger jobs of the parent were read %d times, want once for each poll", poll, n)
				}
			}
			for _, path := range treeJobs {
				if n := len(f.asked(path)); n != 0 {
					t.Errorf("%s: %d requests, want none", path, n)
				}
			}
		})
	}
}

// An error that is not a refusal still fails the report, for
// "pipeline wait" to try again (500) or to stop (401).
func TestCollectDownstreamErrorsThatAreNotRefusals(t *testing.T) {
	for _, path := range []string{e2eJobs, e2eBridges} {
		for _, status := range []int{500, 401} {
			f := tree("failed")
			f.routes[path] = answer(status)
			r, err := Collect(context.Background(), f.client(t), "group/project", 100, whole)
			if r != nil || !gitlab.IsStatus(err, status) {
				t.Errorf("%s answers %d: Collect = %+v, %v; want that error", path, status, r, err)
			}
			if got := gitlab.IsTransient(err); got != (status == 500) {
				t.Errorf("%s answers %d: transient %v", path, status, got)
			}
		}
	}
}

// The pipeline the report is about: a refusal is an error, as before.
func TestCollectRootRefusedIsAnError(t *testing.T) {
	const root = "projects/group/project/pipelines/100"
	for _, path := range []string{root, root + "/jobs", root + "/bridges"} {
		for _, status := range []int{403, 404} {
			f := tree("failed")
			f.routes[path] = answer(status)
			r, err := Collect(context.Background(), f.client(t), "group/project", 100, whole)
			if r != nil || !gitlab.IsStatus(err, status) {
				t.Errorf("%s answers %d: Collect = %+v, %v; want that error", path, status, r, err)
			}
		}
	}
}

// The log of a failed job is read after the tree: the warnings for the
// pipelines come first, then those for the logs.
func TestCollectUnreadableDownstreamAndLogWarnings(t *testing.T) {
	f := tree("failed")
	f.routes[e2eBridges] = answer(403)
	f.routes["projects/group/project/jobs/2/trace"] = ok("FAIL\n")
	// No log for job 20 (e2e-1): 404.
	r, err := Collect(context.Background(), f.client(t), "group/project", 100, Options{Downstream: true, MaxDepth: 4, LogLines: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Warnings) != 2 || !strings.Contains(r.Warnings[0], who) || !strings.Contains(r.Warnings[1], `the log of the failed job "e2e-1"`) {
		t.Errorf("warnings: %q", r.Warnings)
	}
}
