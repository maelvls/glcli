package pipeline

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// relativeRoot is a GitLab served under https://host/gitlab: the path in
// a web_url is then not the path of the project. Pipeline 100 of
// group/project started pipeline 200 in group/e2e (project 6).
func relativeRoot() *fake {
	const (
		parent = "projects/group/project/pipelines/100"
		e2e    = "projects/6/pipelines/200"
	)
	return &fake{routes: map[string]func(url.Values) (int, string){
		parent:           ok(`{"id":100,"status":"failed","web_url":"https://host/gitlab/group/project/-/pipelines/100"}`),
		parent + "/jobs": jobsRoute(),
		parent + "/bridges": ok(`[{"id":10,"name":"e2e","status":"failed","downstream_pipeline":{"id":200,"project_id":6,` +
			`"status":"failed","web_url":"https://host/gitlab/group/e2e/-/pipelines/200"}}]`),
		e2e + "/jobs":              jobsRoute(fakeJob{id: 20, name: "e2e-1", status: "failed"}),
		e2e + "/bridges":           ok(`[]`),
		"projects/6/jobs/20/trace": ok("$ make e2e\nFAIL\n"),
	}}
}

// A downstream pipeline is asked for by the ID of its project, which
// GitLab gives, not by the path found in its web_url: the pipeline, its
// jobs, its trigger jobs and its logs. The path is what the result shows.
func TestCollectAsksForDownstreamPipelinesByProjectID(t *testing.T) {
	f := relativeRoot()
	r, err := Collect(context.Background(), f.client(t), "group/project", 100, Options{Downstream: true, MaxDepth: 4, LogLines: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Warnings) != 0 {
		t.Errorf("a pipeline that can be read by its project ID is not unreadable: %q", r.Warnings)
	}
	if len(r.Downstream) != 1 || r.Downstream[0].Project != "gitlab/group/e2e" {
		t.Errorf("downstream: %+v, want pipeline 200 shown with the path of its web_url", r.Downstream)
	}
	if len(r.FailedJobs) != 1 || r.FailedJobs[0].Name != "e2e-1" || r.FailedJobs[0].Project != "gitlab/group/e2e" || r.FailedJobs[0].Log != "$ make e2e\nFAIL" {
		t.Errorf("failed jobs: %+v, want e2e-1 with its log", r.FailedJobs)
	}
	var asked []string
	f.mu.Lock()
	for path := range f.queries {
		asked = append(asked, path)
	}
	f.mu.Unlock()
	slices.Sort(asked)
	want := []string{
		"projects/6/jobs/20/trace",
		"projects/6/pipelines/200/bridges",
		"projects/6/pipelines/200/jobs",
		"projects/group/project/pipelines/100",
		"projects/group/project/pipelines/100/bridges",
		"projects/group/project/pipelines/100/jobs",
	}
	if !slices.Equal(asked, want) {
		t.Errorf("asked:\n%s\nwant:\n%s", strings.Join(asked, "\n"), strings.Join(want, "\n"))
	}
}

// Without a project_id, the path of the web_url is all there is.
func TestCollectDownstreamPipelineWithoutProjectID(t *testing.T) {
	f := tree("failed")
	f.routes["projects/group/project/pipelines/100/bridges"] = ok(`[{"id":10,"name":"e2e","status":"failed","downstream_pipeline":{"id":200,"status":"failed","web_url":"https://gitlab.com/group/e2e/-/pipelines/200"}}]`)
	f.routes["projects/group/e2e/pipelines/200/jobs"] = jobsRoute(fakeJob{id: 20, name: "e2e-1", status: "failed"})
	f.routes["projects/group/e2e/pipelines/200/bridges"] = ok(`[]`)
	f.routes["projects/group/e2e/jobs/20/trace"] = ok("FAIL\n")
	r, err := Collect(context.Background(), f.client(t), "group/project", 100, Options{Downstream: true, MaxDepth: 4, LogLines: 10})
	if err != nil {
		t.Fatal(err)
	}
	if names := failedNames(r); !slices.Equal(names, []string{"test", "e2e-1"}) || r.FailedJobs[1].Log != "FAIL" || len(r.Warnings) != 1 {
		// The one warning: job 2 (test) has no log in this fake.
		t.Errorf("want test and e2e-1 with its log, got %+v, warnings %q", r.FailedJobs, r.Warnings)
	}
}
