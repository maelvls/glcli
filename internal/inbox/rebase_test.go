package inbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/maelvls/glcli/internal/config"
	"github.com/maelvls/glcli/internal/gitlab"
	"github.com/maelvls/glcli/internal/mr"
)

func version(id, min int, base, head string) gitlab.Version {
	return gitlab.Version{ID: id, CreatedAt: at(min), BaseCommitSHA: base, HeadCommitSHA: head}
}

// The diff versions of a merge request, newest first: opened at :02,
// rebased at :05 (same patch as v1), changed at :08.
var (
	v1 = version(1, 2, "base1", "head1")
	v2 = version(2, 5, "base2", "head2")
	v3 = version(3, 8, "base2", "head3")
)

// patches is the patch each head carries, for the fake "same".
var patches = map[string]string{"head1": "A", "head2": "A", "head3": "B"}

func TestReviewRebaseOnly(t *testing.T) {
	approved := gitlab.Approvals{ApprovedBy: []struct {
		User gitlab.User `json:"user"`
	}{{User: gitlab.User{Username: "me"}}}}
	tests := []struct {
		name     string
		versions []gitlab.Version
		notes    []gitlab.Note
		edit     func(*mr.Snapshot)
		want     []string
		// asked is the pair of versions compared: "seen..newest".
		asked string
	}{
		{
			name:     "reviewed, then rebased",
			versions: []gitlab.Version{v2, v1},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false)},
			asked:    "head1..head2",
		},
		{
			name:     "reviewed, then changed",
			versions: []gitlab.Version{v3, v1},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false)},
			want:     []string{"new_commits"},
			asked:    "head1..head3",
		},
		{
			name:     "reviewed, then rebased, then changed",
			versions: []gitlab.Version{v3, v2, v1},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false)},
			want:     []string{"new_commits"},
			asked:    "head1..head3",
		},
		{
			name:     "reviewed the rebased version, then changed",
			versions: []gitlab.Version{v3, v2, v1},
			notes:    []gitlab.Note{note(1, "me", 6, "nit", false)},
			want:     []string{"new_commits"},
			asked:    "head2..head3",
		},
		{
			name:     "rebased, and a reply in my thread",
			versions: []gitlab.Version{v2, v1},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false), note(2, "alice", 6, "@me done", false)},
			want:     []string{"new_replies"},
			asked:    "head1..head2",
		},
		{
			name:     "my last action is older than every version fetched",
			versions: []gitlab.Version{v3, v2},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false)},
			want:     []string{"new_commits"},
		},
		{
			name:     "a push landed while fetching",
			versions: []gitlab.Version{v2, v1},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false)},
			edit:     func(s *mr.Snapshot) { s.Unsettled = true },
			want:     []string{"new_commits"},
		},
		{
			name:     "nothing pushed since my review: nothing to compare",
			versions: []gitlab.Version{v2, v1},
			notes:    []gitlab.Note{note(1, "me", 6, "nit", false)},
		},
		{
			name:     "approved by me: nothing to compare",
			versions: []gitlab.Version{v3, v1},
			notes:    []gitlab.Note{note(1, "me", 3, "approved this merge request", true)},
			edit:     func(s *mr.Snapshot) { s.Approvals = approved },
		},
		{
			name:     "review requested again: nothing to compare",
			versions: []gitlab.Version{v3, v1},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false), note(2, "bob", 9, "requested review from @me", true)},
			want:     []string{"review_requested"},
		},
	}
	for _, tt := range tests {
		s := snap("bob", tt.notes...)
		s.Versions = tt.versions
		if tt.edit != nil {
			tt.edit(s)
		}
		asked := ""
		got := reviewItemWith(s, me, func(seen, newest gitlab.Version) bool {
			asked += seen.HeadCommitSHA + ".." + newest.HeadCommitSHA
			return patches[seen.HeadCommitSHA] == patches[newest.HeadCommitSHA]
		})
		if !slices.Equal(got.Reasons, tt.want) {
			t.Errorf("%s: reasons = %v, want %v", tt.name, got.Reasons, tt.want)
		}
		if asked != tt.asked {
			t.Errorf("%s: compared %q, want %q", tt.name, asked, tt.asked)
		}
	}
}

// fakeGitLab serves one merge request that I review, with what Collect
// reads of it, and the compare API, whose requests it counts.
type fakeGitLab struct {
	t        *testing.T
	versions []gitlab.Version
	notes    []gitlab.Note
	// diffs is the diff of each "base..head". A missing key is a 404.
	diffs map[string]string

	mu sync.Mutex
	// compareStatus, when set, is answered to every compare request.
	compareStatus int
	compares      int
}

func (f *fakeGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m := gitlab.MergeRequest{
		ID: 10, IID: 1, ProjectID: 1, State: "opened", Author: gitlab.User{Username: "bob"},
		SHA: f.versions[0].HeadCommitSHA, WebURL: "https://gitlab.example/g/p/-/merge_requests/1", CreatedAt: at(0),
	}
	m.References.Full = "g/p!1"
	var out any
	switch path := r.URL.Path; {
	case path == "/api/v4/merge_requests":
		out = []gitlab.MergeRequest{m}
	case strings.HasSuffix(path, "/merge_requests/1"):
		out = m
	case strings.HasSuffix(path, "/approvals"):
		out = gitlab.Approvals{}
	case strings.HasSuffix(path, "/discussions"):
		ds := []gitlab.Discussion{}
		for _, n := range f.notes {
			ds = append(ds, gitlab.Discussion{ID: "d", Notes: []gitlab.Note{n}})
		}
		out = ds
	case strings.HasSuffix(path, "/versions"):
		out = f.versions
	case path == "/api/v4/projects/1/repository/compare":
		f.mu.Lock()
		defer f.mu.Unlock()
		f.compares++
		d, ok := f.diffs[r.URL.Query().Get("from")+".."+r.URL.Query().Get("to")]
		switch {
		case f.compareStatus != 0:
			http.Error(w, `{"message":"failed"}`, f.compareStatus)
			return
		case !ok:
			http.Error(w, `{"message":"404 Not found"}`, 404)
			return
		}
		out = map[string]any{"diffs": []map[string]string{{"old_path": "a.go", "new_path": "a.go", "diff": d}}}
	default:
		f.t.Errorf("unexpected request: %s", r.URL)
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fakeGitLab) set(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.compareStatus = status
}

func (f *fakeGitLab) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.compares
}

// collect runs Collect against f and returns the reasons of the merge
// request. The host is named after the test: the patch IDs are cached
// per host for as long as the process lives.
func (f *fakeGitLab) collect(t *testing.T) []string {
	t.Helper()
	f.t = t
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := gitlab.New(config.Host{Name: t.Name(), Token: "x"})
	c.BaseURL = srv.URL
	res, err := Collect(context.Background(), c, Options{Host: t.Name(), People: me, Roles: Roles{Review: true}})
	if err != nil {
		t.Fatalf("the inbox must not fail: %v", err)
	}
	if len(res.Warnings) != 0 || len(res.Items) != 1 {
		t.Fatalf("items = %+v, warnings = %v", res.Items, res.Warnings)
	}
	return res.Items[0].Reasons
}

var diffs = map[string]string{
	"base1..head1": "@@ -1,2 +1,2 @@\n ctx\n-old\n+new\n",
	"base2..head2": "@@ -40,2 +40,2 @@\n other ctx\n-old\n+new\n",
	"base2..head3": "@@ -40,2 +40,2 @@\n other ctx\n-old\n+newer\n",
}

func TestCollectLeavesRebasesOut(t *testing.T) {
	reviewed := []gitlab.Note{note(1, "me", 3, "nit", false)}
	tests := []struct {
		name     string
		versions []gitlab.Version
		want     []string
		compares int
	}{
		{"rebased", []gitlab.Version{v2, v1}, nil, 2},
		{"changed", []gitlab.Version{v3, v1}, []string{"new_commits"}, 2},
		{"rebased then changed", []gitlab.Version{v3, v2, v1}, []string{"new_commits"}, 2},
		{"older than every version", []gitlab.Version{v3, v2}, []string{"new_commits"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeGitLab{versions: tt.versions, notes: reviewed, diffs: diffs}
			if got := f.collect(t); !slices.Equal(got, tt.want) {
				t.Errorf("reasons = %v, want %v", got, tt.want)
			}
			if got := f.count(); got != tt.compares {
				t.Errorf("%d compare calls, want %d", got, tt.compares)
			}
		})
	}
}

// inbox wait runs Collect every two minutes: the second time must not
// ask GitLab again about the same two versions.
func TestCollectComparesOnce(t *testing.T) {
	f := &fakeGitLab{versions: []gitlab.Version{v2, v1}, notes: []gitlab.Note{note(1, "me", 3, "nit", false)}, diffs: diffs}
	for range 3 {
		if got := f.collect(t); len(got) != 0 {
			t.Errorf("reasons = %v, want none", got)
		}
	}
	if got := f.count(); got != 2 {
		t.Errorf("%d compare calls over three polls, want 2", got)
	}
}

func TestCollectKeepsThePushWhenCompareFails(t *testing.T) {
	for _, status := range []int{500, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := &fakeGitLab{versions: []gitlab.Version{v2, v1}, notes: []gitlab.Note{note(1, "me", 3, "nit", false)}, diffs: diffs}
			f.set(status)
			if got := f.collect(t); !slices.Equal(got, []string{"new_commits"}) {
				t.Errorf("reasons = %v, want new_commits", got)
			}
			// The failure was not cached: once GitLab answers, the rebase
			// is seen for what it is.
			f.set(0)
			if got := f.collect(t); len(got) != 0 {
				t.Errorf("reasons = %v once GitLab answers, want none", got)
			}
		})
	}
}
