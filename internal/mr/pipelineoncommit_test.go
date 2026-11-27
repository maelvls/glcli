package mr

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maelvls/glcli/internal/config"
	"github.com/maelvls/glcli/internal/gitlab"
)

// commitHosts numbers the fake hosts: the parents of merge commits are
// cached per host, for as long as the process lives.
var commitHosts atomic.Int64

// commitServer is a GitLab that knows the parents of some commits of
// project 5 and answers 404 for the others. asked counts the requests.
func commitServer(t *testing.T, parents map[string][]string) (c *gitlab.Client, asked *atomic.Int64) {
	t.Helper()
	asked = &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		p, ok := parents[strings.TrimPrefix(r.URL.Path, "/api/v4/projects/5/repository/commits/")]
		if !ok {
			http.Error(w, `{"message":"404 Commit Not Found"}`, http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"parent_ids":["%s"]}`, strings.Join(p, `","`))
	}))
	t.Cleanup(srv.Close)
	c = gitlab.New(config.Host{Name: fmt.Sprintf("commits-%d", commitHosts.Add(1)), Token: "x"})
	c.BaseURL = srv.URL
	return c, asked
}

// mergedResultsMR is a merge request whose head pipeline ran on the
// merge commit "merge1".
func mergedResultsMR(ref string) gitlab.MergeRequest {
	return gitlab.MergeRequest{
		IID: 1, ProjectID: 5, SHA: "head1",
		HeadPipeline: &gitlab.PipelineRef{ID: 7, SHA: "merge1", Ref: ref},
	}
}

func TestPipelineIsHeadOfMergedResults(t *testing.T) {
	for _, tt := range []struct {
		name    string
		ref     string
		parents map[string][]string
		want    bool
	}{
		{"merge commit of the head", "refs/merge-requests/1/merge", map[string][]string{"merge1": {"target1", "head1"}}, true},
		{"merge-train commit of the head", "refs/merge-requests/1/train", map[string][]string{"merge1": {"target1", "head1"}}, true},
		{"merge commit of an older commit", "refs/merge-requests/1/merge", map[string][]string{"merge1": {"target1", "old1"}}, false},
		// As on main: "mr get" then prints the pipeline as stale.
		{"merge commit that cannot be read", "refs/merge-requests/1/merge", nil, false},
		{"a branch pipeline of another commit", "feature", map[string][]string{"merge1": {"target1", "head1"}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := commitServer(t, tt.parents)
			got, err := PipelineIsHead(context.Background(), c, mergedResultsMR(tt.ref))
			if err != nil || got != tt.want {
				t.Errorf("PipelineIsHead = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestPipelineIsHeadWithoutHeadPipeline(t *testing.T) {
	c, asked := commitServer(t, nil)
	m := gitlab.MergeRequest{IID: 1, ProjectID: 5, SHA: "head1"}
	if got, err := PipelineIsHead(context.Background(), c, m); err != nil || got {
		t.Errorf("PipelineIsHead = %v, %v; want false", got, err)
	}
	// The pipeline ran on the head commit itself: nothing to ask.
	m.HeadPipeline = &gitlab.PipelineRef{ID: 7, SHA: "head1", Ref: "refs/merge-requests/1/head"}
	if got, err := PipelineIsHead(context.Background(), c, m); err != nil || !got || asked.Load() != 0 {
		t.Errorf("PipelineIsHead = %v, %v after %d requests; want true without a request", got, err, asked.Load())
	}
}

// A merge commit that cannot be read is "unknown", not "no". It is asked
// for again when it is the head pipeline's, and once otherwise.
func TestPipelineOnCommitUnreadableMergeCommit(t *testing.T) {
	c, asked := commitServer(t, nil)
	m := mergedResultsMR("refs/merge-requests/1/merge")
	older := gitlab.PipelineRef{ID: 6, SHA: "merge0", Ref: "refs/merge-requests/1/merge"}
	for range 3 {
		for _, p := range []gitlab.PipelineRef{*m.HeadPipeline, older} {
			if on, err := PipelineOnCommit(context.Background(), c, m, p, "head1"); err != nil || on != OnCommitUnknown {
				t.Errorf("pipeline %d: PipelineOnCommit = %v, %v; want unknown", p.ID, on, err)
			}
		}
	}
	if n := asked.Load(); n != 4 {
		t.Errorf("%d requests, want 4: 3 for the head pipeline's commit, 1 for the older one", n)
	}
}
