package mr

import (
	"context"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

// A label whose name contains "commit", added after the push, must not
// take the push away from who made it. Only old merge requests have
// label changes as notes.
func TestPushIsNotCreditedToWhoAddedALabel(t *testing.T) {
	c := fakeCompare(t, map[string]string{
		"base1..head1": "@@ -1 +1 @@\n-old\n+new\n",
		"base2..head2": "@@ -7 +7 @@\n-old\n+newer\n",
		"head1..head2": "@@ -7 +7 @@\n-new\n+newer\n",
	})
	s := snapshot()
	label := note(24, "me", 9, `added ~"needs commit" label`)
	label.System = true
	s.Discussions = append(s.Discussions, gitlab.Discussion{ID: "label-00000000000000", Notes: []gitlab.Note{label}})
	prev := Cursor{SHA: "head1", Base: "base1", Version: 1, State: "opened", MaxNote: 10}
	d := &Differ{Client: c, People: People{Me: "me"}}
	events, err := d.Events(context.Background(), prev, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0].Kind != "push" || events[0].By != "alice" || events[0].Mine {
		t.Errorf("events = %+v, want a push by alice first", events)
	}
}

func TestIsPushNote(t *testing.T) {
	tests := []struct {
		body   string
		system bool
		want   bool
	}{
		{"added 1 commit", true, true},
		{"added 1 commit\n\n<ul><li>1a2b3c4d - Fix the typo</li></ul>\n\n[Compare with previous version](/g/p/-/merge_requests/1/diffs?diff_id=2&start_sha=abc)", true, true},
		{"added 5 commits\n\n<ul><li>1a2b3c4d...5e6f7a8b - 4 commits from branch <code>main</code></li></ul>", true, true},
		{"added 12 commits", true, true},
		// Time tracking (/spend, /estimate): stored system notes.
		{"added 1h of time spent at 2026-11-27 16:00:00 UTC", true, false},
		{"added 2h 30m of time spent at 2026-11-27 16:00:00 UTC", true, false},
		{"added time estimate of 1h", true, false},
		// Labels: notes on old merge requests only, label events since.
		{"added ~123 label", true, false},
		{"added ~bug label", true, false},
		{`added ~"needs commit" label`, true, false},
		{"added ~commit label", true, false},
		{"added ~1 ~2 labels", true, false},
		{"added 3 deleted labels", true, false},
		{"added ~1 ~2 + 1 deleted label", true, false},
		{"added ~1 ~2 labels and removed ~3 label", true, false},
		// Not GitLab's wording, but close to it.
		{"added commits", true, false},
		{"added 3 commitments", true, false},
		// The same words written by a person.
		{"added 1 commit", false, false},
		// Not at the start of the note.
		{"mentioned in commit 1a2b3c4d", true, false},
		{"marked as draft and added 1 commit", true, false},
		{"approved this merge request", true, false},
		{"", true, false},
	}
	for _, tt := range tests {
		if got := IsPushNote(gitlab.Note{Body: tt.body, System: tt.system}); got != tt.want {
			t.Errorf("IsPushNote(%q, system=%v) = %v, want %v", tt.body, tt.system, got, tt.want)
		}
	}
}
