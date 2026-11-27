package mr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/maelvls/glcli/internal/config"
	"github.com/maelvls/glcli/internal/gitlab"
)

// compareServer answers the compare API and counts the requests.
type compareServer struct {
	mu sync.Mutex
	// diffs is the diff of each "base..head"; "too large" stands for a
	// file GitLab leaves out. A missing key is a 404.
	diffs map[string]string
	// status, when set, is answered to every request.
	status int
	calls  int
}

func (f *compareServer) set(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

func (f *compareServer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *compareServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	d, ok := f.diffs[r.URL.Query().Get("from")+".."+r.URL.Query().Get("to")]
	switch {
	case f.status != 0:
		http.Error(w, `{"message":"failed"}`, f.status)
	case !ok:
		http.Error(w, `{"message":"404 Not found"}`, 404)
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{
			"diffs": []map[string]any{{"old_path": "a.go", "new_path": "a.go", "diff": d, "too_large": d == "too large"}},
		})
	}
}

// client returns a client of f. The host is named after the test: the
// cache of SamePatch lives as long as the process, and is per host.
func (f *compareServer) client(t *testing.T) *gitlab.Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := gitlab.New(config.Host{Name: t.Name(), Token: "x"})
	c.BaseURL = srv.URL
	return c
}

func version(base, head string) gitlab.Version {
	return gitlab.Version{BaseCommitSHA: base, HeadCommitSHA: head}
}

func TestSamePatch(t *testing.T) {
	f := &compareServer{diffs: map[string]string{
		"base1..head1": "@@ -1,2 +1,2 @@\n ctx\n-old\n+new\n",
		"base2..head2": "@@ -40,2 +40,2 @@\n other ctx\n-old\n+new\n", // head1, rebased
		"base2..head3": "@@ -40,2 +40,2 @@\n other ctx\n-old\n+newer\n",
	}}
	c := f.client(t)
	ctx := context.Background()
	v1, v2, v3 := version("base1", "head1"), version("base2", "head2"), version("base2", "head3")

	if same, known, err := SamePatch(ctx, c, 1, v1, v2); err != nil || !known || !same {
		t.Errorf("a rebase: same = %v, known = %v, err = %v", same, known, err)
	}
	if same, known, err := SamePatch(ctx, c, 1, v1, v3); err != nil || !known || same {
		t.Errorf("a change: same = %v, known = %v, err = %v", same, known, err)
	}
	if got := f.count(); got != 3 {
		t.Errorf("%d compare calls, want 3: one per version", got)
	}

	// Asking again costs nothing.
	if same, known, err := SamePatch(ctx, c, 1, v1, v2); err != nil || !known || !same {
		t.Errorf("a rebase, again: same = %v, known = %v, err = %v", same, known, err)
	}
	if got := f.count(); got != 3 {
		t.Errorf("%d compare calls after asking again, want 3", got)
	}

	// The same commits in another project are other commits.
	if _, _, err := SamePatch(ctx, c, 2, v1, v2); err != nil {
		t.Fatal(err)
	}
	if got := f.count(); got != 5 {
		t.Errorf("%d compare calls after asking about another project, want 5", got)
	}
}

func TestSamePatchFailureIsNotCached(t *testing.T) {
	f := &compareServer{diffs: map[string]string{
		"base1..head1": "@@ -1 +1 @@\n-old\n+new\n",
		"base2..head2": "@@ -7 +7 @@\n-old\n+new\n",
	}}
	c := f.client(t)
	ctx := context.Background()
	v1, v2 := version("base1", "head1"), version("base2", "head2")

	f.set(500)
	if same, known, err := SamePatch(ctx, c, 1, v1, v2); err == nil || known || same {
		t.Errorf("GitLab failing: same = %v, known = %v, err = %v", same, known, err)
	}
	f.set(0)
	if same, known, err := SamePatch(ctx, c, 1, v1, v2); err != nil || !known || !same {
		t.Errorf("GitLab back: same = %v, known = %v, err = %v", same, known, err)
	}
}

func TestSamePatchUnknown(t *testing.T) {
	f := &compareServer{diffs: map[string]string{
		"base1..head1": "@@ -1 +1 @@\n-old\n+new\n",
		"base3..head3": "too large",
	}}
	c := f.client(t)
	ctx := context.Background()
	v1, gone, large := version("base1", "head1"), version("base2", "head2"), version("base3", "head3")

	// A commit that is gone (404) is unknown, not an error, and is asked
	// about again: only the answers are cached.
	for range 2 {
		if same, known, err := SamePatch(ctx, c, 1, v1, gone); err != nil || known || same {
			t.Errorf("a commit gone: same = %v, known = %v, err = %v", same, known, err)
		}
	}
	if got := f.count(); got != 3 {
		t.Errorf("%d compare calls, want 3: head1 once, the gone commit twice", got)
	}

	// A diff too large is an answer: unknown for good.
	for range 2 {
		if same, known, err := SamePatch(ctx, c, 1, v1, large); err != nil || known || same {
			t.Errorf("too large: same = %v, known = %v, err = %v", same, known, err)
		}
	}
	if got := f.count(); got != 4 {
		t.Errorf("%d compare calls, want 4: the large diff once", got)
	}
}
