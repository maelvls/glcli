package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maelvls/glcli/internal/config"
	"github.com/maelvls/glcli/internal/gitlab"
	"github.com/maelvls/glcli/internal/mr"
)

// version is a diff version pushed at 10:min, with GitLab's patch ID.
func version(min int, patch string) gitlab.Version {
	head := fmt.Sprintf("head%d", min)
	return gitlab.Version{ID: min, CreatedAt: at(min), BaseCommitSHA: "base", HeadCommitSHA: head, PatchIDSHA: patch}
}

func approvedBy(user string) gitlab.Approvals {
	return gitlab.Approvals{ApprovedBy: []struct {
		User gitlab.User `json:"user"`
	}{{User: gitlab.User{Username: user}}}}
}

func yes() bool { return true }

const (
	approvedNote   = "approved this merge request"
	unapprovedNote = "unapproved this merge request"
	retargetNote   = "changed target branch from `main` to `release`"
	deletedNote    = "deleted the `feature` branch. This merge request now targets the `main` branch"
)

type rebaseTest struct {
	name string
	// versions are newest first, like GitLab lists them.
	versions []gitlab.Version
	notes    []gitlab.Note
	edit     func(*mr.Snapshot)
	want     []string
	// at is the minute new_commits is dated from.
	at int
}

// In every case the merge request was opened at :02 with patch A, and I
// commented at :03 unless the notes say otherwise.
func rebaseTests() []rebaseTest {
	comment := []gitlab.Note{note(1, "me", 3, "nit", false)}
	approval := note(1, "me", 3, approvedNote, true)
	opened := version(2, "A")
	thirty := []gitlab.Version{opened}
	for min := 10; min < 40; min++ {
		thirty = append([]gitlab.Version{version(min, "A")}, thirty...)
	}
	return []rebaseTest{
		{
			name:     "rebased",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    comment,
		},
		{
			name:     "rebased thirty times",
			versions: thirty,
			notes:    comment,
		},
		{
			name:     "changed",
			versions: []gitlab.Version{version(5, "B"), opened},
			notes:    comment,
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "rebased, then changed",
			versions: []gitlab.Version{version(8, "B"), version(5, "A"), opened},
			notes:    comment,
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "changed, then rebased: dated from the change",
			versions: []gitlab.Version{version(8, "B"), version(5, "B"), opened},
			notes:    comment,
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "changed, then rebased twice: still dated from the change",
			versions: []gitlab.Version{version(9, "B"), version(8, "B"), version(5, "B"), opened},
			notes:    comment,
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "changed, changed again, then back to the first change: dated from the last push",
			versions: []gitlab.Version{version(10, "B"), version(8, "C"), version(5, "B"), opened},
			notes:    comment,
			want:     []string{"new_commits"}, at: 10,
		},
		{
			name:     "changed, then back to what I saw",
			versions: []gitlab.Version{version(8, "A"), version(5, "B"), opened},
			notes:    comment,
		},
		{
			name:     "I commented on the rebased version, then it changed",
			versions: []gitlab.Version{version(8, "B"), version(5, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 6, "nit", false)},
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "what I saw is the newest version before my comment, not the first one",
			versions: []gitlab.Version{version(8, "A"), version(5, "B"), opened},
			notes:    []gitlab.Note{note(1, "me", 6, "nit", false)},
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "a version pushed at the instant of my comment is one I saw",
			versions: []gitlab.Version{version(8, "A"), version(5, "A"), version(2, "B")},
			notes:    []gitlab.Note{note(1, "me", 5, "nit", false)},
		},
		{
			name:     "rebased, and a reply in my thread",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false), note(2, "alice", 6, "@me done", false)},
			want:     []string{"new_replies"},
		},

		// My approval.
		{
			name:     "approved by me, still: nothing",
			versions: []gitlab.Version{version(5, "B"), opened},
			notes:    []gitlab.Note{approval},
			edit:     func(s *mr.Snapshot) { s.Approvals = approvedBy("me") },
		},
		{
			name:     "a rebase made GitLab remove my approval",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    []gitlab.Note{approval},
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "my approval removed, then rebased again: dated from the first rebase",
			versions: []gitlab.Version{version(8, "A"), version(5, "A"), opened},
			notes:    []gitlab.Note{approval},
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "approved, commented, then a rebase removed my approval",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 4, "one more thing", false)},
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "approved, changed, then back to what I approved: my approval is gone",
			versions: []gitlab.Version{version(8, "A"), version(5, "B"), opened},
			notes:    []gitlab.Note{approval},
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "approved, changed, I commented on the change, then rebased: I know my approval is gone",
			versions: []gitlab.Version{version(8, "B"), version(5, "B"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 6, "fine", false)},
		},
		{
			name:     "approved, changed at the instant of my comment, then rebased: I know",
			versions: []gitlab.Version{version(8, "B"), version(6, "B"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 6, "fine", false)},
		},
		{
			name:     "approved, rebased (my approval stays), commented, changed, then back to what I approved: my approval is gone",
			versions: []gitlab.Version{version(10, "A"), version(8, "B"), version(5, "A"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 6, "by the way", false)},
			want:     []string{"new_commits"}, at: 10,
		},
		{
			name:     "approved, rebased (my approval stays), commented, then a rebase removed my approval",
			versions: []gitlab.Version{version(8, "A"), version(5, "A"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 6, "by the way", false)},
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "a push without a patch ID between my approval and my comment proves nothing",
			versions: []gitlab.Version{version(10, "A"), version(7, "A"), version(5, ""), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 8, "by the way", false)},
			want:     []string{"new_commits"}, at: 10,
		},
		{
			name:     "what I approved has no patch ID",
			versions: []gitlab.Version{version(8, "B"), version(5, "B"), version(2, "")},
			notes:    []gitlab.Note{approval, note(2, "me", 6, "fine", false)},
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "what I approved is the newest version before my approval, not the first one",
			versions: []gitlab.Version{version(10, "B"), version(7, "B"), version(4, "B"), opened},
			notes:    []gitlab.Note{note(1, "me", 5, approvedNote, true), note(2, "me", 8, "by the way", false)},
			want:     []string{"new_commits"}, at: 10,
		},
		{
			name:     "approved, my review was requested again, I commented, then rebased: I know my approval is gone",
			versions: []gitlab.Version{version(8, "A"), opened},
			notes:    []gitlab.Note{approval, note(2, "bob", 5, "requested review from @me", true), note(3, "me", 6, "again?", false)},
		},
		{
			name:     "what I approved is the version pushed at the instant of my approval, not the one before",
			versions: []gitlab.Version{version(10, "B"), version(5, "B"), version(3, "B"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 6, "by the way", false)},
			want:     []string{"new_commits"}, at: 10,
		},
		{
			name:     "my review was requested before my approval: it says nothing",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    []gitlab.Note{note(1, "bob", 1, "requested review from @me", true), note(2, "me", 3, approvedNote, true)},
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "the review of someone else was requested again",
			versions: []gitlab.Version{version(8, "A"), opened},
			notes:    []gitlab.Note{approval, note(2, "bob", 5, "requested review from @meg", true), note(3, "me", 6, "fine", false)},
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "a version pushed at the instant of my approval is the one I approved",
			versions: []gitlab.Version{version(5, "A"), version(3, "A")},
			notes:    []gitlab.Note{approval},
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "a version pushed at the instant of my last action is not unseen",
			versions: []gitlab.Version{version(8, "A"), version(5, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 5, approvedNote, true)},
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "I took my approval back myself, then rebased",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 4, unapprovedNote, true)},
		},
		{
			name:     "an unapproval at the instant of my approval: in doubt, approved",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 3, unapprovedNote, true)},
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "I requested changes after approving, then rebased",
			versions: []gitlab.Version{version(8, "A"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 6, "requested changes", true)},
		},
		{
			name:     "I took my approval back, approved again, and a rebase removed it",
			versions: []gitlab.Version{version(8, "A"), opened},
			notes: []gitlab.Note{
				approval,
				note(2, "me", 4, unapprovedNote, true),
				note(3, "me", 6, approvedNote, true),
			},
			want: []string{"new_commits"}, at: 8,
		},
		{
			name:     "the approval of someone else is not mine",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false), note(2, "alice", 4, approvedNote, true)},
		},

		// Another target branch.
		{
			name:     "retargeted, then rebased: dated from the rebase",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false), note(2, "bob", 4, retargetNote, true)},
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "rebased, then retargeted: dated from the retarget",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false), note(2, "bob", 8, retargetNote, true)},
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "rebased twice, then the target branch was deleted: dated from that",
			versions: []gitlab.Version{version(8, "A"), version(5, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false), note(2, "bob", 9, deletedNote, true)},
			want:     []string{"new_commits"}, at: 9,
		},
		{
			name:     "retargeted three times: dated from the last",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes: []gitlab.Note{
				note(1, "me", 3, "nit", false),
				note(2, "bob", 7, retargetNote, true),
				note(3, "bob", 9, retargetNote, true),
				note(4, "bob", 8, retargetNote, true),
			},
			want: []string{"new_commits"}, at: 9,
		},
		{
			name:     "changed, then retargeted: still dated from the change",
			versions: []gitlab.Version{version(5, "B"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false), note(2, "bob", 8, retargetNote, true)},
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "changed, rebased, then retargeted: still dated from the change",
			versions: []gitlab.Version{version(8, "B"), version(5, "B"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false), note(2, "bob", 9, retargetNote, true)},
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "a rebase removed my approval, then retargeted: dated from the retarget",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    []gitlab.Note{approval, note(2, "bob", 8, retargetNote, true)},
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "retargeted, then a rebase removed my approval: dated from the rebase",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    []gitlab.Note{approval, note(2, "bob", 4, retargetNote, true)},
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "retargeted before my review, then rebased",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    []gitlab.Note{note(1, "bob", 2, retargetNote, true), note(2, "me", 3, "nit", false)},
		},
		{
			name:     "retargeted at the instant of my review, then rebased",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    []gitlab.Note{note(1, "bob", 3, retargetNote, true), note(2, "me", 3, "nit", false)},
		},
		{
			name:     "someone only talks about a target branch",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false), note(2, "project_1_bot_x", 6, retargetNote, false)},
		},

		// Doubt.
		{
			name:     "no patch ID on the newest version",
			versions: []gitlab.Version{version(5, ""), opened},
			notes:    comment,
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "no patch ID on the version I saw",
			versions: []gitlab.Version{version(5, "A"), version(2, "")},
			notes:    comment,
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "no patch ID at all",
			versions: []gitlab.Version{version(5, ""), version(2, "")},
			notes:    comment,
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "no patch ID on the pushes: dated from the last one",
			versions: []gitlab.Version{version(8, ""), version(5, ""), opened},
			notes:    comment,
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "a push landed while fetching",
			versions: []gitlab.Version{version(8, "A"), version(5, "A"), opened},
			notes:    comment,
			edit:     func(s *mr.Snapshot) { s.Unsettled = true },
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "notes are missing",
			versions: []gitlab.Version{version(8, "A"), version(5, "A"), opened},
			notes:    comment,
			edit:     func(s *mr.Snapshot) { s.DiscussionsTruncated = true },
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "my last action is older than every version fetched",
			versions: []gitlab.Version{version(8, "A"), version(5, "A")},
			notes:    comment,
			want:     []string{"new_commits"}, at: 5,
		},
	}
}

func TestReviewRebaseOnly(t *testing.T) {
	for _, tt := range rebaseTests() {
		s := snap("bob", tt.notes...)
		s.Versions = tt.versions
		if tt.edit != nil {
			tt.edit(s)
		}
		got := reviewItemWith(s, me, yes)
		if !slices.Equal(got.Reasons, tt.want) {
			t.Errorf("%s: reasons = %v, want %v", tt.name, got.Reasons, tt.want)
		}
		if slices.Contains(tt.want, "new_commits") && !got.At.Equal(at(tt.at)) {
			t.Errorf("%s: at = %v, want %v", tt.name, got.At, at(tt.at))
		}
	}
}

// On a GitLab whose patch IDs are not trusted, every push after my last
// action is new commits, dated from the newest push: what main did.
func TestReviewPatchIDsNotTrusted(t *testing.T) {
	for _, tt := range rebaseTests() {
		s := snap("bob", tt.notes...)
		s.Versions = tt.versions
		if tt.edit != nil {
			tt.edit(s)
		}
		approved := len(s.Approvals.ApprovedBy) > 0
		got := reviewItem(s, me)
		if has := slices.Contains(got.Reasons, "new_commits"); has == approved {
			t.Errorf("%s: reasons = %v, approved by me: %v", tt.name, got.Reasons, approved)
		}
		if !approved && !slices.Contains(got.Reasons, "new_replies") && !got.At.Equal(tt.versions[0].CreatedAt) {
			t.Errorf("%s: at = %v, want the newest push, %v", tt.name, got.At, tt.versions[0].CreatedAt)
		}
	}
}

// The version of GitLab is only asked when the patch IDs would change
// the answer.
func TestReviewAsksWhetherTrustedOnlyWhenNeeded(t *testing.T) {
	comment := note(1, "me", 3, "nit", false)
	opened := version(2, "A")
	tests := []struct {
		name     string
		versions []gitlab.Version
		notes    []gitlab.Note
		edit     func(*mr.Snapshot)
		asked    int
	}{
		{name: "rebased", versions: []gitlab.Version{version(5, "A"), opened}, notes: []gitlab.Note{comment}, asked: 1},
		{name: "changed, then rebased", versions: []gitlab.Version{version(8, "B"), version(5, "B"), opened}, notes: []gitlab.Note{comment}, asked: 1},
		{name: "changed", versions: []gitlab.Version{version(5, "B"), opened}, notes: []gitlab.Note{comment}},
		{name: "no patch ID", versions: []gitlab.Version{version(5, ""), version(2, "")}, notes: []gitlab.Note{comment}},
		{name: "nothing pushed since my comment", versions: []gitlab.Version{opened}, notes: []gitlab.Note{comment}},
		{name: "never reviewed", versions: []gitlab.Version{version(5, "A"), opened}},
		{name: "a push landed while fetching", versions: []gitlab.Version{version(5, "A"), opened}, notes: []gitlab.Note{comment}, edit: func(s *mr.Snapshot) { s.Unsettled = true }},
		{name: "approved by me", versions: []gitlab.Version{version(5, "A"), opened}, notes: []gitlab.Note{comment}, edit: func(s *mr.Snapshot) { s.Approvals = approvedBy("me") }},
	}
	for _, tt := range tests {
		s := snap("bob", tt.notes...)
		s.Versions = tt.versions
		if tt.edit != nil {
			tt.edit(s)
		}
		asked := 0
		reviewItemWith(s, me, func() bool { asked++; return true })
		if asked != tt.asked {
			t.Errorf("%s: asked %d times, want %d", tt.name, asked, tt.asked)
		}
	}
}

// fakeGitLab serves one merge request that I review (or several, all
// alike), with what Collect reads of it, and counts or records the other
// requests.
type fakeGitLab struct {
	// mrs is how many merge requests I review: one when 0.
	mrs int
	// versions is the JSON GitLab answers for the diff versions.
	versions string
	notes    []gitlab.Note
	// todos is the JSON of my pending to-do items.
	todos     string
	updatedAt time.Time

	mu sync.Mutex
	// release is the "version" of GET /metadata; releaseStatus, when set,
	// is answered instead.
	release       string
	releaseStatus int
	// releaseHangs makes GET /metadata never answer.
	releaseHangs  bool
	releaseCalls  int
	versionsQuery string
	others        []string
}

const (
	mrsURL = "https://gitlab.example/g/p/-/merge_requests/"
	mrURL  = mrsURL + "1"
)

func (f *fakeGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var versions []gitlab.Version
	_ = json.Unmarshal([]byte(f.versions), &versions)
	mergeRequest := func(iid int) gitlab.MergeRequest {
		m := gitlab.MergeRequest{
			ID: 10 + iid, IID: iid, ProjectID: 1, State: "opened", Author: gitlab.User{Username: "bob"},
			SHA: versions[0].HeadCommitSHA, WebURL: fmt.Sprintf("%s%d", mrsURL, iid), CreatedAt: at(0), UpdatedAt: f.updatedAt,
		}
		m.References.Full = fmt.Sprintf("g/p!%d", iid)
		return m
	}
	var out any
	switch path := r.URL.Path; {
	case path == "/api/v4/merge_requests":
		list := []gitlab.MergeRequest{}
		for iid := 1; iid <= max(f.mrs, 1); iid++ {
			list = append(list, mergeRequest(iid))
		}
		out = list
	case strings.HasSuffix(path, "/merge_requests/1"):
		out = mergeRequest(1)
	case strings.HasSuffix(path, "/merge_requests/2"):
		out = mergeRequest(2)
	case strings.HasSuffix(path, "/approvals"):
		out = gitlab.Approvals{}
	case strings.HasSuffix(path, "/discussions"):
		ds := []gitlab.Discussion{}
		for _, n := range f.notes {
			ds = append(ds, gitlab.Discussion{ID: "d", Notes: []gitlab.Note{n}})
		}
		out = ds
	case strings.HasSuffix(path, "/versions"):
		f.versionsQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(f.versions))
		return
	case path == "/api/v4/todos":
		_, _ = w.Write([]byte(f.todos))
		return
	case path == "/api/v4/metadata":
		f.releaseCalls++
		if f.releaseHangs {
			// Until the client gives up; the other requests go on.
			f.mu.Unlock()
			<-r.Context().Done()
			f.mu.Lock()
			return
		}
		if f.releaseStatus != 0 {
			http.Error(w, `{"message":"failed"}`, f.releaseStatus)
			return
		}
		out = map[string]string{"version": f.release, "revision": "ceb07b24cb0"}
	default:
		f.others = append(f.others, r.URL.String())
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fakeGitLab) set(release string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.release, f.releaseStatus = release, status
}

// check fails the test when GitLab's version was not asked calls times,
// or when anything but what the inbox reads was asked: it never compares
// commits.
func (f *fakeGitLab) check(t *testing.T, calls int) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.releaseCalls != calls {
		t.Errorf("GET /metadata asked %d times, want %d", f.releaseCalls, calls)
	}
	if len(f.others) != 0 {
		t.Errorf("unexpected requests: %v", f.others)
	}
}

// hosts numbers the fake hosts: what a host answered to GET /metadata is
// remembered for as long as the process lives.
var hosts atomic.Int64

// client starts f as a host no test has seen.
func (f *fakeGitLab) client(t *testing.T) *gitlab.Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := gitlab.New(config.Host{Name: fmt.Sprintf("fake-%d", hosts.Add(1)), Token: "x"})
	c.BaseURL = srv.URL
	return c
}

// collect runs Collect and returns the only item.
func collect(t *testing.T, c *gitlab.Client, roles Roles) Item {
	t.Helper()
	res, err := Collect(context.Background(), c, Options{Host: c.Host.Name, People: me, Roles: roles})
	if err != nil || len(res.Warnings) != 0 || len(res.Items) != 1 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	return res.Items[0]
}

const (
	rebased = `[
		{"id": 2, "head_commit_sha": "head2", "base_commit_sha": "base2", "created_at": "2026-11-26T10:05:00Z", "patch_id_sha": "d504412d5b6e6739647e752aff8e468dde093f2f"},
		{"id": 1, "head_commit_sha": "head1", "base_commit_sha": "base1", "created_at": "2026-11-26T10:02:00Z", "patch_id_sha": "d504412d5b6e6739647e752aff8e468dde093f2f"}
	]`
	changed = `[
		{"id": 2, "head_commit_sha": "head2", "base_commit_sha": "base2", "created_at": "2026-11-26T10:05:00Z", "patch_id_sha": "72c30d1f0115fc1d2bb0b29b24dc2982cbcdfd32"},
		{"id": 1, "head_commit_sha": "head1", "base_commit_sha": "base1", "created_at": "2026-11-26T10:02:00Z", "patch_id_sha": "d504412d5b6e6739647e752aff8e468dde093f2f"}
	]`
	noPatchID = `[
		{"id": 2, "head_commit_sha": "head2", "base_commit_sha": "base2", "created_at": "2026-11-26T10:05:00Z", "patch_id_sha": null},
		{"id": 1, "head_commit_sha": "head1", "base_commit_sha": "base1", "created_at": "2026-11-26T10:02:00Z"}
	]`
)

// Collect tells a rebase from a change with the patch_id_sha of the diff
// versions it already fetches, on a GitLab recent enough to trust them.
func TestCollectLeavesRebasesOut(t *testing.T) {
	tests := []struct {
		name     string
		release  string
		versions string
		want     []string
		// asked is how many times GitLab's version is asked.
		asked int
	}{
		{"rebased, 18.2", "18.2.0", rebased, nil, 1},
		{"rebased, 18.5.1-ee", "18.5.1-ee", rebased, nil, 1},
		{"rebased, 18.10.0-pre", "18.10.0-pre", rebased, nil, 1},
		{"rebased, 19.0", "19.0.2", rebased, nil, 1},
		{"rebased, 18.1: patch IDs not trusted", "18.1.6-ee", rebased, []string{"new_commits"}, 1},
		{"rebased, 17.11: patch IDs not trusted", "17.11.4", rebased, []string{"new_commits"}, 1},
		{"rebased, a version that cannot be read", "main", rebased, []string{"new_commits"}, 1},
		{"rebased, no version", "", rebased, []string{"new_commits"}, 1},
		{"changed: the version is not asked", "18.5.1-ee", changed, []string{"new_commits"}, 0},
		{"no patch ID: the version is not asked", "18.5.1-ee", noPatchID, []string{"new_commits"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeGitLab{versions: tt.versions, release: tt.release, notes: []gitlab.Note{note(1, "me", 3, "nit", false)}}
			if got := collect(t, f.client(t), Roles{Review: true}).Reasons; !slices.Equal(got, tt.want) {
				t.Errorf("reasons = %v, want %v", got, tt.want)
			}
			f.check(t, tt.asked)
			if !strings.Contains(f.versionsQuery, "per_page=100") {
				t.Errorf("versions asked with %q, want per_page=100", f.versionsQuery)
			}
		})
	}
}

// inbox wait runs Collect every two minutes: GitLab's version is asked
// once per process, and again only while GitLab does not answer.
func TestCollectAsksTheVersionOnce(t *testing.T) {
	f := &fakeGitLab{versions: rebased, notes: []gitlab.Note{note(1, "me", 3, "nit", false)}}
	c := f.client(t)

	f.set("", 500)
	if got := collect(t, c, Roles{Review: true}); !slices.Equal(got.Reasons, []string{"new_commits"}) || !got.At.Equal(at(5)) {
		t.Errorf("GitLab failing: reasons = %v at %v, want new_commits at :05", got.Reasons, got.At)
	}
	f.check(t, 1)

	f.set("18.5.1-ee", 0)
	for range 3 {
		if got := collect(t, c, Roles{Review: true}).Reasons; len(got) != 0 {
			t.Errorf("GitLab back: reasons = %v, want none", got)
		}
	}
	f.check(t, 2)

	// Two merge requests that need the answer: still asked once.
	f = &fakeGitLab{mrs: 2, versions: rebased, release: "18.5.1-ee", notes: []gitlab.Note{note(1, "me", 3, "nit", false)}}
	c = f.client(t)
	res, err := Collect(context.Background(), c, Options{Host: c.Host.Name, People: me, Roles: Roles{Review: true}})
	if err != nil || len(res.Warnings) != 0 || len(res.Items) != 2 || res.Items[0].Waiting() || res.Items[1].Waiting() {
		t.Fatalf("two merge requests: res = %+v, err = %v", res, err)
	}
	f.check(t, 1)

	// A version that cannot be read is an answer too: not asked again.
	f = &fakeGitLab{versions: rebased, release: "main", notes: []gitlab.Note{note(1, "me", 3, "nit", false)}}
	c = f.client(t)
	for range 3 {
		if got := collect(t, c, Roles{Review: true}).Reasons; !slices.Equal(got, []string{"new_commits"}) {
			t.Errorf("unreadable version: reasons = %v, want new_commits", got)
		}
	}
	f.check(t, 1)
}

// A GitLab that does not say its version must not hold the inbox until
// the command times out: after versionTimeout the patch IDs are not
// trusted, for this poll only.
func TestCollectDoesNotWaitForTheVersion(t *testing.T) {
	old := versionTimeout
	versionTimeout = 50 * time.Millisecond
	t.Cleanup(func() { versionTimeout = old })

	f := &fakeGitLab{versions: rebased, release: "18.5.1-ee", todos: "[]", releaseHangs: true, notes: []gitlab.Note{note(1, "me", 3, "nit", false)}}
	c := f.client(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := Collect(ctx, c, Options{Host: c.Host.Name, People: me, Roles: Roles{Review: true, Todo: true}})
	if err != nil || len(res.Warnings) != 0 || len(res.Items) != 1 || !slices.Equal(res.Items[0].Reasons, []string{"new_commits"}) {
		t.Fatalf("GitLab not answering: res = %+v, err = %v", res, err)
	}
	f.check(t, 1)

	// Not remembered: asked again at the next poll.
	f.mu.Lock()
	f.releaseHangs = false
	f.mu.Unlock()
	if got := collect(t, c, Roles{Review: true, Todo: true}).Reasons; len(got) != 0 {
		t.Errorf("GitLab back: reasons = %v, want none", got)
	}
	f.check(t, 2)
}

// A merge request without a reason of its own is dated from its to-do
// item, not from its last update: rebases and edits must not make inbox
// wait fire again for the same to-do item.
func TestCollectDatesFromTheTodo(t *testing.T) {
	todo := `[{"id": 7, "action_name": "marked", "target_type": "MergeRequest", "target_url": "` + mrURL + `",
		"target": {"iid": 1, "web_url": "` + mrURL + `"}, "created_at": "2026-11-26T10:04:00Z"}]`
	f := &fakeGitLab{versions: rebased, release: "18.5.1-ee", todos: todo, updatedAt: at(9), notes: []gitlab.Note{note(1, "me", 3, "nit", false)}}
	got := collect(t, f.client(t), Roles{Review: true, Todo: true})
	if !slices.Equal(got.Reasons, []string{"todo:marked"}) || !got.At.Equal(at(4)) {
		t.Errorf("reasons = %v at %v, want todo:marked at :04", got.Reasons, got.At)
	}

	// With a reason of its own, the newest of the two dates is kept.
	f = &fakeGitLab{versions: changed, release: "18.5.1-ee", todos: todo, updatedAt: at(9), notes: []gitlab.Note{note(1, "me", 3, "nit", false)}}
	got = collect(t, f.client(t), Roles{Review: true, Todo: true})
	if !slices.Equal(got.Reasons, []string{"new_commits", "todo:marked"}) || !got.At.Equal(at(5)) {
		t.Errorf("reasons = %v at %v, want new_commits and todo:marked at :05", got.Reasons, got.At)
	}
}
