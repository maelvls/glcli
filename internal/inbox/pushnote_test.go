package inbox

import (
	"slices"
	"testing"
)

// Logging time (/spend, /estimate) writes a system note that starts with
// "added ", like a push, and so did a label on old merge requests: none
// of them is me acting on the merge request.
func TestOtherAddedNotesAreNotMyAction(t *testing.T) {
	// Reviewer since the creation: GitLab writes no "requested review
	// from @me" note, so only my own actions can clear the request.
	for _, body := range []string{
		"added 1h of time spent at 2026-11-27 16:00:00 UTC",
		"added time estimate of 1h",
		"added ~123 label",
		"added ~1 ~2 labels",
		`added ~"needs commit" label`,
	} {
		got := reviewItem(snap("bob", note(1, "me", 3, body, true)), me)
		if !slices.Equal(got.Reasons, []string{"review_requested"}) {
			t.Errorf("%q: reasons = %v, want [review_requested]", body, got.Reasons)
		}
		if !got.myLast.IsZero() {
			t.Errorf("%q: myLast = %v, want zero", body, got.myLast)
		}
	}
	// On my own merge request, time I log after a comment does not
	// answer the comment.
	got := authoredItem(snap("me", note(1, "alice", 1, "looks wrong", false), note(2, "me", 3, "added 1h of time spent at 2026-11-27 16:00:00 UTC", true)), me)
	if !slices.Equal(got.Reasons, []string{"new_comments"}) {
		t.Errorf("time spent after a comment: reasons = %v, want [new_comments]", got.Reasons)
	}
}

func TestPushIsMyAction(t *testing.T) {
	// A comment after my push still waits on me.
	got := authoredItem(snap("me", note(1, "me", 1, "added 1 commit", true), note(2, "alice", 3, "looks wrong", false)), me)
	if !slices.Equal(got.Reasons, []string{"new_comments"}) {
		t.Errorf("comment after my push: reasons = %v, want [new_comments]", got.Reasons)
	}
	// A comment before my push does not: the push answered it.
	for _, body := range []string{"added 1 commit", "added 3 commits\n\n<ul><li>1a2b3c4d - Fix</li></ul>"} {
		got = authoredItem(snap("me", note(1, "alice", 1, "looks wrong", false), note(2, "me", 3, body, true)), me)
		if len(got.Reasons) != 0 || !got.myLast.Equal(at(3)) {
			t.Errorf("%q: reasons = %v, myLast = %v, want none and %v", body, got.Reasons, got.myLast, at(3))
		}
	}
}
