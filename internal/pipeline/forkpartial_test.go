package pipeline

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

// With Options.Fork, the pipeline the report is about is one whose jobs
// or trigger jobs GitLab may refuse: the report does without them, as for
// a downstream pipeline, and marks the pipeline.
func TestCollectForkPipelineReadableInPart(t *testing.T) {
	const root = "projects/group/project/pipelines/100"
	fork := Options{Downstream: true, MaxDepth: 4, Fork: true}
	for _, tt := range []struct {
		refused    []string
		jobs       []string
		downstream int
		warning    []string
		not        []string
	}{
		{[]string{root + "/jobs"}, []string{"e2e-1"}, 2, []string{"its failed jobs", "It failed"}, []string{"its own downstream pipelines"}},
		{[]string{root + "/bridges"}, []string{"test"}, 0, []string{"its own downstream pipelines"}, []string{"its failed jobs", "It failed"}},
		{[]string{root + "/jobs", root + "/bridges"}, nil, 0, []string{"its failed jobs; its own downstream pipelines", "It failed"}, nil},
	} {
		f := tree("failed")
		for _, path := range tt.refused {
			f.routes[path] = answer(403)
		}
		r, err := Collect(context.Background(), f.client(t), "group/project", 100, fork)
		if err != nil {
			t.Fatalf("%v refused: %v", tt.refused, err)
		}
		if !slices.Equal(marked(r), []int{0}) || !slices.Equal(failedNames(r), tt.jobs) || len(r.Downstream) != tt.downstream || r.Pipeline.Status != "failed" || r.Running {
			t.Errorf("%v refused: want the pipeline marked, the failed jobs %v and %d downstream pipelines, got %+v", tt.refused, tt.jobs, tt.downstream, r)
		}
		has(t, r, append(tt.warning, "pipeline 100 of this merge request runs in another project, group/project (the fork the merge request comes from), where this token cannot read all of it", "HTTP 403"),
			append(tt.not, "trigger job reports", "merge request reports")...)
	}
}

// The pipeline itself refused is still an error for Collect, and one that
// is Denied: the caller then reports what the merge request says.
func TestCollectForkPipelineRefused(t *testing.T) {
	f := tree("failed")
	f.routes["projects/group/project/pipelines/100"] = answer(404)
	r, err := Collect(context.Background(), f.client(t), "group/project", 100, Options{Downstream: true, MaxDepth: 4, Fork: true})
	if r != nil || !Denied(err) || !gitlab.IsStatus(err, 404) {
		t.Errorf("Collect = %+v, %v; want the 404", r, err)
	}
}

// A downstream pipeline of a pipeline in a fork is warned about as any
// downstream pipeline.
func TestCollectForkPipelineWithUnreadableDownstream(t *testing.T) {
	f := tree("failed")
	f.routes[e2eJobs] = answer(403)
	r, err := Collect(context.Background(), f.client(t), "group/project", 100, Options{Downstream: true, MaxDepth: 4, Fork: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(marked(r), []int{200}) || !strings.Contains(strings.Join(r.Warnings, "\n"), who) {
		t.Errorf("want the pipeline 200 marked and warned about, got %+v", r)
	}
}
