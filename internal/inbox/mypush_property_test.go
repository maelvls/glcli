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

// history builds a random history of a merge request of bob that I
// review, opened with patch A at :02: comments and review notes of mine,
// pushes of bob and of mine with their notes, review requests. full
// allows my approvals (never standing: I am not among the approvers) and
// retargets.
func history(rng *rand.Rand, full bool) (versions []gitlab.Version, notes []gitlab.Note, story []string, minute int) {
	patches := []string{"A", "B", "C"}
	versions = append(versions, version(2, "A"))
	minute = 2
	for n := rng.Intn(8); n > 0; n-- {
		minute += 1 + rng.Intn(3)
		id := len(notes) + 1
		k := rng.Intn(10)
		if !full && (k == 2 || k == 9) {
			k = 0
		}
		switch k {
		case 0, 1:
			notes = append(notes, note(id, "me", minute, "nit", false))
			story = append(story, fmt.Sprintf(":%02d I comment", minute))
		case 2:
			notes = append(notes, note(id, "me", minute, approvedNote, true))
			story = append(story, fmt.Sprintf(":%02d I approve", minute))
		case 3:
			body := []string{requestedChangesNote, reviewedNote}[rng.Intn(2)]
			notes = append(notes, note(id, "me", minute, body, true))
			story = append(story, fmt.Sprintf(":%02d I: %s", minute, body))
		case 4, 5, 6:
			p := patches[rng.Intn(len(patches))]
			versions = append([]gitlab.Version{version(minute, p)}, versions...)
			notes = append(notes, pushBy(id, "bob", minute))
			story = append(story, fmt.Sprintf(":%02d bob pushes %q", minute, p))
		case 7:
			p := patches[rng.Intn(len(patches))]
			versions = append([]gitlab.Version{version(minute, p)}, versions...)
			notes = append(notes, pushBy(id, "me", minute))
			story = append(story, fmt.Sprintf(":%02d I push %q", minute, p))
		case 8:
			notes = append(notes, note(id, "bob", minute, "requested review from @me", true))
			story = append(story, fmt.Sprintf(":%02d review requested", minute))
		case 9:
			notes = append(notes, note(id, "bob", minute, retargetNote, true))
			story = append(story, fmt.Sprintf(":%02d retargeted", minute))
		}
	}
	return versions, notes, story, minute
}

func itemFor(versions []gitlab.Version, notes []gitlab.Note) Item {
	s := snap("bob", notes...)
	s.Versions = versions
	return reviewItemWith(s, me, yes)
}

// With trusted patch IDs, a push of mine never adds a reason, and never
// makes one newer, whatever came before: inbox wait must not fire for
// what I push myself. One exception to "never newer", pinned in
// TestMyPushAfterARebaseOfSomeoneElse: my push brings the patch back to
// one I know, and a doubt that the change hid (a retarget) is left, with
// its own date.
func TestMyPushAddsNothingWhateverCameBefore(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	changed, moved := 0, 0
	for i := 0; i < histories; i++ {
		versions, notes, story, minute := history(rng, true)
		before := itemFor(versions, notes)
		p := []string{"A", "B", "C", "D"}[rng.Intn(4)]
		if rng.Intn(2) == 0 {
			p = versions[0].PatchIDSHA
		}
		minute += 1 + rng.Intn(3)
		versions = append([]gitlab.Version{version(minute, p)}, versions...)
		notes = append(notes, pushBy(len(notes)+1, "me", minute))
		after := itemFor(versions, notes)
		for _, r := range after.Reasons {
			if !slices.Contains(before.Reasons, r) {
				t.Fatalf("my push %q at :%02d adds %s:\n%s\nbefore: %v at %v\nafter:  %v at %v", p, minute, r, strings.Join(story, "\n"), before.Reasons, before.At, after.Reasons, after.At)
			}
		}
		if len(after.Reasons) != 0 && after.At.After(before.At) {
			// Allowed only when my push brought the patch back to one I
			// know, which leaves a doubt (retarget, approval gone) dated
			// from something that is not my push.
			back := false
			for _, v := range versions[1:] {
				back = back || v.PatchIDSHA == p
			}
			mine := false
			for _, n := range notes {
				mine = mine || n.Author.Username == "me" && n.CreatedAt.Equal(after.At) && strings.HasPrefix(n.Body, "added ")
			}
			if !back || mine || !after.At.Before(at(minute)) {
				t.Fatalf("my push %q at :%02d makes it newer:\n%s\nbefore: %v at %v\nafter:  %v at %v", p, minute, strings.Join(story, "\n"), before.Reasons, before.At, after.Reasons, after.At)
			}
			moved++
		}
		if !slices.Equal(before.Reasons, after.Reasons) {
			changed++
		}
	}
	t.Logf("my push removed a reason in %d histories of %d, and left an older doubt with a newer date in %d", changed, histories, moved)
}

// With trusted patch IDs, no approval of mine and no retarget, a rebase
// by someone else that keeps the newest patch changes nothing, whatever
// came before, pushes of mine included: neither the reasons nor their
// date.
func TestRebaseOfSomeoneElseChangesNothingWhateverCameBefore(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	waiting := 0
	for i := 0; i < histories; i++ {
		versions, notes, story, minute := history(rng, false)
		before := itemFor(versions, notes)
		minute += 1 + rng.Intn(3)
		versions = append([]gitlab.Version{version(minute, versions[0].PatchIDSHA)}, versions...)
		notes = append(notes, pushBy(len(notes)+1, "bob", minute))
		after := itemFor(versions, notes)
		if !slices.Equal(before.Reasons, after.Reasons) || len(after.Reasons) != 0 && !after.At.Equal(before.At) {
			t.Fatalf("a rebase by bob at :%02d changes the item:\n%s\nbefore: %v at %v\nafter:  %v at %v", minute, strings.Join(story, "\n"), before.Reasons, before.At, after.Reasons, after.At)
		}
		if after.Waiting() {
			waiting++
		}
	}
	t.Logf("%d histories of %d had a reason", waiting, histories)
}
