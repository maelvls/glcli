package pipeline

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

// fromFork is the merge request group/project!1 (project 5) opened from
// the fork contributor/project (project 9). Its head pipeline (95) and
// pipeline 90 of the previous commit ran in the fork; a member of the
// parent project ran pipeline 93 there, for the previous commit.
func fromFork() *fake {
	const mrPath = "projects/group/project/merge_requests/1"
	in := func(id, projectID int, sha, status string) string {
		path := map[int]string{5: "group/project", 9: "contributor/project"}[projectID]
		return `{"id":` + strconv.Itoa(id) + `,"project_id":` + strconv.Itoa(projectID) + `,"sha":"` + sha + `","ref":"feature","status":"` + status +
			`","source":"merge_request_event","web_url":"https://gitlab.com/` + path + `/-/pipelines/` + strconv.Itoa(id) + `"}`
	}
	return &fake{routes: map[string]func(url.Values) (int, string){
		mrPath:                            ok(`{"iid":1,"project_id":5,"sha":"` + headSHA + `","head_pipeline":` + in(95, 9, headSHA, "running") + `}`),
		mrPath + "/pipelines":             ok(`[` + in(95, 9, headSHA, "running") + `,` + in(93, 5, oldSHA, "success") + `,` + in(90, 9, oldSHA, "failed") + `]`),
		"projects/9/pipelines/95":         ok(in(95, 9, headSHA, "failed")),
		"projects/9/pipelines/95/jobs":    jobsRoute(fakeJob{id: 1, name: "test", status: "failed"}),
		"projects/9/pipelines/95/bridges": ok(`[]`),
		"projects/9/jobs/1/trace":         ok("$ make test\nFAIL\n"),
	}}
}

func TestForMRTellsTheProjectOfAPipelineInTheFork(t *testing.T) {
	for _, sha := range []string{"", headSHA[:8]} {
		got, err := ForMR(context.Background(), fromFork().client(t), testMR, sha)
		if err != nil || got.ID != 95 || got.Fork == nil {
			t.Fatalf("ForMR(%q) = %+v, %v; want the pipeline 95, in the fork", sha, got, err)
		}
		want := Summary{ID: 95, Project: "contributor/project", Status: "running", URL: "https://gitlab.com/contributor/project/-/pipelines/95", SHA: headSHA, Ref: "feature", Source: "merge_request_event"}
		if *got.Fork != want || got.ForkProject != "9" {
			t.Errorf("ForMR(%q).Fork = %+v, to read from %q; want %+v, from 9", sha, *got.Fork, got.ForkProject, want)
		}
	}
}

// The newest pipeline of the previous commit ran in the parent project,
// the project of the merge request: no fork to read it from.
func TestForMRPipelineOfAForkMergeRequestRunInTheParentProject(t *testing.T) {
	got, err := ForMR(context.Background(), fromFork().client(t), testMR, oldSHA)
	if want := (Found{ID: 93, SHA: oldSHA, Head: headSHA}); err != nil || got != want {
		t.Errorf("ForMR = %+v, %v; want %+v", got, err, want)
	}
}

// An older pipeline in the fork, found in the list.
func TestForMROlderPipelineInTheFork(t *testing.T) {
	f := fromFork()
	f.routes["projects/group/project/merge_requests/1/pipelines"] = ok(`[
		{"id":95,"project_id":9,"sha":"` + headSHA + `","ref":"feature","status":"running"},
		{"id":90,"project_id":9,"sha":"` + oldSHA + `","ref":"feature","status":"failed"}]`)
	got, err := ForMR(context.Background(), f.client(t), testMR, oldSHA)
	// No web_url: the fork is known by its ID.
	if err != nil || got.ID != 90 || got.Fork == nil || got.ForkProject != "9" || got.Fork.Project != "9" || got.Fork.ID != 90 || got.Fork.Status != "failed" {
		t.Errorf("ForMR = %+v (fork %+v), %v; want the pipeline 90, in project 9", got, got.Fork, err)
	}
}

// GitLab lists the merge request pipelines before the branch pipelines,
// so the newest pipeline of a commit is not always the first listed: the
// project is the one of the pipeline kept, whatever the order.
func TestForMRProjectOfTheNewestPipelineOfTheCommit(t *testing.T) {
	const list = "projects/group/project/merge_requests/1/pipelines"
	head := `{"id":95,"project_id":9,"sha":"` + headSHA + `","ref":"feature","status":"running"}`
	// A member ran pipeline 90 in the parent project; the branch pipeline
	// 93 of the fork came later.
	f := fromFork()
	f.routes[list] = ok(`[` + head + `,
		{"id":90,"project_id":5,"sha":"` + oldSHA + `","ref":"refs/merge-requests/1/head","status":"failed","source":"merge_request_event"},
		{"id":93,"project_id":9,"sha":"` + oldSHA + `","ref":"feature","status":"success","source":"push"}]`)
	got, err := ForMR(context.Background(), f.client(t), testMR, oldSHA)
	if err != nil || got.ID != 93 || got.Fork == nil || got.Fork.ID != 93 || got.ForkProject != "9" {
		t.Errorf("ForMR = %+v (fork %+v), %v; want the pipeline 93, in project 9", got, got.Fork, err)
	}
	// The other way round: the newest is in the parent project.
	f = fromFork()
	f.routes[list] = ok(`[` + head + `,
		{"id":90,"project_id":9,"sha":"` + oldSHA + `","ref":"feature","status":"failed"},
		{"id":93,"project_id":5,"sha":"` + oldSHA + `","ref":"refs/merge-requests/1/head","status":"success"}]`)
	got, err = ForMR(context.Background(), f.client(t), testMR, oldSHA)
	if want := (Found{ID: 93, SHA: oldSHA, Head: headSHA}); err != nil || got != want {
		t.Errorf("ForMR = %+v, %v; want %+v", got, err, want)
	}
}

// A merged-results pipeline of a merge request from a fork runs in the
// parent project: GitLab keeps refs/merge-requests/<iid>/merge there, so
// its merge commit is read from the project of the merge request.
func TestForMRMergedResultsOfAForkMergeRequest(t *testing.T) {
	const mrPath = "projects/group/project/merge_requests/1"
	head := `{"id":95,"project_id":5,"sha":"m95","ref":"refs/merge-requests/1/merge","status":"running","web_url":"https://gitlab.com/group/project/-/pipelines/95"}`
	f := &fake{routes: map[string]func(url.Values) (int, string){
		mrPath:                              ok(`{"iid":1,"project_id":5,"source_project_id":9,"target_project_id":5,"sha":"` + headSHA + `","head_pipeline":` + head + `}`),
		mrPath + "/pipelines":               ok(`[` + head + `]`),
		"projects/5/repository/commits/m95": ok(`{"parent_ids":["` + targetSHA + `","` + headSHA + `"]}`),
	}}
	got, err := ForMR(context.Background(), f.client(t), testMR, "")
	if want := (Found{ID: 95, SHA: headSHA, Head: headSHA}); err != nil || got != want {
		t.Errorf("ForMR = %+v, %v; want %+v", got, err, want)
	}
	if n := len(f.asked("projects/5/repository/commits/m95")); n != 1 || f.total() != 2 {
		t.Errorf("want the merge commit read once from project 5, and the merge request; got %d and %d requests in all", n, f.total())
	}
}

// Collect reads everything from the project it is given: for a pipeline
// in a fork, the fork, named by its ID. The result names it by its path.
func TestCollectReadsThePipelineFromTheFork(t *testing.T) {
	f := fromFork()
	c := f.client(t)
	found, err := ForMR(context.Background(), c, testMR, "")
	if err != nil || found.Fork == nil {
		t.Fatalf("ForMR = %+v, %v", found, err)
	}
	r, err := Collect(context.Background(), c, found.ForkProject, found.ID, Options{Downstream: true, MaxDepth: 4, LogLines: 10})
	if err != nil {
		t.Fatal(err)
	}
	if r.Pipeline.Project != "contributor/project" || r.Pipeline.Status != "failed" || len(r.FailedJobs) != 1 ||
		r.FailedJobs[0].Project != "contributor/project" || r.FailedJobs[0].Log != "$ make test\nFAIL" || len(r.Warnings) != 0 {
		t.Errorf("want the pipeline and its failed job, with its log, from the fork; got %+v", r)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for path := range f.queries {
		if !strings.HasPrefix(path, "projects/9/") && !strings.Contains(path, "/merge_requests/") {
			t.Errorf("asked outside the fork: %s", path)
		}
	}
}

func TestFromMR(t *testing.T) {
	fork := Summary{ID: 95, Project: "contributor/project", Status: "running", URL: "https://gitlab.com/contributor/project/-/pipelines/95"}
	denied := &gitlab.APIError{Method: "GET", Path: "projects/contributor%2Fproject/pipelines/95", Status: 404, Message: "404 Project Not Found"}
	r := FromMR(fork, denied)
	if r.Pipeline != fork || !r.Running || len(r.FailedJobs) != 0 || len(r.Downstream) != 0 {
		t.Errorf("want the pipeline as the merge request reports it, running: %+v", r)
	}
	if len(r.Warnings) != 1 {
		t.Fatalf("want one warning, got %q", r.Warnings)
	}
	for _, want := range []string{"pipeline 95", "contributor/project", "the fork", "cannot read", "HTTP 404", "(running)", "failed jobs", "downstream pipelines"} {
		if !strings.Contains(r.Warnings[0], want) {
			t.Errorf("the warning lacks %q: %s", want, r.Warnings[0])
		}
	}
	fork.Status = "failed"
	if r := FromMR(fork, denied); r.Running || !strings.Contains(r.Warnings[0], "(failed)") {
		t.Errorf("want a finished pipeline, failed: %+v", r)
	}
}

func TestDenied(t *testing.T) {
	status := func(code int) error { return &gitlab.APIError{Method: "GET", Path: "x", Status: code} }
	for err, want := range map[error]bool{
		status(403): true, status(404): true,
		status(401): false, status(400): false, status(429): false, status(500): false,
		errors.New("connection refused"): false, nil: false,
	} {
		if got := Denied(err); got != want {
			t.Errorf("Denied(%v) = %v, want %v", err, got, want)
		}
	}
}
