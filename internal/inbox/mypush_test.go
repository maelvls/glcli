package inbox

import (
	"slices"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

// pushBy is the system note GitLab writes under who pushed, right after
// it created the diff version of the push.
func pushBy(id int, user string, min int) gitlab.Note {
	return note(id, user, min, "added 1 commit\n\n<ul><li>1a2b3c4d - Fix</li></ul>", true)
}

// A push of mine on a merge request I review (the Rebase button, a
// conflict resolved in the UI, a suggestion applied, a commit of mine) is
// not a review: it neither answers a review request nor makes the changes
// of someone else seen. It is not something for me to review either.
//
// The merge request was opened at :00 with a first version at :02 (patch
// A).
func TestMyPushIsNotAReview(t *testing.T) {
	comment := note(1, "me", 3, "nit", false)
	opened := version(2, "A")
	tests := []struct {
		name     string
		versions []gitlab.Version
		notes    []gitlab.Note
		// want is what a GitLab with trusted patch IDs gives, wantOld what
		// an older one gives; at and atOld date the first reason.
		want, wantOld []string
		at, atOld     int
		// myLast still counts my pushes: to-do items older are stale.
		myLast int
	}{
		// Never reviewed.
		{
			name:     "never reviewed, I rebased: dated from the creation",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    []gitlab.Note{pushBy(1, "me", 5)},
			want:     []string{"review_requested"}, at: 0,
			wantOld: []string{"review_requested"}, atOld: 0,
			myLast: 5,
		},
		{
			name:     "never reviewed, I pushed a commit",
			versions: []gitlab.Version{version(5, "B"), opened},
			notes:    []gitlab.Note{pushBy(1, "me", 5)},
			want:     []string{"review_requested"}, at: 0,
			wantOld: []string{"review_requested"}, atOld: 0,
			myLast: 5,
		},
		{
			name:     "my review was requested, I rebased: dated from the request",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    []gitlab.Note{note(1, "bob", 1, "requested review from @me", true), pushBy(2, "me", 5)},
			want:     []string{"review_requested"}, at: 1,
			wantOld: []string{"review_requested"}, atOld: 1,
			myLast: 5,
		},
		{
			name:     "never reviewed, I rebased, then it changed: still a review request",
			versions: []gitlab.Version{version(8, "B"), version(5, "A"), opened},
			notes:    []gitlab.Note{pushBy(1, "me", 5), pushBy(2, "bob", 8)},
			want:     []string{"review_requested"}, at: 0,
			wantOld: []string{"review_requested"}, atOld: 0,
			myLast: 5,
		},
		{
			name:     "reviewed, my review was requested again, I rebased: still requested",
			versions: []gitlab.Version{version(6, "A"), opened},
			notes:    []gitlab.Note{comment, note(2, "bob", 5, "requested review from @me", true), pushBy(3, "me", 6)},
			want:     []string{"review_requested"}, at: 5,
			wantOld: []string{"review_requested"}, atOld: 5,
			myLast: 6,
		},

		// Reviewed, then only pushes of mine: nothing to review.
		{
			name:     "reviewed, then I rebased",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "me", 5)},
			myLast:   5,
		},
		{
			name:     "reviewed, then I pushed a commit",
			versions: []gitlab.Version{version(5, "C"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "me", 5)},
			myLast:   5,
		},
		{
			name:     "reviewed, then I pushed twice",
			versions: []gitlab.Version{version(6, "D"), version(5, "C"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "me", 5), pushBy(3, "me", 6)},
			myLast:   6,
		},
		{
			name:     "approved, then my own rebase: nothing, even with my approval gone",
			versions: []gitlab.Version{version(5, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, approvedNote, true), pushBy(2, "me", 5)},
			myLast:   5,
		},

		// A change of someone else, then a push of mine: the change is
		// still not reviewed, and dated from the push that is not mine.
		{
			name:     "reviewed, it changed, then I rebased",
			versions: []gitlab.Version{version(6, "B"), version(5, "B"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 5), pushBy(3, "me", 6)},
			want:     []string{"new_commits"}, at: 5,
			wantOld: []string{"new_commits"}, atOld: 5,
			myLast: 6,
		},
		{
			name:     "reviewed, it changed, then I pushed a commit on top",
			versions: []gitlab.Version{version(6, "C"), version(5, "B"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 5), pushBy(3, "me", 6)},
			want:     []string{"new_commits"}, at: 5,
			wantOld: []string{"new_commits"}, atOld: 5,
			myLast: 6,
		},
		{
			name:     "reviewed, it changed, then I pushed a commit and rebased: still dated from the change",
			versions: []gitlab.Version{version(10, "C"), version(9, "C"), version(5, "B"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 5), pushBy(3, "me", 9), pushBy(4, "me", 10)},
			want:     []string{"new_commits"}, at: 5,
			wantOld: []string{"new_commits"}, atOld: 5,
			myLast: 10,
		},
		{
			name:     "reviewed, it changed, I rebased, it was rebased again: dated from the change, or from the last push that is not mine",
			versions: []gitlab.Version{version(8, "B"), version(6, "B"), version(5, "B"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 5), pushBy(3, "me", 6), pushBy(4, "bob", 8)},
			want:     []string{"new_commits"}, at: 5,
			wantOld: []string{"new_commits"}, atOld: 8,
			myLast: 6,
		},
		{
			name:     "reviewed, it changed, then I pushed it back to what I saw",
			versions: []gitlab.Version{version(6, "A"), version(5, "B"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 5), pushBy(3, "me", 6)},
			wantOld:  []string{"new_commits"}, atOld: 5,
			myLast: 6,
		},
		{
			name:     "reviewed, it changed, I rebased, then I commented: seen",
			versions: []gitlab.Version{version(6, "B"), version(5, "B"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 5), pushBy(3, "me", 6), note(4, "me", 7, "fine", false)},
			myLast:   7,
		},

		// A push of mine, then a push of someone else.
		{
			name:     "reviewed, I pushed a commit, then it changed",
			versions: []gitlab.Version{version(8, "D"), version(5, "C"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "me", 5), pushBy(3, "bob", 8)},
			want:     []string{"new_commits"}, at: 8,
			wantOld: []string{"new_commits"}, atOld: 8,
			myLast: 5,
		},
		{
			name:     "reviewed, I pushed a commit, then it was rebased: what I pushed is what I saw",
			versions: []gitlab.Version{version(8, "C"), version(5, "C"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "me", 5), pushBy(3, "bob", 8)},
			wantOld:  []string{"new_commits"}, atOld: 8,
			myLast: 5,
		},

		// Whose push it is.
		{
			name:     "a push with a note of someone else is not mine",
			versions: []gitlab.Version{version(5, "B"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 5)},
			want:     []string{"new_commits"}, at: 5,
			wantOld: []string{"new_commits"}, atOld: 5,
			myLast: 3,
		},
		{
			name:     "a push without a note is not mine",
			versions: []gitlab.Version{version(5, "B"), opened},
			notes:    []gitlab.Note{comment},
			want:     []string{"new_commits"}, at: 5,
			wantOld: []string{"new_commits"}, atOld: 5,
			myLast: 3,
		},
		{
			name:     "my note comes after the version of my push, and before the next push",
			versions: []gitlab.Version{version(9, "D"), version(5, "C"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "me", 7), pushBy(3, "bob", 9)},
			want:     []string{"new_commits"}, at: 9,
			wantOld: []string{"new_commits"}, atOld: 9,
			myLast: 7,
		},
		{
			name:     "a push of mine before my review is nothing new",
			versions: []gitlab.Version{version(5, "C"), opened},
			notes:    []gitlab.Note{pushBy(1, "me", 5), note(2, "me", 6, "nit", false)},
			myLast:   6,
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
			if len(mode.want) != 0 && !got.At.Equal(at(mode.at)) {
				t.Errorf("%s, %s: at = %v, want %v", tt.name, mode.name, got.At, at(mode.at))
			}
			if !got.myLast.Equal(at(tt.myLast)) {
				t.Errorf("%s, %s: myLast = %v, want %v", tt.name, mode.name, got.myLast, at(tt.myLast))
			}
		}
	}
}

// Kept as before: a push of mine after a reply clears the reply, as on my
// own merge requests, where the push is the answer.
func TestMyPushStillClearsReplies(t *testing.T) {
	comment := note(1, "me", 3, "nit", false)
	s := snap("bob", comment, note(2, "alice", 4, "@me done", false), pushBy(3, "me", 5))
	s.Versions = []gitlab.Version{version(5, "A"), version(2, "A")}
	if got := reviewItemWith(s, me, yes); len(got.Reasons) != 0 {
		t.Errorf("a reply before my push: reasons = %v, want none", got.Reasons)
	}
	s = snap("bob", comment, pushBy(2, "me", 5), note(3, "alice", 6, "@me done", false))
	s.Versions = []gitlab.Version{version(5, "A"), version(2, "A")}
	if got := reviewItemWith(s, me, yes); !slices.Equal(got.Reasons, []string{"new_replies"}) || !got.At.Equal(at(6)) {
		t.Errorf("a reply after my push: reasons = %v at %v, want new_replies at :06", got.Reasons, got.At)
	}
}

// On my own merge requests my pushes count as before, whoever else
// pushed in between: a push of mine answers the comments before it.
func TestMyPushStillAnswersCommentsOnMyMergeRequest(t *testing.T) {
	s := snap("me", note(1, "alice", 3, "looks wrong", false), pushBy(2, "bob", 4), pushBy(3, "me", 5))
	s.Versions = []gitlab.Version{version(5, "C"), version(4, "B"), version(2, "A")}
	if got := authoredItem(s, me); len(got.Reasons) != 0 || !got.myLast.Equal(at(5)) {
		t.Errorf("a comment before my push: reasons = %v, myLast = %v, want none and %v", got.Reasons, got.myLast, at(5))
	}
	s = snap("me", pushBy(1, "me", 5), note(2, "alice", 6, "looks wrong", false))
	s.Versions = []gitlab.Version{version(5, "C"), version(2, "A")}
	if got := authoredItem(s, me); !slices.Equal(got.Reasons, []string{"new_comments"}) {
		t.Errorf("a comment after my push: reasons = %v, want [new_comments]", got.Reasons)
	}
}

// Kept as before: a to-do item older than a push of mine is stale. The
// review I owe is not: it does not come from the to-do item.
func TestCollectMyPushKeepsTheReviewRequest(t *testing.T) {
	todo := func(minute string) string {
		return `[{"id": 7, "action_name": "review_requested", "target_type": "MergeRequest", "target_url": "` + mrURL + `",
			"target": {"iid": 1, "web_url": "` + mrURL + `"}, "created_at": "2026-11-26T10:` + minute + `:00Z"}]`
	}
	f := &fakeGitLab{versions: rebased, release: "18.5.1-ee", todos: todo("04"), notes: []gitlab.Note{pushBy(1, "me", 5)}}
	got := collect(t, f.client(t), Roles{Review: true, Todo: true})
	if !slices.Equal(got.Reasons, []string{"review_requested"}) || !got.At.Equal(at(0)) || !slices.Equal(got.TodoIDs, []int{7}) {
		t.Errorf("to-do item before my push: reasons = %v at %v, to-do items = %v, want review_requested at :00 and the to-do item 7", got.Reasons, got.At, got.TodoIDs)
	}

	f = &fakeGitLab{versions: rebased, release: "18.5.1-ee", todos: todo("07"), notes: []gitlab.Note{pushBy(1, "me", 5)}}
	got = collect(t, f.client(t), Roles{Review: true, Todo: true})
	if !slices.Equal(got.Reasons, []string{"review_requested", "todo:review_requested"}) || !got.At.Equal(at(7)) {
		t.Errorf("to-do item after my push: reasons = %v at %v, want review_requested and todo:review_requested at :07", got.Reasons, got.At)
	}
}
