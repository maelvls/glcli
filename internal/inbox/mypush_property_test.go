package inbox

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

// histories is how many random histories each property is tried on.
const histories = 30000

// world is a random history of a merge request of bob that I review,
// opened with patch A at :02: comments and review notes of mine, pushes
// of bob and of mine with their notes, review requests, retargets.
//
// My approval stands or falls as GitLab would make it (modelled by hand):
// it stands from my "approved" note until a push whose patch is not the
// one I approved, a review requested again, a retarget, or my "requested
// changes". While it stands I am among the approvers.
type world struct {
	versions []gitlab.Version
	notes    []gitlab.Note
	story    []string
	minute   int
	// approved is the patch I approved, while my approval stands.
	standing bool
	approved string
}

func (w *world) tick(rng *rand.Rand) { w.minute += 1 + rng.Intn(3) }

func (w *world) note(user, body string, system bool, told string) {
	w.notes = append(w.notes, note(len(w.notes)+1, user, w.minute, body, system))
	w.story = append(w.story, fmt.Sprintf(":%02d %s", w.minute, told))
}

// push adds a diff version and the note GitLab writes under who pushed.
func (w *world) push(user, patch string) {
	w.versions = append([]gitlab.Version{version(w.minute, patch)}, w.versions...)
	w.notes = append(w.notes, pushBy(len(w.notes)+1, user, w.minute))
	w.story = append(w.story, fmt.Sprintf(":%02d %s pushes %q", w.minute, user, patch))
	if patch != w.approved {
		w.standing = false
	}
}

func (w *world) newest() string { return w.versions[0].PatchIDSHA }

func (w *world) item() Item {
	s := snap("bob", w.notes...)
	s.Versions = w.versions
	if w.standing {
		s.Approvals = approvedBy("me")
	}
	return reviewItemWith(s, me, yes)
}

func (w *world) String() string {
	return fmt.Sprintf("%s\n(my approval stands: %v)", strings.Join(w.story, "\n"), w.standing)
}

// history builds a random world; retargets says whether it may have some.
func history(rng *rand.Rand, retargets bool) *world {
	patches := []string{"A", "B", "C"}
	w := &world{versions: []gitlab.Version{version(2, "A")}, minute: 2}
	for n := rng.Intn(8); n > 0; n-- {
		w.tick(rng)
		k := rng.Intn(10)
		if !retargets && k == 9 {
			k = 0
		}
		switch k {
		case 0, 1:
			w.note("me", "nit", false, "I comment")
		case 2:
			w.note("me", approvedNote, true, "I approve")
			w.standing, w.approved = true, w.newest()
		case 3:
			if rng.Intn(2) == 0 {
				w.note("me", requestedChangesNote, true, "I request changes")
				w.standing = false
			} else {
				w.note("me", reviewedNote, true, "I submit a review without a comment")
			}
		case 4, 5, 6:
			w.push("bob", patches[rng.Intn(len(patches))])
		case 7:
			w.push("me", patches[rng.Intn(len(patches))])
		case 8:
			w.note("bob", "requested review from @me", true, "my review is requested")
			w.standing = false
		case 9:
			w.note("bob", retargetNote, true, "bob retargets")
			w.standing = false
		}
	}
	return w
}

// With trusted patch IDs, a push of mine adds no reason and makes none
// newer: inbox wait must not fire for what I push myself. Two exceptions:
//
//   - My push changes the patch I approved, so GitLab takes my approval
//     away: the merge request no longer has it, and a rebase by someone
//     else that had kept it now looks like what removed it (the snapshot
//     cannot tell which push did). new_commits then, dated from that
//     rebase, never from my push.
//   - My push brings the patch back to one I know, and a doubt that the
//     change hid (a retarget) is left, with its own date: newer than the
//     change, older than my push. Pinned in
//     TestMyPushAfterARebaseOfSomeoneElse.
func TestMyPushAddsNothingWhateverCameBefore(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	removed, reset, moved := 0, 0, 0
	for i := 0; i < histories; i++ {
		w := history(rng, true)
		before, stood, told := w.item(), w.standing, w.String()
		p := []string{"A", "B", "C", "D"}[rng.Intn(4)]
		if rng.Intn(2) == 0 {
			p = w.newest()
		}
		w.tick(rng)
		w.push("me", p)
		after := w.item()
		fail := func(what string) {
			t.Helper()
			t.Fatalf("my push %q at :%02d %s:\n%s\nbefore: %v at %v\nafter:  %v at %v", p, w.minute, what, told, before.Reasons, before.At, after.Reasons, after.At)
		}
		if after.Waiting() && !after.At.Before(at(w.minute)) {
			fail("dates the item")
		}
		if stood && !w.standing {
			reset++
			continue
		}
		for _, r := range after.Reasons {
			if !slices.Contains(before.Reasons, r) {
				fail("adds " + r)
			}
		}
		if after.Waiting() && after.At.After(before.At) {
			back := false
			for _, v := range w.versions[1:] {
				back = back || v.PatchIDSHA == p
			}
			if !back {
				fail("makes it newer")
			}
			moved++
		}
		if !slices.Equal(before.Reasons, after.Reasons) {
			removed++
		}
	}
	t.Logf("of %d histories, my push removed a reason in %d, took my approval away in %d, and left an older doubt with a newer date in %d", histories, removed, reset, moved)
}

// With trusted patch IDs and no retarget, a rebase by someone else that
// keeps the newest patch changes nothing, whatever came before, pushes
// and approvals of mine included: neither the reasons nor their date.
// (After a retarget, the rebase is when #7 tells me of it.)
func TestRebaseOfSomeoneElseChangesNothingWhateverCameBefore(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	waiting, approved := 0, 0
	for i := 0; i < histories; i++ {
		w := history(rng, false)
		before, told := w.item(), w.String()
		w.tick(rng)
		w.push("bob", w.newest())
		after := w.item()
		if !slices.Equal(before.Reasons, after.Reasons) || after.Waiting() && !after.At.Equal(before.At) {
			t.Fatalf("a rebase by bob at :%02d changes the item:\n%s\nbefore: %v at %v\nafter:  %v at %v", w.minute, told, before.Reasons, before.At, after.Reasons, after.At)
		}
		if after.Waiting() {
			waiting++
		}
		if w.standing {
			approved++
		}
	}
	t.Logf("of %d histories, %d had a reason and %d my approval standing", histories, waiting, approved)
}
