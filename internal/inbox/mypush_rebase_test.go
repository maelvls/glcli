package inbox

import (
	"slices"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

// A rebase by someone else that keeps the patch I know is nothing for me
// to see, so a push of mine after it is not something to review either:
// with trusted patch IDs, what I pushed is then what I last saw. Without
// them the rebase counts, as every push of someone else does.
//
// The merge request was opened at :00 with a first version at :02 (patch
// A), and I commented at :03 unless the notes say otherwise.
func TestMyPushAfterARebaseOfSomeoneElse(t *testing.T) {
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
	}{
		{
			name:     "it was rebased, then I pushed a commit",
			versions: []gitlab.Version{version(7, "C"), version(6, "A"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 6), pushBy(3, "me", 7)},
			wantOld:  []string{"new_commits"}, atOld: 6,
		},
		{
			name:     "it was rebased, I pushed a commit, then it was rebased again",
			versions: []gitlab.Version{version(9, "C"), version(7, "C"), version(6, "A"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 6), pushBy(3, "me", 7), pushBy(4, "bob", 9)},
			wantOld:  []string{"new_commits"}, atOld: 9,
		},
		{
			name:     "it was rebased, I pushed a commit, it was rebased, I pushed another",
			versions: []gitlab.Version{version(10, "D"), version(9, "C"), version(7, "C"), version(6, "A"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 6), pushBy(3, "me", 7), pushBy(4, "bob", 9), pushBy(5, "me", 10)},
			wantOld:  []string{"new_commits"}, atOld: 9,
		},
		{
			name:     "it was rebased, I pushed a commit, then it changed",
			versions: []gitlab.Version{version(9, "D"), version(7, "C"), version(6, "A"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 6), pushBy(3, "me", 7), pushBy(4, "bob", 9)},
			want:     []string{"new_commits"}, at: 9,
			wantOld: []string{"new_commits"}, atOld: 9,
		},
		{
			name:     "it changed, then I pushed a commit: the change is not seen",
			versions: []gitlab.Version{version(7, "C"), version(6, "B"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 6), pushBy(3, "me", 7)},
			want:     []string{"new_commits"}, at: 6,
			wantOld: []string{"new_commits"}, atOld: 6,
		},
		{
			name:     "a push without a patch ID, then I pushed a commit: in doubt, not seen",
			versions: []gitlab.Version{version(7, "C"), version(6, ""), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 6), pushBy(3, "me", 7)},
			want:     []string{"new_commits"}, at: 6,
			wantOld: []string{"new_commits"}, atOld: 6,
		},
		{
			name:     "it changed, I pushed it back to what I saw, then I pushed a commit",
			versions: []gitlab.Version{version(11, "D"), version(9, "A"), version(8, "B"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 8), pushBy(3, "me", 9), pushBy(4, "me", 11)},
			wantOld:  []string{"new_commits"}, atOld: 8,
		},
		{
			name:     "it changed, it was rebased, then I pushed a commit: dated from the change, as before my push",
			versions: []gitlab.Version{version(12, "B"), version(10, "C"), version(8, "C"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 8), pushBy(3, "bob", 10), pushBy(4, "me", 12)},
			want:     []string{"new_commits"}, at: 8,
			wantOld: []string{"new_commits"}, atOld: 10,
		},
		{
			name:     "I approved a change, pushed a commit, it was rebased, then I pushed again: my pushes are not what removed my approval",
			versions: []gitlab.Version{version(16, "A"), version(13, "B"), version(10, "B"), version(5, "C"), opened},
			notes: []gitlab.Note{
				pushBy(1, "bob", 5), note(2, "me", 7, approvedNote, true), pushBy(3, "me", 10), pushBy(4, "bob", 13), pushBy(5, "me", 16),
			},
			wantOld: []string{"new_commits"}, atOld: 13,
		},
		{
			// The one case where a push of mine makes the date newer: the
			// change of :04 hid the retarget of :07, which is what is left
			// once I undid the change. Not dated from my push.
			name:     "it changed, it was retargeted, then I pushed it back to what I saw: the retarget is left",
			versions: []gitlab.Version{version(13, "A"), version(4, "C"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 4), note(3, "bob", 7, retargetNote, true), pushBy(4, "me", 13)},
			want:     []string{"new_commits"}, at: 7,
			wantOld: []string{"new_commits"}, atOld: 4,
		},

		// My approval is gone since the rebase of :06: no push of mine
		// changes that, nor its date.
		{
			name:     "approved, a rebase removed my approval",
			versions: []gitlab.Version{version(6, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, approvedNote, true), pushBy(2, "bob", 6)},
			want:     []string{"new_commits"}, at: 6,
			wantOld: []string{"new_commits"}, atOld: 6,
		},
		{
			name:     "approved, a rebase removed my approval, then I rebased: still dated from the first rebase",
			versions: []gitlab.Version{version(7, "A"), version(6, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, approvedNote, true), pushBy(2, "bob", 6), pushBy(3, "me", 7)},
			want:     []string{"new_commits"}, at: 6,
			wantOld: []string{"new_commits"}, atOld: 6,
		},
		{
			name:     "approved, a rebase removed my approval, I rebased, then it was rebased: still dated from the first rebase",
			versions: []gitlab.Version{version(9, "A"), version(7, "A"), version(6, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, approvedNote, true), pushBy(2, "bob", 6), pushBy(3, "me", 7), pushBy(4, "bob", 9)},
			want:     []string{"new_commits"}, at: 6,
			wantOld: []string{"new_commits"}, atOld: 9,
		},
		{
			name:     "approved, a rebase removed my approval, then I pushed a commit: still dated from the first rebase",
			versions: []gitlab.Version{version(7, "C"), version(6, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, approvedNote, true), pushBy(2, "bob", 6), pushBy(3, "me", 7)},
			want:     []string{"new_commits"}, at: 6,
			wantOld: []string{"new_commits"}, atOld: 6,
		},
		{
			name:     "approved, a rebase removed my approval, I pushed a commit, then it was rebased: still dated from the first rebase",
			versions: []gitlab.Version{version(9, "C"), version(7, "C"), version(6, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, approvedNote, true), pushBy(2, "bob", 6), pushBy(3, "me", 7), pushBy(4, "bob", 9)},
			want:     []string{"new_commits"}, at: 6,
			wantOld: []string{"new_commits"}, atOld: 9,
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
		}
	}
}
