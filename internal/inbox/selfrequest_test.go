package inbox

import (
	"slices"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

// When I approve, request changes or submit a review without being a
// reviewer, GitLab makes me one and writes "requested review from @me"
// under my own name, before the note of the review; the "approved" note
// comes later, from a worker. That request is not one.
func TestMyOwnReviewRequestIsNotARequest(t *testing.T) {
	self := func(id, min int) gitlab.Note { return note(id, "me", min, "requested review from @me", true) }
	tests := []struct {
		name     string
		notes    []gitlab.Note
		approved bool
		want     []string
		at       int
	}{
		{
			name:     "I approved without being a reviewer, the approved note is not written yet",
			notes:    []gitlab.Note{self(1, 5)},
			approved: true,
		},
		{
			name:     "I commented, then approved without being a reviewer, the approved note is not written yet",
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false), self(2, 5)},
			approved: true,
		},
		{
			name:     "I approved without being a reviewer, the approved note is written",
			notes:    []gitlab.Note{self(1, 5), note(2, "me", 6, approvedNote, true)},
			approved: true,
		},
		{
			name:  "I requested changes without being a reviewer",
			notes: []gitlab.Note{self(1, 5), note(2, "me", 5, requestedChangesNote, true)},
		},
		{
			name:  "I reviewed, then made myself a reviewer: nothing asks me",
			notes: []gitlab.Note{note(1, "me", 3, "nit", false), self(2, 5)},
		},
		{
			name:  "never reviewed, I made myself a reviewer: a review I owe since the creation",
			notes: []gitlab.Note{self(1, 5)},
			want:  []string{"review_requested"}, at: 0,
		},
		{
			name:  "someone else requested my review after mine",
			notes: []gitlab.Note{note(1, "me", 3, "nit", false), note(2, "bob", 5, "requested review from @me", true)},
			want:  []string{"review_requested"}, at: 5,
		},
		{
			name:  "someone else requested my review and others' in a note that names me",
			notes: []gitlab.Note{note(1, "me", 3, "nit", false), note(2, "bob", 5, "requested review from @alice and @me", true)},
			want:  []string{"review_requested"}, at: 5,
		},
	}
	for _, tt := range tests {
		s := snap("bob", tt.notes...)
		if tt.approved {
			s.Approvals = approvedBy("me")
		}
		got := reviewItem(s, me)
		if !slices.Equal(got.Reasons, tt.want) {
			t.Errorf("%s: reasons = %v, want %v", tt.name, got.Reasons, tt.want)
		}
		if tt.want != nil && !got.At.Equal(at(tt.at)) {
			t.Errorf("%s: at = %v, want %v", tt.name, got.At, at(tt.at))
		}
	}
}
