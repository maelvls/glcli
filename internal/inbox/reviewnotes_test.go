package inbox

import (
	"slices"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

const (
	requestedChangesNote = "requested changes"
	reviewedNote         = "left review comments"
)

// A review submitted without a comment leaves only a system note under
// my name: "requested changes", "left review comments", or "unapproved
// this merge request" when I take my approval back. I acted: what was
// pushed before is not new to me.
func TestMyReviewNotesAreMyAction(t *testing.T) {
	approval := note(1, "me", 3, approvedNote, true)
	comment := note(1, "me", 3, "nit", false)
	opened := version(2, "A")
	tests := []struct {
		name     string
		versions []gitlab.Version
		notes    []gitlab.Note
		// want is what a GitLab with trusted patch IDs gives, wantOld what
		// an older one gives; at and atOld date new_commits.
		want, wantOld []string
		at, atOld     int
		myLast        int
	}{
		{
			name:     "approved, changed, I requested changes without a comment, then rebased",
			versions: []gitlab.Version{version(8, "B"), version(5, "B"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 6, requestedChangesNote, true)},
			wantOld:  []string{"new_commits"}, atOld: 8,
			myLast: 6,
		},
		{
			name:     "approved, changed, I took my approval back, then rebased",
			versions: []gitlab.Version{version(8, "B"), version(5, "B"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 6, unapprovedNote, true)},
			wantOld:  []string{"new_commits"}, atOld: 8,
			myLast: 6,
		},
		{
			name:     "changed, then I requested changes: nothing is new",
			versions: []gitlab.Version{version(5, "B"), opened},
			notes:    []gitlab.Note{comment, note(2, "me", 6, requestedChangesNote, true)},
			myLast:   6,
		},
		{
			name:     "changed, then I submitted a review without a comment: nothing is new",
			versions: []gitlab.Version{version(5, "B"), opened},
			notes:    []gitlab.Note{comment, note(2, "me", 6, reviewedNote, true)},
			myLast:   6,
		},
		{
			name:     "changed, then I took my approval back: nothing is new",
			versions: []gitlab.Version{version(5, "B"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 6, unapprovedNote, true)},
			myLast:   6,
		},
		{
			name:     "reviewer since the creation, I only requested changes: reviewed",
			versions: []gitlab.Version{opened},
			notes:    []gitlab.Note{note(1, "me", 3, requestedChangesNote, true)},
			myLast:   3,
		},
		{
			name:     "reviewer since the creation, I only submitted a review without a comment: reviewed",
			versions: []gitlab.Version{opened},
			notes:    []gitlab.Note{note(1, "me", 3, reviewedNote, true)},
			myLast:   3,
		},
		{
			name:     "changed after I requested changes",
			versions: []gitlab.Version{version(8, "B"), opened},
			notes:    []gitlab.Note{note(1, "me", 6, requestedChangesNote, true)},
			want:     []string{"new_commits"}, at: 8,
			wantOld: []string{"new_commits"}, atOld: 8,
			myLast: 6,
		},
		{
			name:     "changed at the instant I requested changes: seen",
			versions: []gitlab.Version{version(6, "B"), opened},
			notes:    []gitlab.Note{comment, note(2, "me", 6, requestedChangesNote, true)},
			myLast:   6,
		},
		{
			name:     "the changes someone else requested are not my action",
			versions: []gitlab.Version{version(5, "B"), opened},
			notes:    []gitlab.Note{comment, note(2, "alice", 6, requestedChangesNote, true)},
			want:     []string{"new_commits"}, at: 5,
			wantOld: []string{"new_commits"}, atOld: 5,
			myLast: 3,
		},
		{
			name:     "asking someone else for a review is not reviewing",
			versions: []gitlab.Version{opened},
			notes:    []gitlab.Note{note(1, "me", 3, "requested review from @alice", true)},
			want:     []string{"review_requested"},
			wantOld:  []string{"review_requested"},
		},
	}
	for _, tt := range tests {
		for _, mode := range []struct {
			name    string
			trusted func() bool
			want    []string
			at      int
		}{
			{"patch IDs trusted", yes, tt.want, tt.at},
			{"patch IDs not trusted", func() bool { return false }, tt.wantOld, tt.atOld},
		} {
			s := snap("bob", tt.notes...)
			s.Versions = tt.versions
			got := reviewItemWith(s, me, mode.trusted)
			if !slices.Equal(got.Reasons, mode.want) {
				t.Errorf("%s, %s: reasons = %v, want %v", tt.name, mode.name, got.Reasons, mode.want)
			}
			if slices.Contains(mode.want, "new_commits") && !got.At.Equal(at(mode.at)) {
				t.Errorf("%s, %s: at = %v, want %v", tt.name, mode.name, got.At, at(mode.at))
			}
			if tt.myLast == 0 && !got.myLast.IsZero() || tt.myLast != 0 && !got.myLast.Equal(at(tt.myLast)) {
				t.Errorf("%s, %s: myLast = %v, want minute %d", tt.name, mode.name, got.myLast, tt.myLast)
			}
		}
	}
}

// On my own merge request too: taking my approval back after a comment is
// acting on it, like approving already was.
func TestMyReviewNotesAnswerCommentsOnMyMergeRequest(t *testing.T) {
	for _, body := range []string{approvedNote, unapprovedNote, requestedChangesNote, reviewedNote} {
		got := authoredItem(snap("me", note(1, "alice", 3, "looks wrong", false), note(2, "me", 4, body, true)), me)
		if len(got.Reasons) != 0 || !got.myLast.Equal(at(4)) {
			t.Errorf("%q after a comment: reasons = %v, myLast = %v, want none and %v", body, got.Reasons, got.myLast, at(4))
		}
		got = authoredItem(snap("me", note(1, "me", 3, body, true), note(2, "alice", 4, "looks wrong", false)), me)
		if !slices.Equal(got.Reasons, []string{"new_comments"}) {
			t.Errorf("a comment after %q: reasons = %v, want [new_comments]", body, got.Reasons)
		}
	}
}

// A to-do item older than my "requested changes" is stale, like one older
// than a comment of mine.
func TestCollectTodoOlderThanMyRequestedChangesIsStale(t *testing.T) {
	todo := `[{"id": 7, "action_name": "mentioned", "target_type": "MergeRequest", "target_url": "` + mrURL + `",
		"target": {"iid": 1, "web_url": "` + mrURL + `"}, "created_at": "2026-11-26T10:04:00Z"}]`
	f := &fakeGitLab{versions: rebased, release: "18.5.1-ee", todos: todo, notes: []gitlab.Note{note(1, "me", 6, requestedChangesNote, true)}}
	got := collect(t, f.client(t), Roles{Review: true, Todo: true})
	if len(got.Reasons) != 0 || !slices.Equal(got.TodoIDs, []int{7}) {
		t.Errorf("reasons = %v, to-do items = %v, want no reason and the to-do item 7", got.Reasons, got.TodoIDs)
	}

	// One that came after still waits on me.
	f = &fakeGitLab{versions: rebased, release: "18.5.1-ee", todos: todo, notes: []gitlab.Note{note(1, "me", 3, requestedChangesNote, true)}}
	got = collect(t, f.client(t), Roles{Review: true, Todo: true})
	if !slices.Equal(got.Reasons, []string{"todo:mentioned"}) || !got.At.Equal(at(4)) {
		t.Errorf("reasons = %v at %v, want todo:mentioned at :04", got.Reasons, got.At)
	}
}
