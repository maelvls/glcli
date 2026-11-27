package pipeline

import (
	"context"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

// later makes path answer status from the moment the switch it returns is
// set: to be called before the fake is started.
func (f *fake) later(path string, status int) *atomic.Bool {
	var refused atomic.Bool
	before := f.routes[path]
	f.routes[path] = func(q url.Values) (int, string) {
		if refused.Load() {
			return answer(status)(q)
		}
		return before(q)
	}
	return &refused
}

// lostError checks the error for something GitLab showed earlier in the
// wait and refuses now: it names the pipeline and GitLab's answer, and it
// is neither a refusal to do without nor an error to try again on.
func lostError(t *testing.T, err error, name, status string) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error, got a report")
	}
	if Denied(err) || gitlab.IsTransient(err) {
		t.Errorf("the error must end the wait: denied %v, transient %v", Denied(err), gitlab.IsTransient(err))
	}
	for _, want := range []string{name + " could be read earlier in this wait and no longer can be", status} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q: %v", want, err)
		}
	}
}

// What GitLab showed of a downstream pipeline during a wait, and refuses
// at a later poll, is an error: the token lost its access, and a report
// without that pipeline's tree would know less than the poll before.
func TestCollectDownstreamRefusedLaterInTheSameWaitIsAnError(t *testing.T) {
	for _, status := range []int{403, 404} {
		f := tree("running")
		refused := f.later(e2eBridges, status)
		c := f.client(t)
		polls := Options{Downstream: true, MaxDepth: 4, StatusOnly: true, Memory: NewMemory()}
		for poll := range 2 {
			if r, err := Collect(context.Background(), c, "group/project", 100, polls); err != nil || !r.Running || r.Partial() {
				t.Fatalf("poll %d: %+v, %v", poll, r, err)
			}
		}
		refused.Store(true)
		r, err := Collect(context.Background(), c, "group/project", 100, polls)
		if r != nil {
			t.Errorf("want no report, got %+v", r)
		}
		lostError(t, err, "the downstream pipeline 200 (in group/e2e)", "HTTP "+map[int]string{403: "403", 404: "404"}[status])
	}
}

// The same for the pipeline the report is about, and for jobs that were
// read before.
func TestCollectRefusedLaterInTheSameWaitIsAnError(t *testing.T) {
	const root = "projects/group/project/pipelines/100"
	for _, tt := range []struct{ path, name string }{
		{root, "pipeline 100 (in group/project)"},
		{root + "/bridges", "pipeline 100 (in group/project)"},
		{root + "/jobs", "pipeline 100 (in group/project)"},
		{e2eJobs, "the downstream pipeline 200 (in group/e2e)"},
		{"projects/5/pipelines/300/jobs", "the downstream pipeline 300 (in group/project)"},
	} {
		f := tree("failed")
		refused := f.later(tt.path, 404)
		c := f.client(t)
		o := Options{Downstream: true, MaxDepth: 4, Memory: NewMemory()}
		if _, err := Collect(context.Background(), c, "group/project", 100, o); err != nil {
			t.Fatal(err)
		}
		refused.Store(true)
		_, err := Collect(context.Background(), c, "group/project", 100, o)
		lostError(t, err, tt.name, "HTTP 404")
	}
}

// What was refused from the first poll on is not lost: the report does
// without it at each poll. So do the jobs that are asked for at the end
// only, when they are refused then.
func TestCollectNeverReadableIsNotLost(t *testing.T) {
	f := tree("failed")
	f.routes[e2eBridges] = answer(403)
	refused := f.later(e2eJobs, 403)
	c := f.client(t)
	memory := NewMemory()
	polls := Options{Downstream: true, MaxDepth: 4, StatusOnly: true, Memory: memory}
	for poll := range 3 {
		r, err := Collect(context.Background(), c, "group/project", 100, polls)
		if err != nil {
			t.Fatalf("poll %d: %v", poll, err)
		}
		has(t, r, []string{who, "its own downstream pipelines"}, "its failed jobs")
	}
	refused.Store(true)
	r, err := Collect(context.Background(), c, "group/project", 100, Options{Downstream: true, MaxDepth: 4, Memory: memory})
	if err != nil {
		t.Fatalf("the read of the end: %v", err)
	}
	has(t, r, []string{who, "its failed jobs; its own downstream pipelines"})
}

// "pipeline get" has no memory: one report, made of what can be read.
func TestCollectWithoutMemoryForgets(t *testing.T) {
	f := tree("failed")
	refused := f.later(e2eBridges, 403)
	c := f.client(t)
	if _, err := Collect(context.Background(), c, "group/project", 100, whole); err != nil {
		t.Fatal(err)
	}
	refused.Store(true)
	r, err := Collect(context.Background(), c, "group/project", 100, whole)
	if err != nil {
		t.Fatalf("without a memory, a refusal is done without: %v", err)
	}
	has(t, r, []string{who})
}
