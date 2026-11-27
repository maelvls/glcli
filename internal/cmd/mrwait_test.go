package cmd

import (
	"slices"
	"strings"
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

func mrWaitKinds(events []mrWaitEvent) []string {
	out := []string{}
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

// A comment by someone else ends the wait, my own does not, and the
// cursor printed is one from which nothing is new.
func TestMRWaitEndsOnACommentBySomeoneElse(t *testing.T) {
	f := newMRWaitFake(t, "18.5.1-ee")
	opened := mrWaitOpened()
	f.play(opened)
	start := f.get()

	mine := opened.note(11, "me", 2, "one more thing", false)
	commented := mine.note(12, "bob", 3, "done, have a look", false)
	f.play(opened, mine, commented)
	out := f.wait(start.Cursor)
	if !slices.Equal(out.TriggeredBy, []string{"comment"}) || out.TimedOut || len(out.Hints) != 0 {
		t.Fatalf("triggered_by %v, timed_out %v, hints %v; want comment: %s", out.TriggeredBy, out.TimedOut, out.Hints, out.raw)
	}
	if len(out.Events) != 1 || out.Events[0].Kind != "comment" || out.Events[0].By != "bob" || out.Events[0].Comment == nil || out.Events[0].Comment.Body != "done, have a look" {
		t.Errorf("events = %+v, want bob's comment only: %s", out.Events, out.raw)
	}
	if got := f.fetches(); got != 3 {
		t.Errorf("fetched %d times, want 3: nothing, my comment, bob's", got)
	}
	if got := f.requests(); len(got) != 0 {
		t.Errorf("asked %v, want nothing but the merge request", got)
	}

	// From the cursor printed, nothing is new.
	if out.MR.Cursor == "" || out.MR.Cursor == start.Cursor {
		t.Fatalf("mr.cursor = %q, want a new cursor", out.MR.Cursor)
	}
	f.play(commented)
	if again := f.get("--cursor", out.MR.Cursor); len(again.Events) != 0 || !strings.Contains(again.raw, `"events":[]`) {
		t.Errorf("mr get from the new cursor: %s; want no event", again.raw)
	}
	if again := f.wait(out.MR.Cursor, "--timeout", "1m"); !again.TimedOut || len(again.Events) != 0 {
		t.Errorf("mr wait from the new cursor: %s; want a timeout", again.raw)
	}
	// From the first one, the comment is still there.
	if again := f.get("--cursor", start.Cursor); !slices.Equal(mrWaitKinds(again.Events), []string{"comment"}) {
		t.Errorf("mr get from the first cursor: %s; want the comment", again.raw)
	}
}

// On a GitLab whose patch IDs are trusted, a push that keeps the patch ID
// does not end the wait, a push that changes it does, and nothing is
// asked from the compare API to tell them apart.
func TestMRWaitRebaseThenChange(t *testing.T) {
	f := newMRWaitFake(t, "18.5.1-ee")
	f.compare["base2..head3"] = mrWaitComparison("@@ -1 +1 @@\n-old\n+newer\n")
	opened := mrWaitOpened()
	f.play(opened)
	start := f.get()

	rebased := opened.push(2, "head2", "base2", mrWaitPatchA, 5)
	changed := rebased.push(3, "head3", "base2", mrWaitPatchB, 7)
	f.play(opened, rebased, rebased, changed)
	out := f.wait(start.Cursor)
	if !slices.Equal(out.TriggeredBy, []string{"push"}) || out.TimedOut {
		t.Fatalf("triggered_by %v, timed_out %v; want push: %s", out.TriggeredBy, out.TimedOut, out.raw)
	}
	if got := f.fetches(); got != 4 {
		t.Errorf("fetched %d times, want 4: the rebase must not end the wait", got)
	}
	if len(out.Events) != 1 {
		t.Fatalf("events = %+v, want one push: %s", out.Events, out.raw)
	}
	e := out.Events[0]
	if e.Kind != "push" || e.SHA != "head3" || e.PreviousSHA != "head1" || e.RebaseOnly == nil || *e.RebaseOnly || e.By != "alice" || e.Mine || len(e.Commits) != 1 {
		t.Errorf("push = %+v, want head1 to head3 by alice, rebase_only false, one commit: %s", e, out.raw)
	}
	if got, want := f.requests(), []string{"metadata", "commits base2..head3"}; !slices.Equal(got, want) {
		t.Errorf("asked %v, want %v", got, want)
	}

	// The rebase alone: the wait goes on until its time is up.
	f.play(opened, rebased)
	if out := f.wait(start.Cursor, "--timeout", "1m"); !out.TimedOut || len(out.TriggeredBy) != 0 || len(out.Events) != 0 || out.MR.Cursor != start.Cursor {
		t.Errorf("a rebase alone: %s; want a timeout", out.raw)
	}
	// It is still an event, and ends the wait of who asks for it.
	f.play(opened, rebased)
	out = f.wait(start.Cursor, "--on", "rebase")
	if !slices.Equal(out.TriggeredBy, []string{"rebase"}) || len(out.Events) != 1 || out.Events[0].RebaseOnly == nil || !*out.Events[0].RebaseOnly || out.Events[0].SHA != "head2" {
		t.Errorf("--on rebase: %s; want the rebase", out.raw)
	}
	if got := f.requests(); slices.ContainsFunc(got, func(r string) bool { return r != "metadata" }) {
		t.Errorf("asked %v, want no comparison", got)
	}
}

// When nothing tells whether a push changed anything, rebase_only is
// absent and the push ends the wait like a change.
func TestMRWaitRebaseOnlyUnknownEndsTheWait(t *testing.T) {
	diff := "@@ -1 +1 @@\n-old\n+new\n"
	tests := []struct {
		name string
		// newer is what GitLab answers when asked to compare base2..head2.
		newer string
		// want is rebase_only: "true", "false" or "unknown".
		want string
	}{
		{"an answer without diffs", `{"commits": []}`, "unknown"},
		{"a diff too large", `{"commits": [], "diffs": [{"old_path": "a.go", "new_path": "a.go", "diff": "", "too_large": true}]}`, "unknown"},
		{"a binary file", mrWaitComparison("Binary files a/a.go and b/a.go differ\n"), "unknown"},
		{"the commits are gone", "", "unknown"},
		// The same patch ID before 18.2 may hide whitespace: the diffs decide.
		{"another diff", mrWaitComparison("@@ -1 +1 @@\n-old\n+    new\n"), "false"},
		{"the same diff", mrWaitComparison(diff), "true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Before 18.2, patch IDs are not trusted: the compare API is asked.
			f := newMRWaitFake(t, "17.11.4")
			f.compare["base1..head1"] = mrWaitComparison(diff)
			if tt.newer != "" {
				f.compare["base2..head2"] = tt.newer
			}
			opened := mrWaitOpened()
			f.play(opened)
			start := f.get()

			rebased := opened.push(2, "head2", "base2", mrWaitPatchA, 5)
			f.play(opened, rebased)
			out := f.wait(start.Cursor, "--timeout", "1m")
			if tt.want == "true" {
				if !out.TimedOut || len(out.TriggeredBy) != 0 {
					t.Errorf("a rebase: %s; want a timeout", out.raw)
				}
				return
			}
			if !slices.Equal(out.TriggeredBy, []string{"push"}) || out.TimedOut || len(out.Events) != 1 || out.Events[0].Kind != "push" {
				t.Fatalf("triggered_by %v, timed_out %v; want one push: %s", out.TriggeredBy, out.TimedOut, out.raw)
			}
			got := "unknown"
			if r := out.Events[0].RebaseOnly; r != nil {
				got = map[bool]string{true: "true", false: "false"}[*r]
			}
			if got != tt.want || strings.Contains(out.raw, "rebase_only") != (tt.want != "unknown") {
				t.Errorf("rebase_only = %s, want %s: %s", got, tt.want, out.raw)
			}
			if got := f.fetches(); got != 2 {
				t.Errorf("fetched %d times, want 2", got)
			}
		})
	}
}

// My own close does not end the wait as an event, but a closed merge
// request is the end of it: no triggered_by, and a hint. With
// --include-mine it is an event like any other.
func TestMRWaitClosedByMe(t *testing.T) {
	f := newMRWaitFake(t, "18.5.1-ee")
	opened := mrWaitOpened()
	f.play(opened)
	start := f.get()

	closed := opened.state("closed")
	closedAt := mrWaitAt(9)
	closed.MR.ClosedBy, closed.MR.ClosedAt = &gitlab.User{Username: "me"}, &closedAt
	const hint = "The merge request is closed: nothing more will happen to it."

	f.play(opened, closed)
	out := f.wait(start.Cursor)
	if len(out.TriggeredBy) != 0 || strings.Contains(out.raw, "triggered_by") || out.TimedOut || !slices.Contains(out.Hints, hint) {
		t.Errorf("closed by me: %s; want no triggered_by, and the hint", out.raw)
	}
	if len(out.Events) != 1 || out.Events[0].Kind != "state" || out.Events[0].State != "closed" || out.Events[0].PreviousState != "opened" || out.Events[0].By != "me" || !out.Events[0].Mine {
		t.Errorf("events = %+v, want my close: %s", out.Events, out.raw)
	}
	if out.MR.State != "closed" || f.fetches() != 2 {
		t.Errorf("mr.state %q after %d fetches, want closed after 2", out.MR.State, f.fetches())
	}

	f.play(opened, closed)
	out = f.wait(start.Cursor, "--include-mine")
	if !slices.Equal(out.TriggeredBy, []string{"state"}) || len(out.Hints) != 0 || len(out.Events) != 1 || !out.Events[0].Mine {
		t.Errorf("closed by me, --include-mine: %s; want triggered_by state and no hint", out.raw)
	}

	// Closed by someone else: an event that ends the wait.
	closed.MR.ClosedBy = &gitlab.User{Username: "alice"}
	f.play(opened, closed)
	out = f.wait(start.Cursor)
	if !slices.Equal(out.TriggeredBy, []string{"state"}) || len(out.Hints) != 0 || len(out.Events) != 1 || out.Events[0].By != "alice" || out.Events[0].Mine {
		t.Errorf("closed by alice: %s; want triggered_by state", out.raw)
	}
}

// GitLab's "locked", the state of a merge request while it is merged, is
// not reported: whichever polls catch it, the merge is one state event.
func TestMRWaitLockedThenMerged(t *testing.T) {
	f := newMRWaitFake(t, "18.5.1-ee")
	opened := mrWaitOpened()
	f.play(opened)
	start := f.get()

	locked := opened.state("locked")
	merged := opened.state("merged")
	mergedAt := mrWaitAt(9)
	merged.MR.MergeUser, merged.MR.MergedAt, merged.MR.MergeCommitSHA = &gitlab.User{Username: "alice"}, &mergedAt, "mergesha"
	isTheMerge := func(out mrWaitOutput) bool {
		return len(out.Events) == 1 && out.Events[0].Kind == "state" && out.Events[0].State == "merged" &&
			out.Events[0].PreviousState == "opened" && out.Events[0].By == "alice" && out.Events[0].SHA == "mergesha"
	}

	f.play(opened, locked, locked, merged)
	out := f.wait(start.Cursor)
	if !slices.Equal(out.TriggeredBy, []string{"state"}) || !isTheMerge(out) || out.MR.State != "merged" {
		t.Errorf("locked, then merged: %s; want one state event, opened to merged", out.raw)
	}
	if got := f.fetches(); got != 4 {
		t.Errorf("fetched %d times, want 4: locked must not end the wait", got)
	}
	f.play(merged)
	if again := f.get("--cursor", out.MR.Cursor); len(again.Events) != 0 {
		t.Errorf("mr get from the cursor of the merge: %s; want no event", again.raw)
	}

	// Something else ends the wait while the merge request is locked: the
	// cursor printed then does not say "locked", and the merge is still
	// reported once, by the next wait.
	lockedAndCommented := locked.note(12, "bob", 3, "merging now", false)
	f.play(opened, lockedAndCommented)
	out = f.wait(start.Cursor)
	if !slices.Equal(out.TriggeredBy, []string{"comment"}) || !slices.Equal(mrWaitKinds(out.Events), []string{"comment"}) || out.MR.State != "locked" {
		t.Fatalf("a comment while locked: %s; want the comment only", out.raw)
	}
	f.play(lockedAndCommented)
	if again := f.get("--cursor", out.MR.Cursor); len(again.Events) != 0 || again.State != "locked" {
		t.Errorf("still locked: %s; want no event", again.raw)
	}
	mergedAndCommented := merged.note(12, "bob", 3, "merging now", false)
	f.play(lockedAndCommented, mergedAndCommented)
	next := f.wait(out.MR.Cursor)
	if !slices.Equal(next.TriggeredBy, []string{"state"}) || !isTheMerge(next) || f.fetches() != 2 {
		t.Errorf("then merged: %s after %d fetches; want one state event, opened to merged, after 2", next.raw, f.fetches())
	}
	f.play(mergedAndCommented)
	if again := f.get("--cursor", next.MR.Cursor); len(again.Events) != 0 {
		t.Errorf("mr get from the cursor of the merge: %s; want no event", again.raw)
	}
}

// When --timeout runs out, the wait says so and gives back the cursor it
// started from, so that waiting again misses nothing.
func TestMRWaitTimeout(t *testing.T) {
	f := newMRWaitFake(t, "18.5.1-ee")
	opened := mrWaitOpened()
	f.play(opened)
	start := f.get()

	// My own comment arrives meanwhile: not something to wait for.
	mine := opened.note(11, "me", 2, "one more thing", false)
	f.play(opened, mine)
	out := f.wait(start.Cursor, "--timeout", "1m")
	if !out.TimedOut || len(out.TriggeredBy) != 0 || len(out.Events) != 0 || !strings.Contains(out.raw, `"events":[]`) {
		t.Errorf("out = %s; want timed_out and no event", out.raw)
	}
	if out.MR.Cursor != start.Cursor {
		t.Errorf("mr.cursor = %q, want the cursor it started from, %q", out.MR.Cursor, start.Cursor)
	}
	if !slices.ContainsFunc(out.Hints, func(h string) bool { return strings.Contains(h, "--cursor "+start.Cursor) }) {
		t.Errorf("hints = %v, want one that gives the cursor to wait again", out.Hints)
	}
	// A minute of the fake's time, read 10 seconds at a time, is 3 polls.
	if got := f.fetches(); got != 3 {
		t.Errorf("fetched %d times, want 3", got)
	}
}
