package inbox

import (
	"slices"
	"testing"
	"time"

	"github.com/maelvls/glcli/internal/gitlab"
	"github.com/maelvls/glcli/internal/mr"
)

// A push of mine never dates new_commits, wherever it sits among the
// pushes of the others: inbox wait must not fire for what I pushed, nor
// for a rebase of it by someone else.
//
// The merge request was opened at :00 with a first version at :02 (patch
// A), and I commented at :03 unless the notes say otherwise.
func TestMyPushNeverDatesNewCommits(t *testing.T) {
	comment := note(1, "me", 3, "nit", false)
	opened := version(2, "A")
	tests := []struct {
		name     string
		versions []gitlab.Version
		notes    []gitlab.Note
		edit     func(*mr.Snapshot)
		// want is what a GitLab with trusted patch IDs gives, wantOld what
		// an older one gives; at and atOld date new_commits.
		want, wantOld []string
		at, atOld     int
	}{
		{
			name:     "it changed, I pushed a commit on top, then it was rebased: dated from the change",
			versions: []gitlab.Version{version(8, "C"), version(6, "C"), version(5, "B"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 5), pushBy(3, "me", 6), pushBy(4, "bob", 8)},
			want:     []string{"new_commits"}, at: 5,
			wantOld: []string{"new_commits"}, atOld: 8,
		},
		{
			name:     "it changed, I pushed a commit and rebased, then it was rebased: dated from the change",
			versions: []gitlab.Version{version(8, "C"), version(7, "C"), version(6, "C"), version(5, "B"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 5), pushBy(3, "me", 6), pushBy(4, "me", 7), pushBy(5, "bob", 8)},
			want:     []string{"new_commits"}, at: 5,
			wantOld: []string{"new_commits"}, atOld: 8,
		},
		{
			name:     "it changed, it was rebased, I pushed a commit, then it was rebased: dated from the change",
			versions: []gitlab.Version{version(9, "C"), version(7, "C"), version(6, "B"), version(5, "B"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 5), pushBy(3, "bob", 6), pushBy(4, "me", 7), pushBy(5, "bob", 9)},
			want:     []string{"new_commits"}, at: 5,
			wantOld: []string{"new_commits"}, atOld: 9,
		},
		{
			name:     "pushes of mine between the pushes of the others, twice: dated from the first change",
			versions: []gitlab.Version{version(10, "C"), version(9, "C"), version(8, "B"), version(7, "B"), version(5, "B"), opened},
			notes: []gitlab.Note{
				comment, pushBy(2, "bob", 5), pushBy(3, "me", 7), pushBy(4, "bob", 8), pushBy(5, "me", 9), pushBy(6, "bob", 10),
			},
			want: []string{"new_commits"}, at: 5,
			wantOld: []string{"new_commits"}, atOld: 10,
		},
		{
			name:     "it changed, I pushed another change, then it was rebased: dated from the change that is not mine",
			versions: []gitlab.Version{version(8, "B"), version(7, "B"), version(5, "D"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 5), pushBy(3, "me", 7), pushBy(4, "bob", 8)},
			want:     []string{"new_commits"}, at: 5,
			wantOld: []string{"new_commits"}, atOld: 8,
		},
		{
			name:     "it changed, it changed again, I pushed a commit, then it was rebased: dated from the last change that is not mine",
			versions: []gitlab.Version{version(9, "C"), version(7, "C"), version(6, "D"), version(5, "B"), opened},
			notes:    []gitlab.Note{comment, pushBy(2, "bob", 5), pushBy(3, "bob", 6), pushBy(4, "me", 7), pushBy(5, "bob", 9)},
			want:     []string{"new_commits"}, at: 6,
			wantOld: []string{"new_commits"}, atOld: 9,
		},

		// What I saw includes the pushes of mine that followed my review.
		{
			name:     "approved, I rebased, then a rebase removed my approval: dated from the rebase that is not mine",
			versions: []gitlab.Version{version(8, "A"), version(5, "A"), opened},
			notes:    []gitlab.Note{note(1, "me", 3, approvedNote, true), pushBy(2, "me", 5), pushBy(3, "bob", 8)},
			want:     []string{"new_commits"}, at: 8,
			wantOld: []string{"new_commits"}, atOld: 8,
		},
		{
			name: "I pushed, it was pushed 30 seconds later, then I commented: seen",
			versions: []gitlab.Version{
				{ID: 3, CreatedAt: at(5).Add(30 * time.Second), HeadCommitSHA: "head530", PatchIDSHA: "D"},
				version(5, "C"), opened,
			},
			notes: []gitlab.Note{pushBy(1, "me", 5), note(2, "me", 6, "nit", false)},
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
