package pipeline

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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
