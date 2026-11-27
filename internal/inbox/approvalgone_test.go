package inbox

import (
	"slices"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

// Two leftovers of "is my approval gone, and do I know it" on a GitLab
// whose patch IDs are trusted. In every case I approved the first version
// (patch A, :02) at :03 and I am no longer among the approvers. What
// GitLab does to the approvals is modelled by hand, not observed.
func TestApprovalGone(t *testing.T) {
	approval := note(1, "me", 3, approvedNote, true)
	opened := version(2, "A")
	tests := []struct {
		name     string
		versions []gitlab.Version
		notes    []gitlab.Note
		want     []string
		at       int
	}{
		// A. Changed, then back to the patch I saw: the changes in between
		// may be what removed my approval, whatever I knew before.
		{
			name:     "a change that kept my approval, I commented, a change removed my approval",
			versions: []gitlab.Version{version(8, "C"), version(5, "B"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 6, "by the way", false)},
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "a change that kept my approval, I commented, a change removed my approval, then back to what I saw: still waits on me",
			versions: []gitlab.Version{version(10, "B"), version(8, "C"), version(5, "B"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 6, "by the way", false)},
			want:     []string{"new_commits"}, at: 10,
		},
		{
			name:     "added as a reviewer after my approval (it stays), I commented, a change removed my approval",
			versions: []gitlab.Version{version(8, "B"), opened},
			notes:    []gitlab.Note{approval, note(2, "bob", 4, "requested review from @me", true), note(3, "me", 5, "by the way", false)},
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "added as a reviewer after my approval (it stays), I commented, a change removed my approval, then back to what I saw: still waits on me",
			versions: []gitlab.Version{version(10, "A"), version(8, "B"), opened},
			notes:    []gitlab.Note{approval, note(2, "bob", 4, "requested review from @me", true), note(3, "me", 5, "by the way", false)},
			want:     []string{"new_commits"}, at: 10,
		},
		{
			// The accepted cost: the notes and versions are those of the
			// second case, so it cannot be told from it. It fires once, at
			// the return, although I knew at :06 that my approval was gone.
			name:     "a change removed my approval, I commented, changed, then back to what I saw: fires once at the return",
			versions: []gitlab.Version{version(10, "B"), version(8, "C"), version(5, "B"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 6, "I saw it", false)},
			want:     []string{"new_commits"}, at: 10,
		},
		{
			name:     "after the return, rebased again: still dated from the return",
			versions: []gitlab.Version{version(12, "B"), version(10, "B"), version(8, "C"), version(5, "B"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 6, "by the way", false)},
			want:     []string{"new_commits"}, at: 10,
		},
		{
			name:     "after the return, I commented, then rebased: I know",
			versions: []gitlab.Version{version(12, "B"), version(10, "B"), version(8, "C"), version(5, "B"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 6, "by the way", false), note(3, "me", 11, "fine", false)},
		},
		{
			name:     "changed and back before my last action, then rebased: I know",
			versions: []gitlab.Version{version(10, "B"), version(7, "B"), version(6, "C"), version(5, "B"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 8, "by the way", false)},
		},
		{
			name:     "a push without a patch ID since my last action is not a change",
			versions: []gitlab.Version{version(10, "B"), version(8, ""), version(5, "B"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 6, "by the way", false)},
		},
		{
			name:     "I took my approval back, changed, then back to what I saw: nothing to lose",
			versions: []gitlab.Version{version(10, "A"), version(8, "B"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 4, unapprovedNote, true)},
		},
		{
			name:     "never approved, changed, then back to what I saw: nothing to lose",
			versions: []gitlab.Version{version(10, "A"), version(8, "B"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, "nit", false)},
		},
		{
			// With my review notes counted as my last action: requesting
			// changes takes my approval back, and what I saw then is B.
			name:     "a change, I requested changes without a comment, changed, then back to what I saw: nothing to lose",
			versions: []gitlab.Version{version(10, "B"), version(8, "C"), version(5, "B"), opened},
			notes:    []gitlab.Note{approval, note(2, "me", 6, requestedChangesNote, true)},
		},

		// B. A retarget before my last action: I acted since, I know.
		{
			name:     "retargeted (my approval is removed), I commented, then rebased: I know",
			versions: []gitlab.Version{version(8, "A"), opened},
			notes:    []gitlab.Note{approval, note(2, "bob", 5, retargetNote, true), note(3, "me", 6, "fine", false)},
		},
		{
			name:     "retargeted, I commented, rebased, I commented, rebased again: I know",
			versions: []gitlab.Version{version(11, "A"), version(8, "A"), opened},
			notes:    []gitlab.Note{approval, note(2, "bob", 5, retargetNote, true), note(3, "me", 6, "fine", false), note(4, "me", 9, "still fine", false)},
		},
		{
			name:     "the target branch was deleted, I commented, then rebased: I know",
			versions: []gitlab.Version{version(8, "A"), opened},
			notes:    []gitlab.Note{approval, note(2, "bob", 5, deletedNote, true), note(3, "me", 6, "fine", false)},
		},
		{
			name:     "retargeted at the instant of my comment, then rebased: I know",
			versions: []gitlab.Version{version(8, "A"), opened},
			notes:    []gitlab.Note{approval, note(2, "bob", 6, retargetNote, true), note(3, "me", 6, "fine", false)},
		},
		{
			name:     "retargeted, I rebased it myself, then it was rebased again: I know",
			versions: []gitlab.Version{version(8, "A"), version(6, "A"), opened},
			notes:    []gitlab.Note{approval, note(2, "bob", 5, retargetNote, true), pushBy(3, "me", 6), pushBy(4, "bob", 8)},
		},
		{
			name:     "retargeted before my approval, I commented, then a rebase removed my approval",
			versions: []gitlab.Version{version(8, "A"), opened},
			notes:    []gitlab.Note{note(1, "bob", 1, retargetNote, true), approval, note(3, "me", 6, "fine", false)},
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "retargeted at the instant of my approval, I commented, then a rebase removed my approval",
			versions: []gitlab.Version{version(8, "A"), opened},
			notes:    []gitlab.Note{note(1, "bob", 3, retargetNote, true), approval, note(3, "me", 6, "fine", false)},
			want:     []string{"new_commits"}, at: 8,
		},
		{
			name:     "retargeted, I commented, then changed and back to what I saw: still waits on me",
			versions: []gitlab.Version{version(10, "A"), version(8, "B"), opened},
			notes:    []gitlab.Note{approval, note(2, "bob", 5, retargetNote, true), note(3, "me", 6, "fine", false)},
			want:     []string{"new_commits"}, at: 10,
		},
		{
			name:     "someone only talks about a target branch before my comment, then a rebase removed my approval",
			versions: []gitlab.Version{version(8, "A"), opened},
			notes:    []gitlab.Note{approval, note(2, "alice", 5, retargetNote, false), note(3, "me", 6, "fine", false)},
			want:     []string{"new_commits"}, at: 8,
		},
	}
	for _, tt := range tests {
		s := snap("bob", tt.notes...)
		s.Versions = tt.versions
		got := reviewItemWith(s, me, yes)
		if !slices.Equal(got.Reasons, tt.want) {
			t.Errorf("%s: reasons = %v, want %v", tt.name, got.Reasons, tt.want)
		}
		if tt.want != nil && !got.At.Equal(at(tt.at)) {
			t.Errorf("%s: at = %v, want %v", tt.name, got.At, at(tt.at))
		}
		// On a GitLab whose patch IDs are not trusted, none of this
		// applies: the newest push, which is never mine here and always
		// after my last action, counts.
		newest := tt.versions[0].CreatedAt
		if got := reviewItem(s, me); !slices.Equal(got.Reasons, []string{"new_commits"}) || !got.At.Equal(newest) {
			t.Errorf("%s, patch IDs not trusted: reasons = %v at %v, want new_commits at %v", tt.name, got.Reasons, got.At, newest)
		}
	}
}
