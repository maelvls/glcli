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

// version is a diff version pushed at 10:min, with GitLab's patch ID.
func version(id, min int, patch string) gitlab.Version {
	head := "head" + string(rune('0'+id))
	return gitlab.Version{ID: id, CreatedAt: at(min), BaseCommitSHA: "base", HeadCommitSHA: head, PatchIDSHA: patch}
}

func approvedBy(user string) gitlab.Approvals {
	return gitlab.Approvals{ApprovedBy: []struct {
		User gitlab.User `json:"user"`
	}{{User: gitlab.User{Username: user}}}}
}

func TestReviewRebaseOnly(t *testing.T) {
	// In every case the merge request was opened at :02 with patch A, and
	// I commented at :03 unless the notes say otherwise.
	comment := []gitlab.Note{note(1, "me", 3, "nit", false)}
	approval := note(1, "me", 3, "approved this merge request", true)
	opened := version(1, 2, "A")
	tests := []struct {
		name string
		// versions are newest first, like GitLab lists them.
		versions []gitlab.Version
		notes    []gitlab.Note
		edit     func(*mr.Snapshot)
		want     []string
		// at is the minute new_commits is dated from.
		at int
	}{
		{
			name:     "rebased",
			versions: []gitlab.Version{version(2, 5, "A"), opened},
			notes:    comment,
		},
		{
			name:     "changed",
			versions: []gitlab.Version{version(2, 5, "B"), opened},
			notes:    comment,
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "rebased, then changed",
			versions: []gitlab.Version{version(3, 8, "B"), version(2, 5, "A"), opened},
			notes:    comment,
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "changed, then rebased: dated from the change",
			versions: []gitlab.Version{version(3, 8, "B"), version(2, 5, "B"), opened},
			notes:    comment,
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "changed, then rebased twice: still dated from the change",
			versions: []gitlab.Version{version(4, 9, "B"), version(3, 8, "B"), version(2, 5, "B"), opened},
			notes:    comment,
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "changed, then back to what I saw",
			versions: []gitlab.Version{version(3, 8, "A"), version(2, 5, "B"), opened},
			notes:    comment,
		},
		{
			name:     "I commented on the rebased version, then it changed",
			versions: []gitlab.Version{version(3, 8, "B"), version(2, 5, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 6, "nit", false)},
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "rebased, and a reply in my thread",
			versions: []gitlab.Version{version(2, 5, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false), note(2, "alice", 6, "@me done", false)},
			want:     []string{"new_replies"},
		},

		// My approval.
		{
			name:     "approved by me, still: nothing",
			versions: []gitlab.Version{version(2, 5, "B"), opened},
			notes:    []gitlab.Note{approval},
			edit:     func(s *mr.Snapshot) { s.Approvals = approvedBy("me") },
		},
		{
			name:     "a rebase made GitLab remove my approval",
			versions: []gitlab.Version{version(2, 5, "A"), opened},
			notes:    []gitlab.Note{approval},
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "my approval removed, then rebased again: dated from the first rebase",
			versions: []gitlab.Version{version(3, 8, "A"), version(2, 5, "A"), opened},
			notes:    []gitlab.Note{approval},
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "I took my approval back myself, then rebased",
			versions: []gitlab.Version{version(2, 5, "A"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 4, "unapproved this merge request", true)},
		},
		{
			name:     "I took my approval back, approved again, and a rebase removed it",
			versions: []gitlab.Version{version(2, 8, "A"), opened},
			notes: []gitlab.Note{
				approval,
				note(2, "me", 4, "unapproved this merge request", true),
				note(3, "me", 6, "approved this merge request", true),
			},
			want: []string{"new_commits"}, at: 8,
		},
		{
			name:     "the approval of someone else is not mine",
			versions: []gitlab.Version{version(2, 5, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false), note(2, "alice", 4, "approved this merge request", true)},
		},

		// Another target branch.
		{
			name:     "retargeted after my review",
			versions: []gitlab.Version{version(2, 5, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false), note(2, "bob", 5, "changed target branch from `main` to `release`", true)},
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "target branch deleted after my review",
			versions: []gitlab.Version{version(2, 5, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false), note(2, "bob", 5, "deleted the `feature` branch. This merge request now targets the `main` branch", true)},
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "retargeted before my review, then rebased",
			versions: []gitlab.Version{version(2, 5, "A"), opened},
			notes:    []gitlab.Note{note(1, "bob", 2, "changed target branch from `main` to `release`", true), note(2, "me", 3, "nit", false)},
		},

		// Doubt.
		{
			name:     "no patch ID on the newest version",
			versions: []gitlab.Version{version(2, 5, ""), opened},
			notes:    comment,
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "no patch ID on the version I saw",
			versions: []gitlab.Version{version(2, 5, "A"), version(1, 2, "")},
			notes:    comment,
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "no patch ID at all",
			versions: []gitlab.Version{version(2, 5, ""), version(1, 2, "")},
			notes:    comment,
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "a push landed while fetching",
			versions: []gitlab.Version{version(2, 5, "A"), opened},
			notes:    comment,
			edit:     func(s *mr.Snapshot) { s.Unsettled = true },
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "notes are missing",
			versions: []gitlab.Version{version(2, 5, "A"), opened},
			notes:    comment,
			edit:     func(s *mr.Snapshot) { s.DiscussionsTruncated = true },
			want:     []string{"new_commits"}, at: 5,
		},
		{
			name:     "my last action is older than every version fetched",
			versions: []gitlab.Version{version(3, 8, "A"), version(2, 5, "A")},
			notes:    comment,
			want:     []string{"new_commits"}, at: 5,
		},
	}
	for _, tt := range tests {
		s := snap("bob", tt.notes...)
		s.Versions = tt.versions
		if tt.edit != nil {
			tt.edit(s)
		}
		got := reviewItem(s, me)
		if !slices.Equal(got.Reasons, tt.want) {
			t.Errorf("%s: reasons = %v, want %v", tt.name, got.Reasons, tt.want)
		}
		if slices.Contains(tt.want, "new_commits") && !got.At.Equal(at(tt.at)) {
			t.Errorf("%s: at = %v, want %v", tt.name, got.At, at(tt.at))
		}
	}
}

// fakeGitLab serves one merge request that I review, with what Collect
// reads of it, and records the other requests.
type fakeGitLab struct {
	// versions is the JSON GitLab answers for the diff versions.
	versions string
	notes    []gitlab.Note

	mu     sync.Mutex
	others []string
}

func (f *fakeGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var versions []gitlab.Version
	_ = json.Unmarshal([]byte(f.versions), &versions)
	m := gitlab.MergeRequest{
		ID: 10, IID: 1, ProjectID: 1, State: "opened", Author: gitlab.User{Username: "bob"},
		SHA: versions[0].HeadCommitSHA, WebURL: "https://gitlab.example/g/p/-/merge_requests/1", CreatedAt: at(0),
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
		_, _ = w.Write([]byte(f.versions))
		return
	default:
		f.mu.Lock()
		f.others = append(f.others, r.URL.String())
		f.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

// Collect tells a rebase from a change with what it already fetches: the
// patch_id_sha of the diff versions. It asks GitLab nothing more, and
// never compares commits.
func TestCollectLeavesRebasesOut(t *testing.T) {
	tests := []struct {
		name     string
		versions string
		want     []string
	}{
		{"rebased", `[
			{"id": 2, "head_commit_sha": "head2", "base_commit_sha": "base2", "created_at": "2026-11-26T10:05:00Z", "patch_id_sha": "d504412d5b6e6739647e752aff8e468dde093f2f"},
			{"id": 1, "head_commit_sha": "head1", "base_commit_sha": "base1", "created_at": "2026-11-26T10:02:00Z", "patch_id_sha": "d504412d5b6e6739647e752aff8e468dde093f2f"}
		]`, nil},
		{"changed", `[
			{"id": 2, "head_commit_sha": "head2", "base_commit_sha": "base2", "created_at": "2026-11-26T10:05:00Z", "patch_id_sha": "72c30d1f0115fc1d2bb0b29b24dc2982cbcdfd32"},
			{"id": 1, "head_commit_sha": "head1", "base_commit_sha": "base1", "created_at": "2026-11-26T10:02:00Z", "patch_id_sha": "d504412d5b6e6739647e752aff8e468dde093f2f"}
		]`, []string{"new_commits"}},
		{"a GitLab that gives no patch ID", `[
			{"id": 2, "head_commit_sha": "head2", "base_commit_sha": "base2", "created_at": "2026-11-26T10:05:00Z", "patch_id_sha": null},
			{"id": 1, "head_commit_sha": "head1", "base_commit_sha": "base1", "created_at": "2026-11-26T10:02:00Z"}
		]`, []string{"new_commits"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeGitLab{versions: tt.versions, notes: []gitlab.Note{note(1, "me", 3, "nit", false)}}
			srv := httptest.NewServer(f)
			defer srv.Close()
			c := gitlab.New(config.Host{Name: "test", Token: "x"})
			c.BaseURL = srv.URL
			res, err := Collect(context.Background(), c, Options{Host: "test", People: me, Roles: Roles{Review: true}})
			if err != nil || len(res.Warnings) != 0 || len(res.Items) != 1 {
				t.Fatalf("res = %+v, err = %v", res, err)
			}
			if got := res.Items[0].Reasons; !slices.Equal(got, tt.want) {
				t.Errorf("reasons = %v, want %v", got, tt.want)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.others) != 0 {
				t.Errorf("unexpected requests: %v", f.others)
			}
		})
	}
}
